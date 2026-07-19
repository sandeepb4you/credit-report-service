package models

import "time"

// CreditReport is the row model for the credit_reports table.
type CreditReport struct {
	ID        int64      `json:"id"         db:"id"`
	SubjectID string     `json:"subjectId"  db:"subject_id"`
	Score     *int32     `json:"score"      db:"score"`
	Status    *string    `json:"status"     db:"status"`
	CreatedAt time.Time  `json:"createdAt"  db:"created_at"`
	UpdatedAt time.Time  `json:"updatedAt"  db:"updated_at"`
}
