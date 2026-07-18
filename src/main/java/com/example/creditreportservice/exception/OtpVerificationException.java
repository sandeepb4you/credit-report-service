package com.example.creditreportservice.exception;

/**
 * Raised when an OTP verify attempt fails: wrong code, expired, or too many attempts.
 * Mapped to HTTP 400 by {@link GlobalExceptionHandler}.
 */
public class OtpVerificationException extends RuntimeException {

    public OtpVerificationException(String message) {
        super(message);
    }

}
