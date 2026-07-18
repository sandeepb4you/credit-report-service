package com.example.creditreportservice.dto;

import jakarta.validation.constraints.Email;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

/**
 * Stage 1 request: client provides mobile and email. OTP is delivered to the email.
 */
@Getter
@Setter
@Builder
public class SendOtpRequest {

    @NotBlank
    @Pattern(regexp = "^[6-9]\\d{9}$", message = "mobile must be a 10-digit Indian mobile number")
    private String mobile;

    @NotBlank
    @Email(message = "email must be valid")
    private String email;

}
