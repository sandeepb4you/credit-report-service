package com.example.creditreportservice.dto;

import com.example.creditreportservice.model.RegistrationStatus;
import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

/**
 * Response to {@code POST /api/registration/pan} on success.
 */
@Getter
@Setter
@Builder
public class SubmitPanResponse {

    private Long userId;
    private Long attemptId;
    private RegistrationStatus status;

}
