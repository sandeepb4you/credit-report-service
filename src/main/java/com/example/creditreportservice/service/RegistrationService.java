package com.example.creditreportservice.service;

import com.example.creditreportservice.config.RegistrationProperties;
import com.example.creditreportservice.dto.OtpSentResponse;
import com.example.creditreportservice.dto.OtpVerifiedResponse;
import com.example.creditreportservice.dto.SendOtpRequest;
import com.example.creditreportservice.dto.SubmitPanResponse;
import com.example.creditreportservice.dto.VerifyOtpRequest;
import com.example.creditreportservice.exception.RegistrationStateException;
import com.example.creditreportservice.model.RegistrationAttempt;
import com.example.creditreportservice.model.RegistrationStatus;
import com.example.creditreportservice.model.UserAccount;
import com.example.creditreportservice.repository.RegistrationAttemptRepository;
import com.example.creditreportservice.repository.UserAccountRepository;
import com.example.creditreportservice.service.ocr.OcrClient;
import com.example.creditreportservice.service.ocr.OcrResult;
import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;
import org.springframework.web.multipart.MultipartFile;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.time.Instant;
import java.time.LocalDate;
import java.util.UUID;

/**
 * Orchestrates the 3-stage registration flow.
 *
 * <ol>
 *     <li>OTP send → returns attemptId</li>
 *     <li>OTP verify → flips attempt to {@code OTP_VERIFIED}</li>
 *     <li>PAN submit + OCR → creates a {@link UserAccount}, attempt becomes {@code PAN_VERIFIED}</li>
 * </ol>
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class RegistrationService {

    private final RegistrationAttemptRepository attemptRepo;
    private final UserAccountRepository userRepo;
    private final OtpService otpService;
    private final MailService mailService;
    private final OcrClient ocrClient;
    private final PanValidator panValidator;
    private final RegistrationProperties props;

    // ------------------------------------------------------------------
    // Stage 1 — send OTP
    // ------------------------------------------------------------------

    @Transactional
    public OtpSentResponse sendOtp(SendOtpRequest req) {
        // Reject duplicates against already-confirmed users up front.
        if (userRepo.existsByMobile(req.getMobile())) {
            throw new RegistrationStateException("Mobile number already registered");
        }
        if (userRepo.existsByEmail(req.getEmail())) {
            throw new RegistrationStateException("Email already registered");
        }

        // Reuse the latest in-flight attempt for this mobile, if any; else start fresh.
        RegistrationAttempt attempt = attemptRepo
                .findFirstByMobileOrderByCreatedAtDesc(req.getMobile())
                .filter(a -> a.getStatus() == RegistrationStatus.STARTED)
                .orElseGet(() -> RegistrationAttempt.builder()
                        .mobile(req.getMobile())
                        .email(req.getEmail())
                        .build());
        attempt.setEmail(req.getEmail());

        String plainOtp = otpService.issue(attempt);
        attempt = attemptRepo.save(attempt);

        // Mail outside the txn would be ideal, but JavaMailSender has no tx awareness;
        // we accept best-effort here and let the caller retry on failure.
        mailService.sendOtp(req.getEmail(), plainOtp);

        return OtpSentResponse.builder()
                .attemptId(attempt.getId())
                .expiresAt(attempt.getOtpExpiresAt())
                .resendAvailableInSeconds(props.getOtp().getResendCooldownSeconds())
                .build();
    }

    // ------------------------------------------------------------------
    // Stage 1 — verify OTP
    // ------------------------------------------------------------------

    @Transactional
    public OtpVerifiedResponse verifyOtp(VerifyOtpRequest req) {
        RegistrationAttempt attempt = requireAttempt(req.getAttemptId());
        otpService.verify(attempt, req.getOtp());
        attemptRepo.save(attempt);
        return OtpVerifiedResponse.builder()
                .attemptId(attempt.getId())
                .status(attempt.getStatus())
                .build();
    }

    // ------------------------------------------------------------------
    // Stage 2 — submit PAN + image
    // ------------------------------------------------------------------

    @Transactional
    public SubmitPanResponse submitPan(Long attemptId,
                                       String panNumber,
                                       String fullName,
                                       LocalDate dateOfBirth,
                                       MultipartFile image) {
        RegistrationAttempt attempt = requireAttempt(attemptId);
        if (attempt.getStatus() != RegistrationStatus.OTP_VERIFIED) {
            throw new RegistrationStateException(
                    "PAN submission requires a verified OTP. Current status: "
                            + attempt.getStatus());
        }
        if (image == null || image.isEmpty()) {
            throw new RegistrationStateException("PAN card image is required");
        }

        // Pre-flight uniqueness check (in addition to the DB unique constraint).
        String pan = panNumber == null ? null : panNumber.toUpperCase().trim();
        if (pan != null && userRepo.existsByPanNumber(pan)) {
            throw new RegistrationStateException("PAN number already registered");
        }

        Path saved = persistImage(attempt, image);
        OcrResult ocr;
        try {
            ocr = ocrClient.extract(image.getBytes(), image.getContentType());
        } catch (IOException e) {
            throw new RegistrationStateException("Could not read uploaded image");
        }

        // Throws PanValidationException on mismatch — attempt stays OTP_VERIFIED for retry.
        panValidator.validate(pan, fullName, ocr);

        UserAccount user = UserAccount.builder()
                .mobile(attempt.getMobile())
                .email(attempt.getEmail())
                .panNumber(pan)
                .fullName(fullName.trim())
                .dateOfBirth(dateOfBirth)
                .panImagePath(saved.toString())
                .build();
        user = userRepo.save(user);

        attempt.setPanNumber(pan);
        attempt.setPanName(fullName);
        attempt.setDateOfBirth(dateOfBirth);
        attempt.setPanImagePath(saved.toString());
        attempt.setOcrPanNumber(ocr.panNumber());
        attempt.setOcrPanName(ocr.name());
        attempt.setUserAccount(user);
        attempt.setStatus(RegistrationStatus.PAN_VERIFIED);
        attemptRepo.save(attempt);

        log.info("Registration complete for mobile={} userId={}", attempt.getMobile(), user.getId());

        return SubmitPanResponse.builder()
                .userId(user.getId())
                .attemptId(attempt.getId())
                .status(attempt.getStatus())
                .build();
    }

    // ------------------------------------------------------------------
    // helpers
    // ------------------------------------------------------------------

    private RegistrationAttempt requireAttempt(Long id) {
        return attemptRepo.findById(id)
                .orElseThrow(() -> new RegistrationStateException(
                        "Registration attempt not found: " + id));
    }

    private Path persistImage(RegistrationAttempt attempt, MultipartFile image) {
        try {
            Path dir = Path.of(props.getPanImageDir());
            Files.createDirectories(dir);
            String original = image.getOriginalFilename() == null
                    ? "pan" : image.getOriginalFilename();
            String suffix = original.lastIndexOf('.') >= 0
                    ? original.substring(original.lastIndexOf('.')) : ".jpg";
            Path target = dir.resolve("pan_" + attempt.getId()
                    + "_" + UUID.randomUUID() + suffix);
            try (var in = image.getInputStream()) {
                Files.copy(in, target, StandardCopyOption.REPLACE_EXISTING);
            }
            return target;
        } catch (IOException e) {
            throw new RegistrationStateException("Could not store PAN image");
        }
    }

    /** Visible for scheduler/test use later — flips expired attempts. */
    @Transactional
    public int expireStaleAttempts(Instant cutoff) {
        // Bulk update is straightforward via a @Modifying query later; for now,
        // a simple load-and-flip keeps the code query-light.
        int n = 0;
        for (RegistrationAttempt a : attemptRepo.findAll()) {
            if (a.getStatus() == RegistrationStatus.STARTED
                    && a.getOtpExpiresAt() != null
                    && a.getOtpExpiresAt().isBefore(cutoff)) {
                a.setStatus(RegistrationStatus.EXPIRED);
                attemptRepo.save(a);
                n++;
            }
        }
        return n;
    }

}
