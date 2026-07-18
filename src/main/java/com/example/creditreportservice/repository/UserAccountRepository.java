package com.example.creditreportservice.repository;

import com.example.creditreportservice.model.UserAccount;
import org.springframework.data.jpa.repository.JpaRepository;
import org.springframework.stereotype.Repository;

import java.util.Optional;

/**
 * Repository for {@link UserAccount}.
 */
@Repository
public interface UserAccountRepository extends JpaRepository<UserAccount, Long> {

    boolean existsByMobile(String mobile);
    boolean existsByEmail(String email);
    boolean existsByPanNumber(String panNumber);

    Optional<UserAccount> findByMobile(String mobile);

}
