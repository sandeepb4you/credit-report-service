//go:build googlevision

// GoogleVision is an OCR provider backed by Google Cloud Vision. It is gated
// behind the "googlevision" build tag so the binary doesn't require the
// google-cloud-go SDK unless explicitly built with:
//
//	go build -tags googlevision ./...
//
// Authentication uses standard Google credentials: set
// GOOGLE_APPLICATION_CREDENTIALS to a service-account JSON path.
package ocr

import (
	"context"
	"fmt"

	vision "cloud.google.com/go/vision/apiv1"
	visionpb "cloud.google.com/go/vision/v2/apiv1/visionpb"
)

type GoogleVision struct {
	client *vision.ImageAnnotatorClient
}

func NewGoogleVision(ctx context.Context) (*GoogleVision, error) {
	c, err := vision.NewImageAnnotatorClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("create vision client: %w", err)
	}
	return &GoogleVision{client: c}, nil
}

func (g *GoogleVision) Close() error { return g.client.Close() }

func (g *GoogleVision) Extract(imageBytes []byte, contentType string) (*Result, error) {
	img := &visionpb.Image{Content: imageBytes}
	feat := &visionpb.Feature{
		Type:       visionpb.Feature_DOCUMENT_TEXT_DETECTION,
		MaxResults: 1,
	}
	req := &visionpb.AnnotateImageRequest{
		Image:    img,
		Features: []*visionpb.Feature{feat},
	}
	resp, err := g.client.BatchAnnotateImages(context.Background(),
		&visionpb.BatchAnnotateImagesRequest{Requests: []*visionpb.AnnotateImageRequest{req}})
	if err != nil {
		return nil, fmt.Errorf("vision batch annotate: %w", err)
	}
	if len(resp.Responses) == 0 {
		return &Result{}, nil
	}
	r := resp.Responses[0]
	if r.Error != nil {
		return nil, fmt.Errorf("vision: %s", r.Error.Message)
	}
	if r.FullTextAnnotation == nil {
		return &Result{}, nil
	}
	text := r.FullTextAnnotation.GetText()
	conf := 0.0
	if len(r.FullTextAnnotation.Pages) > 0 {
		conf = float64(r.FullTextAnnotation.Pages[0].Confidence)
	}
	return &Result{
		Text:       text,
		PANNumber:  ExtractPAN(text),
		Name:       ExtractName(text),
		Confidence: conf,
	}, nil
}

