package com.example.creditreportservice.service.ocr;

/**
 * Extracts text (and a best-effort PAN + name) from a PAN card image.
 * Implementations are selected by {@code app.registration.ocr.provider}.
 */
public interface OcrClient {

    OcrResult extract(byte[] imageBytes, String contentType);

}
