package com.example.creditreportservice.service.ocr;

/**
 * Result of OCR on a PAN card image. {@link #confidence} ranges 0..1 and is
 * provider-reported when available, otherwise best-effort.
 */
public record OcrResult(String text, String panNumber, String name, double confidence) {
}
