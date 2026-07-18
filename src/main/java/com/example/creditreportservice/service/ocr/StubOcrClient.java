package com.example.creditreportservice.service.ocr;

import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.stereotype.Component;

/**
 * Dev-only OCR client that returns a deterministic mock result. Lets the flow
 * run end-to-end without any cloud credentials.
 *
 * <p>Enabled when {@code app.registration.ocr.provider=stub}.
 */
@Component
@ConditionalOnProperty(prefix = "app.registration.ocr", name = "provider", havingValue = "stub", matchIfMissing = true)
public class StubOcrClient implements OcrClient {

    @Override
    public OcrResult extract(byte[] imageBytes, String contentType) {
        // Deterministic placeholder. Tests / dev runs can rely on this.
        String text = "INCOME TAX DEPARTMENT\nNAME: SAMPLE USER\nABCDE1234F";
        return new OcrResult(
                text,
                PanTextUtils.extractPan(text),
                PanTextUtils.extractName(text),
                1.0);
    }

}
