package com.example.creditreportservice.config;

import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import lombok.Getter;
import lombok.Setter;
import org.springframework.boot.context.properties.ConfigurationProperties;
import org.springframework.validation.annotation.Validated;

/**
 * Tunes the registration flow without code changes. Bound from
 * {@code app.registration.*} in {@code application.yml}.
 */
@Getter
@Setter
@Validated
@ConfigurationProperties(prefix = "app.registration")
public class RegistrationProperties {

    private final Otp otp = new Otp();
    private final Pan pan = new Pan();
    private final Ocr ocr = new Ocr();

    /** Local directory where PAN card uploads are written. */
    @NotBlank
    private String panImageDir = "./data/pan-images";

    @Getter
    @Setter
    public static class Otp {
        @Min(4)
        private int length = 6;
        /** OTP validity window in seconds. */
        @Min(30)
        private int ttlSeconds = 300;
        /** Minimum gap between consecutive sends for the same attempt. */
        @Min(0)
        private int resendCooldownSeconds = 60;
        /** Max wrong-OTP attempts before the attempt is locked. */
        @Min(1)
        private int maxAttempts = 5;
        /** Max times an OTP may be (re)sent before the attempt is locked. */
        @Min(1)
        private int maxSends = 5;
    }

    @Getter
    @Setter
    public static class Pan {
        /** Maximum Levenshtein distance allowed between submitted and OCR name. */
        @Min(0)
        private int nameMatchDistance = 2;
    }

    @Getter
    @Setter
    public static class Ocr {
        /** "stub" or "google-vision". */
        @NotBlank
        private String provider = "stub";
        /** Minimum confidence (0..1) for an OCR result to be trusted. */
        @Min(0)
        private double minConfidence = 0.8;
    }

}
