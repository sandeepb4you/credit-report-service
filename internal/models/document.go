package models

import "time"

// Document kinds the service writes today. The column is free text so a new
// kind is a constant here, not a migration.
const (
	DocTypePAN = "PAN"
)

// Document is one stored user document — one row per upload. An account may
// hold any number of them, including several of the same kind; which one is
// "current" for a purpose is that feature's own reference
// (kyc_records.document_id for the PAN card), and superseded rows remain as
// history until the retention policy says otherwise. The bytes live in the
// private S3 bucket; S3URI is deliberately not serialized — readers go
// through a presigned link minted behind whatever permission the document
// warrants (kyc:verify for a PAN card).
type Document struct {
	ID        int64     `json:"id"        db:"id"`
	AccountID int64     `json:"accountId" db:"account_id"`
	DocType   string    `json:"docType"   db:"doc_type"`
	S3URI     string    `json:"-"         db:"s3_uri"`
	FileName  string    `json:"fileName"  db:"filename"`
	MimeType  string    `json:"mimeType"  db:"mime_type"`
	SizeBytes *int64    `json:"sizeBytes,omitempty" db:"size_bytes"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}
