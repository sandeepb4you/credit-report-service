package com.example.creditreportservice.controller;

import com.example.creditreportservice.dto.CreditReportDto;
import com.example.creditreportservice.service.CreditReportService;
import jakarta.validation.Valid;
import lombok.RequiredArgsConstructor;
import org.springframework.http.HttpStatus;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

import java.util.List;

/**
 * REST endpoints for credit reports.
 */
@RestController
@RequestMapping("/api/credit-reports")
@RequiredArgsConstructor
public class CreditReportController {

    private final CreditReportService creditReportService;

    @GetMapping
    public List<CreditReportDto> getAll() {
        return creditReportService.findAll();
    }

    @GetMapping("/{id}")
    public CreditReportDto getById(@PathVariable Long id) {
        return creditReportService.findById(id);
    }

    @GetMapping("/by-subject/{subjectId}")
    public CreditReportDto getBySubjectId(@PathVariable String subjectId) {
        return creditReportService.findBySubjectId(subjectId);
    }

    @PostMapping
    public ResponseEntity<CreditReportDto> create(@Valid @RequestBody CreditReportDto dto) {
        CreditReportDto created = creditReportService.create(dto);
        return ResponseEntity.status(HttpStatus.CREATED).body(created);
    }

    @DeleteMapping("/{id}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void delete(@PathVariable Long id) {
        creditReportService.deleteById(id);
    }

}
