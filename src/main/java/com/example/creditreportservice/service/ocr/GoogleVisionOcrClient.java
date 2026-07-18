package com.example.creditreportservice.service.ocr;

import com.google.cloud.vision.v1.AnnotateImageRequest;
import com.google.cloud.vision.v1.AnnotateImageResponse;
import com.google.cloud.vision.v1.Feature;
import com.google.cloud.vision.v1.Image;
import com.google.cloud.vision.v1.ImageAnnotatorClient;
import com.google.protobuf.ByteString;
import lombok.extern.slf4j.Slf4j;
import org.springframework.boot.autoconfigure.condition.ConditionalOnProperty;
import org.springframework.stereotype.Component;

import java.util.List;

/**
 * Google Cloud Vision backed OCR. Authenticates via standard Google credentials
 * (set {@code GOOGLE_APPLICATION_CREDENTIALS} to a service-account JSON path).
 *
 * <p>Enabled when {@code app.registration.ocr.provider=google-vision}.
 */
@Slf4j
@Component
@ConditionalOnProperty(prefix = "app.registration.ocr", name = "provider", havingValue = "google-vision")
public class GoogleVisionOcrClient implements OcrClient {

    @Override
    public OcrResult extract(byte[] imageBytes, String contentType) {
        ByteString imgBytes = ByteString.copyFrom(imageBytes);
        Image img = Image.newBuilder().setContent(imgBytes).build();
        Feature feature = Feature.newBuilder().setType(Feature.Type.DOCUMENT_TEXT_DETECTION).build();
        AnnotateImageRequest request = AnnotateImageRequest.newBuilder()
                .addFeatures(feature)
                .setImage(img)
                .build();

        // try-with-resources: ImageAnnotatorClient is Closeable and thread-safe to share.
        try (ImageAnnotatorClient client = ImageAnnotatorClient.create()) {
            AnnotateImageResponse response = client.batchAnnotateImages(List.of(request))
                    .getResponses(0);
            if (response.hasError()) {
                log.warn("Vision API returned an error: {}", response.getError().getMessage());
                return new OcrResult("", null, null, 0.0);
            }
            String text = response.getFullTextAnnotation().getText();
            return new OcrResult(
                    text,
                    PanTextUtils.extractPan(text),
                    PanTextUtils.extractName(text),
                    confidenceOf(response));
        } catch (Exception e) {
            log.error("Google Vision OCR failed", e);
            throw new RuntimeException("OCR provider unavailable", e);
        }
    }

    private static double confidenceOf(AnnotateImageResponse response) {
        if (response.getFullTextAnnotation().getPagesCount() == 0) {
            return 0.0;
        }
        return response.getFullTextAnnotation().getPages(0).getConfidence();
    }

}
