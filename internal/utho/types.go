// Package utho — request/response types for the Utho Cloud object-storage API.
// The client and package doc live in client.go; this file holds only the data
// shapes so they can be referenced by the client and stub without cycles.
package utho

import "time"

// Config configures the Utho object-storage client.
type Config struct {
	APIToken string        // Bearer token; empty -> stub client (no I/O)
	DCSlug   string        // Utho datacenter slug, e.g. "uk-london"
	Bucket   string        // target bucket name
	BaseURL  string        // override of the default API host (rarely needed)
	Timeout  time.Duration // per-request timeout
}

// UploadResponse is the JSON envelope Utho returns from the upload endpoint.
// The shape isn't fully documented; Utho echoes at least a status and (we
// expect) a link to the stored object. We decode leniently and fall back to a
// constructed URL when Link is empty (see client.objectURL).
type UploadResponse struct {
	Status string `json:"status"` // "success" | "error"
	// Link is the public URL of the stored object when Utho echoes it. Empty in
	// some responses; the client constructs a URL in that case.
	Link string `json:"link"`
	URL  string `json:"url"`  // alternate key some Utho endpoints use
	Path string `json:"path"` // alternate: object path only
	Msg  string `json:"msg"`  // error message on failure
}
