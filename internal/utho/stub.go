package utho

import "fmt"

// stubUpload returns a deterministic fake object URL without performing any
// HTTP, so the credit-report PDF relay runs end-to-end in dev/CI without Utho
// credentials. Mirrors the stub convention used by every external-capability
// package in this service.
func (c *Client) stubUpload(bucket, path string) string {
	if bucket == "" {
		bucket = c.cfg.Bucket
	}
	if bucket == "" {
		bucket = "stub-bucket"
	}
	dc := c.cfg.DCSlug
	if dc == "" {
		dc = "stub-dc"
	}
	return fmt.Sprintf("https://%s.%s.utho.io/%s", bucket, dc, path)
}
