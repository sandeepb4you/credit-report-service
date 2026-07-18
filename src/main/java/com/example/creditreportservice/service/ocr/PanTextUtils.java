package com.example.creditreportservice.service.ocr;

import java.util.ArrayList;
import java.util.List;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * Helpers for post-processing raw OCR text into a PAN number and holder name.
 * Shared by every {@link OcrClient} implementation so the parsing rules stay
 * in one place.
 */
final class PanTextUtils {

    /** Standard Indian PAN format: 5 letters, 4 digits, 1 letter. */
    private static final Pattern PAN_PATTERN = Pattern.compile("\\b([A-Z]{5}[0-9]{4}[A-Z])\\b");

    private PanTextUtils() {}

    /**
     * Find the first token that looks like a PAN. Returns uppercase or null.
     */
    static String extractPan(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        Matcher m = PAN_PATTERN.matcher(text.toUpperCase());
        return m.find() ? m.group(1) : null;
    }

    /**
     * Best-effort name extraction. Indian PAN cards print the holder's name on
     * a line near the words {@code NAME} / {@code /NAME}. We capture the line
     * following such a marker; if no marker exists, fall back to the longest
     * whitespace-separated run of title-case words.
     */
    static String extractName(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        String[] lines = text.split("\\r?\\n");
        for (int i = 0; i < lines.length; i++) {
            String line = lines[i].trim();
            if (line.matches("(?i).*(^|\\s)(name|নাম)(\\s|/|:).*") && i + 1 < lines.length) {
                String candidate = lines[i + 1].trim();
                if (looksLikePersonName(candidate)) {
                    return normalizeName(candidate);
                }
            }
        }
        // Fallback: longest run of title-case words.
        String best = "";
        for (String line : lines) {
            String t = line.trim();
            if (looksLikePersonName(t) && t.length() > best.length()) {
                best = t;
            }
        }
        return best.isBlank() ? null : normalizeName(best);
    }

    private static boolean looksLikePersonName(String s) {
        // At least two alphabetic tokens, each starting with a letter.
        String[] tokens = s.split("\\s+");
        List<String> ok = new ArrayList<>();
        for (String tk : tokens) {
            String cleaned = tk.replaceAll("[^A-Za-z]", "");
            if (!cleaned.isBlank() && Character.isLetter(cleaned.charAt(0))) {
                ok.add(cleaned);
            }
        }
        return ok.size() >= 2;
    }

    private static String normalizeName(String s) {
        // collapse whitespace, strip trailing punctuation
        return s.trim().replaceAll("\\s+", " ").replaceAll("[.,;:]+$", "");
    }

}
