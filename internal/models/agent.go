package models

import "time"

// Agent status values.
const (
	AgentActive   = "ACTIVE"
	AgentInactive = "INACTIVE"
)

// Agent is the row model for the agents table: referral partners who help
// users register. Each agent has a unique code that users provide at signup.
type Agent struct {
	ID        int64     `json:"id"        db:"id"`
	Code      string    `json:"code"      db:"code"`
	Name      string    `json:"name"      db:"name"`
	Email     *string   `json:"email"     db:"email"`
	Phone     *string   `json:"phone"     db:"phone"`
	Status    string    `json:"status"    db:"status"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
}

// AgentWithSignupCount wraps an agent with the number of accounts that
// signed up under that agent within a given date range.
type AgentWithSignupCount struct {
	*Agent
	SignupCount int64 `json:"signupCount"`
}
