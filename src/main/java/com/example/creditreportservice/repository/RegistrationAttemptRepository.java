package com.example.creditreportservice.repository;

import com.example.creditreportservice.model.RegistrationAttempt;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.Optional;

/**
 * Repository for in-flight {@link RegistrationAttempt} rows.
 */
@Repository
public interface RegistrationAttemptRepository extends JpaRepository<RegistrationAttempt, Long> {

    /** Most recent attempt for this mobile — used to decide resume-vs-restart. */
    Optional<RegistrationAttempt> findFirstByMobileOrderByCreatedAtDesc(String mobile);

}
