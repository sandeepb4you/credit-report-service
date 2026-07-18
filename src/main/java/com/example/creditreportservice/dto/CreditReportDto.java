package com.example.creditreportservice.dto;

import com.example.creditreportservice.model.CreditReport;
import jakarta.validation.constraints.NotBlank;
import lombok.Builder;
import lombok.Getter;
import lombok.Setter;

import java.time.Instant;

/**
 * Data transfer object for {@link CreditReport} used across the API layer.
 * Keeps the persistence model out of the REST contract.
 */
@Getter
@Setter
@Builder
public class CreditReportDto {

    private Long id;
    @NotBlank
    private String subjectId;
    private Integer score;
    private String status;
    private Instant createdAt;
    private Instant updatedAt;

    public static CreditReportDto from(CreditReport entity) {
        return CreditReportDto.builder()
                .id(entity.getId())
                .subjectId(entity.getSubjectId())
                .score(entity.getScore())
                .status(entity.getStatus())
                .createdAt(entity.getCreatedAt())
                .updatedAt(entity.getUpdatedAt())
                .build();
    }

}
