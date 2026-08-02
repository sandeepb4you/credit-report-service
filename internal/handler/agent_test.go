package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"

	"credit-report-service/internal/apperr"
)

// ---- Agent handler validation tests --------------------------------------
// Following the same pattern as auth_test.go: handlers are created with nil
// service deps; we only exercise input validation (failure paths), not
// business logic (which would panic on nil services).

func newAgentApp() *fiber.App {
	return fiber.New(fiber.Config{ErrorHandler: apperr.ErrorHandler})
}

func TestCreateAgent_Validation(t *testing.T) {
	h := NewAgentHandler(nil, nil)
	app := newAgentApp()
	app.Post("/api/admin/agents", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		c.Locals("accountRole", "admin")
		return h.CreateAgent(c)
	})

	tests := []struct {
		name       string
		body       map[string]string
		wantStatus int
	}{
		{"missing code and name", map[string]string{}, 400},
		{"missing code", map[string]string{"name": "Jane"}, 400},
		{"missing name", map[string]string{"code": "AGENT001"}, 400},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("POST", "/api/admin/agents", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status %d, want %d; body: %s", resp.StatusCode, tc.wantStatus, string(body))
			}
		})
	}

	t.Run("invalid JSON body", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/admin/agents", bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
}

func TestUpdateAgent_Validation(t *testing.T) {
	h := NewAgentHandler(nil, nil)
	app := newAgentApp()
	app.Put("/api/admin/agents/:id", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		c.Locals("accountRole", "admin")
		return h.UpdateAgent(c)
	})

	t.Run("missing name", func(t *testing.T) {
		b, _ := json.Marshal(map[string]string{"email": "test@test.com"})
		req := httptest.NewRequest("PUT", "/api/admin/agents/1", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/admin/agents/1", bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
}

func TestSetAgentStatus_Validation(t *testing.T) {
	h := NewAgentHandler(nil, nil)
	app := newAgentApp()
	app.Patch("/api/admin/agents/:id/status", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		c.Locals("accountRole", "admin")
		return h.SetAgentStatus(c)
	})

	tests := []struct {
		name       string
		body       map[string]string
		wantStatus int
	}{
		{"empty status", map[string]string{"status": ""}, 400},
		{"invalid status", map[string]string{"status": "PENDING"}, 400},
		{"random status", map[string]string{"status": "DELETED"}, 400},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("PATCH", "/api/admin/agents/1/status", bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status %d, want %d; body: %s", resp.StatusCode, tc.wantStatus, string(body))
			}
		})
	}

	t.Run("invalid JSON body", func(t *testing.T) {
		req := httptest.NewRequest("PATCH", "/api/admin/agents/1/status", bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
}

func TestUpdateAgentCode_Validation(t *testing.T) {
	h := NewAgentHandler(nil, nil)
	app := newAgentApp()
	app.Put("/api/profile/agent-code", func(c *fiber.Ctx) error {
		c.Locals("accountID", int64(1))
		c.Locals("accountRole", "user")
		return h.UpdateAgentCode(c)
	})

	t.Run("empty agentCode", func(t *testing.T) {
		b, _ := json.Marshal(map[string]string{"agentCode": ""})
		req := httptest.NewRequest("PUT", "/api/profile/agent-code", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})

	t.Run("missing body", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/profile/agent-code", nil)
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
}

func TestSignupWithAgentCode_Validation(t *testing.T) {
	h := NewAuthHandler(nil)
	app := newAgentApp()
	app.Post("/api/auth/signup", h.Signup)

	t.Run("invalid email with agent code", func(t *testing.T) {
		b, _ := json.Marshal(map[string]string{
			"email":     "not-an-email",
			"password":  "password123",
			"agentCode": "AGENT001",
		})
		req := httptest.NewRequest("POST", "/api/auth/signup", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})

	t.Run("invalid JSON body with agent code", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/auth/signup", bytes.NewReader([]byte("not json")))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.StatusCode != 400 {
			t.Fatalf("status %d, want 400", resp.StatusCode)
		}
	})
}
