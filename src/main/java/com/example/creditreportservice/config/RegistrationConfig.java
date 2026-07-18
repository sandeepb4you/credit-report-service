package com.example.creditreportservice.config;

import org.springframework.boot.context.properties.EnableConfigurationProperties;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.crypto.bcrypt.BCryptPasswordEncoder;
import org.springframework.security.crypto.password.PasswordEncoder;

/**
 * Wires registration-related beans. Kept deliberately small: just the
 * {@link RegistrationProperties} type and a {@link PasswordEncoder} for OTP hashing.
 */
@Configuration
@EnableConfigurationProperties(RegistrationProperties.class)
public class RegistrationConfig {

    /**
     * BCrypt is intentionally strong for OTP-at-rest hashing. Each verify is a
     * single BCrypt compare — negligible cost at registration request rates.
     */
    @Bean
    public PasswordEncoder passwordEncoder() {
        return new BCryptPasswordEncoder();
    }

}
