package com.example.creditreportservice.controller;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.Map;

/**
 * Simple liveness/readiness endpoint in addition to Spring Boot Actuator.
 * Place domain-specific {@code @RestController} classes alongside this one
 * (e.g. {@code CreditReportController}).
 */
@RestController
@RequestMapping("/api")
public class HealthController {

    @GetMapping("/ping")
    public Map<String, String> ping() {
        return Map.of("status", "UP", "service", "credit-report-service");
    }

}
