// Package s3store stores credit-report PDFs in Amazon S3 and hands out
// short-lived links to them.
//
// It replaces Utho as the relay destination now that the service runs on AWS:
// the EC2 instance carries an IAM role, so there is no long-lived access key in
// the environment for an attacker to find or for anyone to rotate. Utho's own
// API is S3-compatible, so going back would mean an endpoint override here
// rather than a second client.
//
// The bucket is private — public access is blocked at the bucket, and its policy
// denies any non-TLS request — so an object URL is of no use on its own. Reads
// therefore go through [Client.PresignGet], which mints a signed URL that
// expires. That is the whole reason this package exists rather than a bare
// PutObject call somewhere: a credit report must not be reachable by anyone who
// happens to learn its key.
package s3store

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Config configures the client. Credentials are deliberately absent: they come
// from the instance's IAM role via the default credential chain.
type Config struct {
	Bucket string
	Region string
	// PresignTTL bounds how long a download link stays valid. Kept short — the
	// link is handed straight to a browser that follows it immediately, so a
	// long window only widens the period in which a leaked URL still works.
	PresignTTL time.Duration
}

// Client uploads to and presigns from one bucket.
type Client struct {
	cfg      Config
	api      *s3.Client
	presign  *s3.PresignClient
	stubOnly bool
}

// New returns a client for cfg, or a stub when no bucket is configured.
//
// An empty bucket yields the stub rather than an error, matching the convention
// the rest of the service uses for unconfigured upstreams (Digitap, Cashfree,
// MSG91): a developer machine with no AWS access still boots and still serves
// every path that does not need object storage.
func New(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.PresignTTL <= 0 {
		cfg.PresignTTL = 10 * time.Minute
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return &Client{cfg: cfg, stubOnly: true}, nil
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	api := s3.NewFromConfig(awsCfg)
	return &Client{cfg: cfg, api: api, presign: s3.NewPresignClient(api)}, nil
}

// IsStub reports whether this client is the no-op stub.
func (c *Client) IsStub() bool { return c.stubOnly }

// Bucket is the configured bucket name, or "" for the stub.
func (c *Client) Bucket() string { return c.cfg.Bucket }

// Upload stores body at key and returns the object's s3:// URI.
//
// The URI, not an https URL: the bucket is private, so an https link would only
// look usable. Callers persist this and presign it at read time.
//
// There is deliberately no bucket parameter: the configured bucket is the only
// destination, because a relay a caller could aim at an arbitrary bucket is a
// way to write credit reports somewhere unaudited.
func (c *Client) Upload(ctx context.Context, key, filename string, body []byte) (string, error) {
	return c.UploadAs(ctx, key, filename, "application/pdf", body)
}

// UploadAs is [Client.Upload] with an explicit content type, for objects that
// are not report PDFs (e.g. uploaded KYC documents, which may be images).
func (c *Client) UploadAs(ctx context.Context, key, filename, contentType string, body []byte) (string, error) {
	if c.stubOnly {
		return "", fmt.Errorf("s3store: no bucket configured")
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// Content-Disposition so a presigned link downloads as a sensibly named file
	// rather than rendering inline under a numeric key.
	disposition := fmt.Sprintf("attachment; filename=%q", filename)
	_, err := c.api.PutObject(ctx, &s3.PutObjectInput{
		Bucket:             &c.cfg.Bucket,
		Key:                &key,
		Body:               bytes.NewReader(body),
		ContentType:        &contentType,
		ContentDisposition: &disposition,
	})
	if err != nil {
		return "", fmt.Errorf("s3 put %s: %w", key, err)
	}
	return "s3://" + c.cfg.Bucket + "/" + key, nil
}

// PresignGet returns a time-limited download URL for a stored object, addressed
// either by key or by the s3:// URI that [Upload] returned.
func (c *Client) PresignGet(ctx context.Context, keyOrURI string) (string, time.Duration, error) {
	if c.stubOnly {
		return "", 0, fmt.Errorf("s3store: no bucket configured")
	}
	key, err := c.keyFrom(keyOrURI)
	if err != nil {
		return "", 0, err
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &c.cfg.Bucket,
		Key:    &key,
	}, s3.WithPresignExpires(c.cfg.PresignTTL))
	if err != nil {
		return "", 0, fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, c.cfg.PresignTTL, nil
}

// Download fetches a stored object's bytes, for emailing it as an attachment.
func (c *Client) Download(ctx context.Context, keyOrURI string) ([]byte, error) {
	if c.stubOnly {
		return nil, fmt.Errorf("s3store: no bucket configured")
	}
	key, err := c.keyFrom(keyOrURI)
	if err != nil {
		return nil, err
	}
	out, err := c.api.GetObject(ctx, &s3.GetObjectInput{Bucket: &c.cfg.Bucket, Key: &key})
	if err != nil {
		return nil, fmt.Errorf("s3 get %s: %w", key, err)
	}
	defer out.Body.Close()
	buf := &bytes.Buffer{}
	if _, err := buf.ReadFrom(out.Body); err != nil {
		return nil, fmt.Errorf("s3 read %s: %w", key, err)
	}
	return buf.Bytes(), nil
}

// Delete removes a stored object.
//
// Used when the report it belongs to is deleted: the file is encrypted, but an
// encrypted report nobody can reach any more is still somebody's credit file
// sitting in a bucket, and versioning is on, so a delete leaves a marker rather
// than shredding history.
//
// S3 answers a delete of a key that is not there with success, and that is the
// behaviour wanted here — a reset re-run after a partial failure should finish
// quietly rather than report a problem that no longer exists.
func (c *Client) Delete(ctx context.Context, keyOrURI string) error {
	if c.stubOnly {
		return fmt.Errorf("s3store: no bucket configured")
	}
	key, err := c.keyFrom(keyOrURI)
	if err != nil {
		return err
	}
	if _, err := c.api.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &c.cfg.Bucket, Key: &key,
	}); err != nil {
		return fmt.Errorf("s3 delete %s: %w", key, err)
	}
	return nil
}

// keyFrom accepts a bare key or an s3://bucket/key URI and returns the key.
//
// A URI naming a different bucket is refused rather than silently read from the
// configured one: the stored value is the record of where an object actually
// went, and quietly reinterpreting it would turn a misconfiguration into a
// wrong-object read.
func (c *Client) keyFrom(keyOrURI string) (string, error) {
	v := strings.TrimSpace(keyOrURI)
	if v == "" {
		return "", fmt.Errorf("s3store: empty key")
	}
	if !strings.HasPrefix(v, "s3://") {
		return strings.TrimPrefix(v, "/"), nil
	}
	rest := strings.TrimPrefix(v, "s3://")
	bucket, key, ok := strings.Cut(rest, "/")
	if !ok || key == "" {
		return "", fmt.Errorf("s3store: malformed uri %q", keyOrURI)
	}
	if bucket != c.cfg.Bucket {
		return "", fmt.Errorf("s3store: uri names bucket %q, configured bucket is %q", bucket, c.cfg.Bucket)
	}
	return key, nil
}
