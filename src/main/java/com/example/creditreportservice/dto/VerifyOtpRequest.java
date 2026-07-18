package com.example.creditreportservice.dto;

import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;
import jakarta.validation.constraints.Pattern;
import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

/**
 * Stage 1 confirmation: client returns the OTP sent to the email.
 */
@Getter
@Setter
@Builder
public class VerifyOtpRequest {

    @NotNull
    private Long attemptId;

    @NotBlank
    @Pattern(regexp = "\\d{4,8}$", message = "otp must be digits")
    private String otp;

}
