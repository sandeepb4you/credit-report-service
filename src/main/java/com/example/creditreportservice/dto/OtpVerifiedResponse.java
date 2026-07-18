package com.example.creditreportservice.dto;

import com.example.creditreportservice.model.RegistrationStatus;
import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

/**
 * Response to {@code POST /api/registration/otp/verify}.
 */
@Getter
@Setter
@Builder
public class OtpVerifiedResponse {

    private Long attemptId;
    private RegistrationStatus status;

}
