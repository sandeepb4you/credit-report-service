package com.example.creditreportservice.exception;

/**
 * Raised when a request targets a registration attempt that is missing, expired,
 * or in the wrong lifecycle stage for the operation.
 * Mapped to HTTP 409 by {@link GlobalExceptionHandler}.
 */
public class RegistrationStateException extends RuntimeException {

    public RegistrationStateException(String message) {
        super(message);
    }

}
