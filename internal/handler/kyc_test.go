package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestGetKYCStatus_Unauthenticated(t *testing.T) {
	h := NewKycHandler(nil)
	app := newApp()
	app.Get("/api/kyc/status", h.GetStatus)

	req := httptest.NewRequest("GET", "/api/kyc/status", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestListPendingKYC_Unauthenticated(t *testing.T) {
	h := NewKycHandler(nil)
	app := newApp()
	app.Get("/api/admin/kyc/pending", h.ListPending)

	req := httptest.NewRequest("GET", "/api/admin/kyc/pending", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// A non-numeric limit/offset must fail loudly rather than fall back to the
// default page — a typo'd query would otherwise serve rows the caller did not
// ask for. The nil service is never reached, since parsing fails first.
func TestListPendingKYC_BadPaging(t *testing.T) {
	cases := []string{"?limit=fifty", "?offset=abc", "?limit=10&offset=1.5"}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			h := NewKycHandler(nil)
			app := newApp()
			app.Use(func(c *fiber.Ctx) error {
				c.Locals("accountID", int64(1))
				return c.Next()
			})
			app.Get("/api/admin/kyc/pending", h.ListPending)

			req := httptest.NewRequest("GET", "/api/admin/kyc/pending"+q, nil)
			resp, err := app.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 400 {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}
