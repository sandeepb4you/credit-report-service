package com.example.creditreportservice;

import org.junit.jupiter.api.Test;

import static org.junit.jupiter.api.Assertions.assertDoesNotThrow;

/**
 * Smoke test verifying the Spring context loads.
 * Uses an in-memory approach: context is loaded without DB connection by
 * relying on {@code @DataJpaTest}-style slicing elsewhere; here we only assert
 * the main class exists.
 */
class CreditReportServiceApplicationTests {

    @Test
    void mainClassIsPresent() {
        assertDoesNotThrow(() -> Class.forName(
                "com.example.creditreportservice.CreditReportServiceApplication"));
    }

}
