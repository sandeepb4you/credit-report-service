package com.example.creditreportservice.controller;

import com.example.creditreportservice.dto.OtpSentResponse;
import com.example.creditreportservice.dto.OtpVerifiedResponse;
import com.example.creditreportservice.dto.SendOtpRequest;
import com.example.creditreportservice.dto.SubmitPanResponse;
import com.example.creditreportservice.dto.VerifyOtpRequest;
import com.example.creditreportservice.service.RegistrationService;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RequestPart;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.multipart.MultipartFile;

import java.time.LocalDate;

/**
 * Multi-step registration endpoints.
 *
 * <pre>
 * POST /api/registration/otp/send    (JSON)            -> OtpSentResponse
 * POST /api/registration/otp/verify   (JSON)            -> OtpVerifiedResponse
 * POST /api/registration/pan          (multipart/form)  -> SubmitPanResponse
 * </pre>
 */
@RestController
@RequestMapping("/api/registration")
@RequiredArgsConstructor
public class RegistrationController {

    private final RegistrationService registrationService;

    @PostMapping("/otp/send")
    public OtpSentResponse sendOtp(@Valid @RequestBody SendOtpRequest req) {
        return registrationService.sendOtp(req);
    }

    @PostMapping("/otp/verify")
    public OtpVerifiedResponse verifyOtp(@Valid @RequestBody VerifyOtpRequest req) {
        return registrationService.verifyOtp(req);
    }

    /**
     * Multipart endpoint — the Android client posts the PAN photo plus the
     * typed-in fields. {@code image} is required.
     */
    @PostMapping(value = "/pan", consumes = "multipart/form-data")
    public ResponseEntity<SubmitPanResponse> submitPan(
            @RequestParam("attemptId") Long attemptId,
            @RequestParam("panNumber") String panNumber,
            @RequestParam("fullName") String fullName,
            @RequestParam(value = "dateOfBirth", required = false)
            LocalDate dateOfBirth,
            @RequestPart("image") MultipartFile image) {
        SubmitPanResponse body = registrationService.submitPan(
                attemptId, panNumber, fullName, dateOfBirth, image);
        return ResponseEntity.status(HttpStatus.CREATED).body(body);
    }

}
