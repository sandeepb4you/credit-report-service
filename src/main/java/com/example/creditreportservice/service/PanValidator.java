package com.example.creditreportservice.service;

import com.example.creditreportservice.config.RegistrationProperties;
import com.example.creditreportservice.exception.PanValidationException;
import com.example.creditreportservice.service.ocr.OcrResult;
import lombok.RequiredArgsConstructor;
import org.springframework.stereotype.Service;

import java.util.regex.Pattern;

/**
 * Validates submitted PAN data against the OCR'd image content. Format check
 * is regex-based; image consistency check compares the submitted PAN number
 * (exact) and name (fuzzy via Levenshtein) to the OCR output.
 *
 * <p>Per the chosen design, this does <strong>not</strong> verify PAN
 * authenticity against the income-tax database — only format + OCR consistency.
 */
@Service
@RequiredArgsConstructor
public class PanValidator {

    private static final Pattern PAN_FORMAT =
            Pattern.compile("^[A-Z]{5}[0-9]{4}[A-Z]$");

    private final RegistrationProperties props;

    /** Throws {@link PanValidationException} on any failure; otherwise returns void. */
    public void validate(String submittedPan, String submittedName, OcrResult ocr) {
        validateFormat(submittedPan);
        if (submittedName == null || submittedName.isBlank()) {
            throw new PanValidationException("Full name is required");
        }
        if (ocr == null) {
            throw new PanValidationException("OCR could not read the PAN image");
        }
        if (ocr.confidence() < props.getOcr().getMinConfidence()) {
            throw new PanValidationException(
                    "PAN image was not clear enough (confidence "
                            + String.format("%.2f", ocr.confidence()) + ")");
        }

        // PAN number must match exactly after normalization.
        String ocrPan = ocr.panNumber() == null ? null : ocr.panNumber().toUpperCase().trim();
        String submitted = submittedPan.toUpperCase().trim();
        if (ocrPan == null || !ocrPan.equals(submitted)) {
            throw new PanValidationException(
                    "PAN number on the image does not match the entered value");
        }

        // Name match is fuzzy to tolerate OCR spacing / minor differences.
        if (ocr.name() == null) {
            throw new PanValidationException("Could not read name from the PAN image");
        }
        String a = normalizeName(submittedName);
        String b = normalizeName(ocr.name());
        int distance = levenshtein(a, b);
        if (distance > props.getPan().getNameMatchDistance()) {
            throw new PanValidationException(
                    "Name on the image does not match the entered name");
        }
    }

    static void validateFormat(String pan) {
        if (pan == null || !PAN_FORMAT.matcher(pan.toUpperCase().trim()).matches()) {
            throw new PanValidationException(
                    "PAN must be 5 letters, 4 digits, 1 letter (e.g. ABCDE1234F)");
        }
    }

    private static String normalizeName(String s) {
        return s == null ? ""
                : s.trim()
                        .replaceAll("[^A-Za-z\\s]", "")
                        .replaceAll("\\s+", " ")
                        .toLowerCase();
    }

    /** Classic DP Levenshtein; small strings so O(m*n) is fine. */
    static int levenshtein(String a, String b) {
        int m = a.length();
        int n = b.length();
        if (m == 0) return n;
        if (n == 0) return m;
        int[] prev = new int[n + 1];
        int[] curr = new int[n + 1];
        for (int j = 0; j <= n; j++) prev[j] = j;
        for (int i = 1; i <= m; i++) {
            curr[0] = i;
            for (int j = 1; j <= n; j++) {
                int cost = a.charAt(i - 1) == b.charAt(j - 1) ? 0 : 1;
                curr[j] = Math.min(Math.min(curr[j - 1] + 1, prev[j] + 1), prev[j - 1] + cost);
            }
            int[] tmp = prev; prev = curr; curr = tmp;
        }
        return prev[n];
    }

}
