package models

import "time"

// User is a confirmed KYC-accepted user.
type User struct {
	ID           int64      `json:"id"            db:"id"`
	Mobile       string     `json:"mobile"        db:"mobile"`
	Email        string     `json:"email"         db:"email"`
	PANNumber    string     `json:"panNumber"     db:"pan_number"`
	FirstName    string     `json:"firstName"     db:"first_name"`
	LastName     string     `json:"lastName"      db:"last_name"`
	DateOfBirth  *time.Time `json:"dateOfBirth"   db:"date_of_birth"`
	PANImagePath *string    `json:"panImagePath"  db:"pan_image_path"`
	Status       string     `json:"status"        db:"status"`
	CreatedAt    time.Time  `json:"createdAt"     db:"created_at"`
	UpdatedAt    time.Time  `json:"updatedAt"     db:"updated_at"`
}
