package com.example.creditreportservice.service;

import com.example.creditreportservice.dto.CreditReportDto;
import com.example.creditreportservice.exception.ResourceNotFoundException;
import com.example.creditreportservice.model.CreditReport;
import com.example.creditreportservice.repository.CreditReportRepository;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;
import org.springframework.transaction.annotation.Transactional;

import java.util.List;

/**
 * Business logic for credit reports.
 *
 * <p>Annotate public methods with {@link Transactional @Transactional} where the
 * operation spans repository calls or mutates state.
 */
@Service
@RequiredArgsConstructor
public class CreditReportService {

    private final CreditReportRepository creditReportRepository;

    @Transactional(readOnly = true)
    public List<CreditReportDto> findAll() {
        return creditReportRepository.findAll().stream()
                .map(CreditReportDto::from)
                .toList();
    }

    @Transactional(readOnly = true)
    public CreditReportDto findById(Long id) {
        return creditReportRepository.findById(id)
                .map(CreditReportDto::from)
                .orElseThrow(() -> new ResourceNotFoundException(
                        "Credit report not found with id " + id));
    }

    @Transactional(readOnly = true)
    public CreditReportDto findBySubjectId(String subjectId) {
        return creditReportRepository.findBySubjectId(subjectId)
                .map(CreditReportDto::from)
                .orElseThrow(() -> new ResourceNotFoundException(
                        "Credit report not found for subject " + subjectId));
    }

    @Transactional
    public CreditReportDto create(CreditReportDto dto) {
        CreditReport entity = CreditReport.builder()
                .subjectId(dto.getSubjectId())
                .score(dto.getScore())
                .status(dto.getStatus())
                .build();
        return CreditReportDto.from(creditReportRepository.save(entity));
    }

    @Transactional
    public void deleteById(Long id) {
        if (!creditReportRepository.existsById(id)) {
            throw new ResourceNotFoundException("Credit report not found with id " + id);
        }
        creditReportRepository.deleteById(id);
    }

}
