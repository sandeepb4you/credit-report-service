package com.example.creditreportservice.dto;

import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

import java.time.Instant;

/**
 * Response to {@code POST /api/registration/otp/send}.
 * The {@code attemptId} is the client's session token for the rest of the flow.
 */
@Getter
@Setter
@Builder
public class OtpSentResponse {

    private Long attemptId;
    private Instant expiresAt;
    private Integer resendAvailableInSeconds;

}
