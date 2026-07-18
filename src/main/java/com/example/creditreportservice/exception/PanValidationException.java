package com.example.creditreportservice.exception;

/**
 * Raised when PAN format is invalid or the OCR result on the uploaded image
 * does not match the submitted PAN number / name. Mapped to HTTP 422.
 *
 * <p>The exception intentionally carries no PII in its message; structured
 * details are added by {@link GlobalExceptionHandler} so the client can show
 * the failing field without leaking OCR noise.
 */
public class PanValidationException extends RuntimeException {

    public PanValidationException(String message) {
        super(message);
    }

}
