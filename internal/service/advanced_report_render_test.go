package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"credit-report-service/internal/models"
	"credit-report-service/internal/render"
)

// TestAdvancedReport_RendersAPDF drives the real browser end to end.
//
// Opt-in, because it needs one: with no RENDERER_URL it skips, so `go test
// ./...` on a machine without Docker stays green. To run it:
//
//	docker run -d --name renderer --shm-size=1g -p 9222:9222 chromedp/headless-shell
//	RENDERER_URL=ws://127.0.0.1:9222 go test ./internal/service/ -run RendersAPDF
//
// Set ADVANCED_REPORT_OUT=/path/file.pdf to keep the output and look at it —
// which is the point of a designed document: the assertions below can say it is
// a PDF of about the right size, and only a person can say it is right.
func TestAdvancedReport_RendersAPDF(t *testing.T) {
	url := os.Getenv("RENDERER_URL")
	if url == "" {
		t.Skip("RENDERER_URL not set; skipping the browser render")
	}

	renderer := render.NewChromium(render.Config{URL: url, Timeout: 60 * time.Second})
	if !renderer.Available() {
		t.Fatal("renderer reports itself unavailable with a URL set")
	}

	svc := &CreditAnalyticsService{}
	acc := &models.Account{FirstName: strptr("Tandava"), LastName: strptr("Aradhyula")}
	html, err := svc.renderAdvancedHTML(acc, fullInsights())
	if err != nil {
		t.Fatalf("build html: %v", err)
	}

	pdf, err := renderer.PDF(context.Background(), html)
	if err != nil {
		t.Fatalf("render pdf: %v", err)
	}

	if len(pdf) < 5000 {
		t.Fatalf("pdf is %d bytes — too small to be eight pages", len(pdf))
	}
	if string(pdf[:4]) != "%PDF" {
		t.Fatalf("output is not a PDF: % x", pdf[:8])
	}
	if out := os.Getenv("ADVANCED_REPORT_OUT"); out != "" {
		if err := os.WriteFile(filepath.Clean(out), pdf, 0o600); err != nil {
			t.Fatalf("write %s: %v", out, err)
		}
		t.Logf("wrote %s (%d bytes)", out, len(pdf))
	}
}
