// Package render turns HTML into PDF with a headless Chromium.
//
// A browser rather than a PDF library because the document is a designed one —
// eight A4 pages of gradients, arcs, a payment-history grid and web fonts — and
// re-expressing that in drawing primitives would mean maintaining the design
// twice, in two languages, with no way to look at it but to generate a file.
// The template is HTML, so it can be opened in a browser while it is being
// written, and what ships is what was seen.
//
// The browser is a SIDECAR, not a library inside this process. `apk add
// chromium` takes the API image from 17 MB to 1.16 GB (measured), and a render
// peaks at a few hundred MB of RAM — inside the API container that spike sits
// next to request handling, and a browser that wedges takes the API down with
// it. As a separate container it can be restarted, memory-capped and upgraded
// on its own, and this package is a thin DevTools client either way.
package render

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Renderer prints HTML documents to PDF.
//
// An interface because everything above it — the report template, the service
// that stores the output — must be testable without a browser on the machine.
type Renderer interface {
	// PDF renders a complete HTML document and returns the PDF bytes.
	PDF(ctx context.Context, html string) ([]byte, error)
	// Available reports whether a renderer is actually configured. False means
	// callers should answer "not available" rather than fail.
	Available() bool
}

// Chromium renders through a remote headless Chromium over the DevTools
// protocol (chromedp/headless-shell in docker-compose).
type Chromium struct {
	// url is the DevTools websocket endpoint, e.g. ws://renderer:9222.
	url string
	// timeout bounds one render end to end. A page that never settles must not
	// hold a request open: the caller is a user waiting on a download.
	timeout time.Duration
}

// Config is what NewChromium needs. An empty URL selects the stub.
type Config struct {
	URL     string
	Timeout time.Duration
}

const defaultRenderTimeout = 45 * time.Second

// NewChromium returns a renderer for cfg.URL, or a stub when it is empty.
//
// Empty is the ordinary state of a developer machine, and it must not be a boot
// failure: every other optional upstream here (S3, SMS, mail) degrades the same
// way, so a laptop runs the whole service and the one feature that needs a
// browser reports itself unavailable.
func NewChromium(cfg Config) Renderer {
	if cfg.URL == "" {
		return Unavailable{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRenderTimeout
	}
	return &Chromium{url: cfg.URL, timeout: timeout}
}

func (c *Chromium) Available() bool { return true }

// PDF loads html into a fresh tab and prints it to A4 with no margins — the
// page geometry lives in the document's own CSS (@page), which is the only
// place a designer can see it.
func (c *Chromium) PDF(ctx context.Context, html string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// One allocator per render: a shared browser context that outlives a request
	// accumulates tabs when a render is cancelled halfway.
	allocCtx, cancelAlloc := chromedp.NewRemoteAllocator(ctx, c.url)
	defer cancelAlloc()
	tabCtx, cancelTab := chromedp.NewContext(allocCtx)
	defer cancelTab()

	var pdf []byte
	err := chromedp.Run(tabCtx,
		// about:blank first: SetDocumentContent needs a frame to write into.
		chromedp.Navigate("about:blank"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			frameTree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return fmt.Errorf("get frame tree: %w", err)
			}
			return page.SetDocumentContent(frameTree.Frame.ID, html).Do(ctx)
		}),
		// The document is self-contained (inline CSS, no network fonts), so
		// there is nothing to wait for beyond layout.
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			pdf, _, err = page.PrintToPDF().
				WithPrintBackground(true).
				WithPreferCSSPageSize(true).
				Do(ctx)
			return err
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("render pdf: %w", err)
	}
	return pdf, nil
}

// Unavailable is the no-browser renderer: it refuses rather than pretending.
type Unavailable struct{}

func (Unavailable) Available() bool { return false }

func (Unavailable) PDF(context.Context, string) ([]byte, error) {
	return nil, fmt.Errorf("no pdf renderer configured")
}
