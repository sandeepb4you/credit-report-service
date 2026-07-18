package com.example.creditreportservice.service;

import com.example.creditreportservice.config.RegistrationProperties;
import com.example.creditreportservice.exception.OtpVerificationException;
import com.example.creditreportservice.exception.RegistrationStateException;
import com.example.creditreportservice.model.RegistrationAttempt;
import com.example.creditreportservice.model.RegistrationStatus;
import lombok.RequiredArgsConstructor;
import org.springframework.security.crypto.password.PasswordEncoder;
import org.springframework.stereotype.Service;

import java.security.SecureRandom;
import java.time.Duration;
import java.time.Instant;

/**
 * OTP generation, hashing, expiry, and rate-limit checks. Stateless relative to
 * persistence — operates on a {@link RegistrationAttempt} passed in by the
 * orchestrating {@link RegistrationService}.
 */
@Service
@RequiredArgsConstructor
public class OtpService {

    private static final SecureRandom RNG = new SecureRandom();

    private final RegistrationProperties props;
    private final PasswordEncoder passwordEncoder;

    /** Generates a fresh OTP, stashes its hash + expiry on the attempt, returns plaintext. */
    public String issue(RegistrationAttempt attempt) {
        RegistrationProperties.Otp cfg = props.getOtp();
        Instant now = Instant.now();

        if (attempt.getOtpSendCount() != null && attempt.getOtpSendCount() >= cfg.getMaxSends()) {
            throw new RegistrationStateException(
                    "OTP resend limit reached; please restart registration");
        }
        if (attempt.getLastOtpSentAt() != null && attempt.getStatus() == RegistrationStatus.STARTED) {
            long elapsed = Duration.between(attempt.getLastOtpSentAt(), now).getSeconds();
            if (elapsed < cfg.getResendCooldownSeconds()) {
                throw new RegistrationStateException(
                        "Please wait " + (cfg.getResendCooldownSeconds() - elapsed)
                                + "s before requesting a new OTP");
            }
        }

        String plain = generateNumeric(cfg.getLength());
        attempt.setOtpHash(passwordEncoder.encode(plain));
        attempt.setOtpExpiresAt(now.plusSeconds(cfg.getTtlSeconds()));
        attempt.setLastOtpSentAt(now);
        attempt.setOtpSendCount((attempt.getOtpSendCount() == null ? 0 : attempt.getOtpSendCount()) + 1);
        attempt.setOtpAttempts(0);
        return plain;
    }

    /** Throws {@link OtpVerificationException} on any failure; otherwise returns void. */
    public void verify(RegistrationAttempt attempt, String suppliedOtp) {
        RegistrationProperties.Otp cfg = props.getOtp();
        if (attempt.getStatus() != RegistrationStatus.STARTED) {
            throw new RegistrationStateException(
                    "OTP already consumed or attempt is not in OTP stage");
        }
        if (attempt.getOtpHash() == null) {
            throw new RegistrationStateException("No OTP was issued for this attempt");
        }
        if (attempt.getOtpExpiresAt() == null || attempt.getOtpExpiresAt().isBefore(Instant.now())) {
            throw new OtpVerificationException("OTP expired; please request a new one");
        }
        int attempts = (attempt.getOtpAttempts() == null ? 0 : attempt.getOtpAttempts()) + 1;
        attempt.setOtpAttempts(attempts);
        if (attempts > cfg.getMaxAttempts()) {
            throw new OtpVerificationException("Too many wrong attempts; request a new OTP");
        }
        if (!passwordEncoder.matches(suppliedOtp, attempt.getOtpHash())) {
            throw new OtpVerificationException("Invalid OTP");
        }
        // success — clear OTP fields, advance state
        attempt.setOtpHash(null);
        attempt.setOtpExpiresAt(null);
        attempt.setOtpAttempts(0);
        attempt.setStatus(RegistrationStatus.OTP_VERIFIED);
    }

    private String generateNumeric(int length) {
        StringBuilder sb = new StringBuilder(length);
        // Bound to 0..9 inclusive with uniform distribution.
        for (int i = 0; i < length; i++) {
            sb.append(RNG.nextInt(10));
        }
        return sb.toString();
    }

}
