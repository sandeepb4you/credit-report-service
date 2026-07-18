package com.example.creditreportservice.repository;

import com.example.creditreportservice.model.CreditReport;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.Optional;

/**
 * Spring Data JPA repository for {@link CreditReport}.
 */
@Repository
public interface CreditReportRepository extends JpaRepository<CreditReport, Long> {

    Optional<CreditReport> findBySubjectId(String subjectId);

}
