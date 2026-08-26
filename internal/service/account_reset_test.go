package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"credit-report-service/internal/apperr"
	"credit-report-service/internal/models"
	"credit-report-service/internal/repository"
)

// fakeResetStore stands in for AccountRepo. The reset itself is one SQL
// transaction and is not what these tests are about: what matters here is that
// nothing is deleted unless the confirmation names the very account being
// reset, and that the stored PDFs go with the rows.
type fakeResetStore struct {
	byID     map[int64]*models.Account
	byEmail  map[string]*models.Account
	byPhone  map[string]*models.Account
	pdfURIs  []string
	resets   []int64
	resetErr error
}

func (f *fakeResetStore) FindByID(_ context.Context, id int64) (*models.Account, error) {
	if a, ok := f.byID[id]; ok {
		return a, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeResetStore) FindByEmail(_ context.Context, email string) (*models.Account, error) {
	if a, ok := f.byEmail[email]; ok {
		return a, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeResetStore) FindByPhone(_ context.Context, phone string) (*models.Account, error) {
	if a, ok := f.byPhone[phone]; ok {
		return a, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeResetStore) SignupResetPreview(
	_ context.Context, _ int64,
) (*models.AccountResetCounts, error) {
	return &models.AccountResetCounts{Reports: 3, Orders: 1, PaidOrders: 1}, nil
}

func (f *fakeResetStore) ResetToSignup(
	_ context.Context, accountID int64,
) (*models.AccountResetResult, error) {
	if f.resetErr != nil {
		return nil, f.resetErr
	}
	f.resets = append(f.resets, accountID)
	return &models.AccountResetResult{
		AccountID:     accountID,
		Removed:       models.AccountResetCounts{Reports: 3, Orders: 1, PaidOrders: 1},
		PDFObjectURIs: f.pdfURIs,
		TokenEpoch:    7,
	}, nil
}

// fakeRemover records deletions and can fail on demand.
type fakeRemover struct {
	deleted []string
	stub    bool
	failAll bool
}

func (f *fakeRemover) Delete(_ context.Context, keyOrURI string) error {
	if f.failAll {
		return fmt.Errorf("bucket unavailable")
	}
	f.deleted = append(f.deleted, keyOrURI)
	return nil
}

func (f *fakeRemover) IsStub() bool { return f.stub }

func str(s string) *string { return &s }

func storeWithAccount() *fakeResetStore {
	acc := &models.Account{
		ID:           9,
		Role:         models.RoleUser,
		PrimaryPhone: str("+917908096603"),
	}
	withEmail := &models.Account{
		ID:           2,
		Role:         models.RoleAdmin,
		PrimaryEmail: str("Admin@Example.COM"),
		PrimaryPhone: str("+919916314203"),
	}
	return &fakeResetStore{
		byID:    map[int64]*models.Account{9: acc, 2: withEmail},
		byPhone: map[string]*models.Account{"+917908096603": acc, "+919916314203": withEmail},
		byEmail: map[string]*models.Account{"admin@example.com": withEmail},
		pdfURIs: []string{
			"s3://myscorr-credit-reports/credit-reports/9/23.pdf",
			"s3://myscorr-credit-reports/credit-reports/9/16.pdf",
		},
	}
}

func TestReset_RequiresTheAccountsOwnContactDetail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		confirm string
	}{
		{"empty", ""},
		{"another account's number", "+919916314203"},
		{"the right number, one digit out", "+917908096604"},
		{"not a phone or an email at all", "yes"},
		{"an email on an account that has none", "shubhra@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithAccount()
			svc := NewAccountResetService(store)

			_, err := svc.Reset(context.Background(), 2, 9, tc.confirm)
			if err == nil {
				t.Fatal("expected the reset to be refused")
			}
			if len(store.resets) != 0 {
				t.Errorf("account was reset anyway: %v", store.resets)
			}
			var v *apperr.Validation
			if !errors.As(err, &v) {
				t.Errorf("want a validation error the UI can show on the field, got %T", err)
			}
		})
	}
}

func TestReset_AcceptsTheRegisteredPhoneHoweverItIsTyped(t *testing.T) {
	// The same shapes sign-in accepts, and no others: the confirmation runs
	// through normalizePhone, so a leading STD zero is refused here exactly as
	// it is on the login screen rather than being quietly special-cased.
	for _, confirm := range []string{
		"+917908096603", "917908096603", "7908096603", "+91 79080 96603", "79080 96603",
	} {
		t.Run(confirm, func(t *testing.T) {
			store := storeWithAccount()
			svc := NewAccountResetService(store)

			res, err := svc.Reset(context.Background(), 2, 9, confirm)
			if err != nil {
				t.Fatalf("reset refused for %q: %v", confirm, err)
			}
			if len(store.resets) != 1 || store.resets[0] != 9 {
				t.Errorf("reset %v, want [9]", store.resets)
			}
			// The epoch is what actually ends the target's session.
			if res.TokenEpoch == 0 {
				t.Error("no token epoch returned; live access tokens would survive the reset")
			}
		})
	}
}

// An email signup confirms with its address, case and spacing notwithstanding.
func TestReset_AcceptsTheRegisteredEmail(t *testing.T) {
	store := storeWithAccount()
	svc := NewAccountResetService(store)

	if _, err := svc.Reset(context.Background(), 2, 2, "  admin@example.com "); err != nil {
		t.Fatalf("reset refused: %v", err)
	}
	if len(store.resets) != 1 {
		t.Errorf("resets = %v, want one", store.resets)
	}
}

// Resetting your own account is allowed — it is the most likely use, since the
// admin's own number is the one they test signup with. The role is not touched
// by the reset, so they are still an admin when they sign back in.
func TestReset_AllowsSelfAndLeavesTheRoleAlone(t *testing.T) {
	store := storeWithAccount()
	svc := NewAccountResetService(store)

	if _, err := svc.Reset(context.Background(), 2, 2, "+919916314203"); err != nil {
		t.Fatalf("an admin must be able to reset their own account: %v", err)
	}
	if got := store.byID[2].Role; got != models.RoleAdmin {
		t.Errorf("role = %q, want it untouched (%q)", got, models.RoleAdmin)
	}
}

func TestReset_DeletesTheStoredReportPDFs(t *testing.T) {
	store := storeWithAccount()
	remover := &fakeRemover{}
	svc := NewAccountResetService(store)
	svc.SetPDFStore(remover)

	if _, err := svc.Reset(context.Background(), 2, 9, "+917908096603"); err != nil {
		t.Fatal(err)
	}
	if len(remover.deleted) != 2 {
		t.Fatalf("deleted %v, want both report objects", remover.deleted)
	}
}

// The bucket failing must not undo a reset that has already committed: the
// account is back at signup either way, and the leftover file is logged.
func TestReset_SurvivesAFailingObjectStore(t *testing.T) {
	for _, tc := range []struct {
		name    string
		remover *fakeRemover
		wire    bool
	}{
		{"delete fails", &fakeRemover{failAll: true}, true},
		{"store is a stub", &fakeRemover{stub: true}, true},
		{"no store wired at all", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storeWithAccount()
			svc := NewAccountResetService(store)
			if tc.wire {
				svc.SetPDFStore(tc.remover)
			}
			if _, err := svc.Reset(context.Background(), 2, 9, "+917908096603"); err != nil {
				t.Fatalf("reset should have succeeded: %v", err)
			}
			if len(store.resets) != 1 {
				t.Errorf("resets = %v, want one", store.resets)
			}
		})
	}
}

func TestReset_UnknownAccountIsNotFound(t *testing.T) {
	store := storeWithAccount()
	svc := NewAccountResetService(store)

	_, err := svc.Reset(context.Background(), 2, 404, "+917908096603")
	var nf *apperr.NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("want a 404, got %v (%T)", err, err)
	}
	if len(store.resets) != 0 {
		t.Errorf("something was reset: %v", store.resets)
	}
}

// A failing wipe is a failure, not a half-reset reported as success: the
// transaction rolls back, so the account is untouched and the caller must see
// an error rather than an empty receipt.
func TestReset_ReportsAFailedWipe(t *testing.T) {
	store := storeWithAccount()
	store.resetErr = fmt.Errorf("deadlock detected")
	svc := NewAccountResetService(store)

	if _, err := svc.Reset(context.Background(), 2, 9, "+917908096603"); err == nil {
		t.Fatal("expected the error to surface")
	}
}

func TestLookup_ResolvesPhoneOrEmailAndPreviewsWhatGoes(t *testing.T) {
	for _, tc := range []struct {
		identifier string
		wantID     int64
	}{
		{"7908096603", 9},
		{"+91 79080 96603", 9},
		{"ADMIN@example.com", 2},
	} {
		t.Run(tc.identifier, func(t *testing.T) {
			svc := NewAccountResetService(storeWithAccount())
			got, err := svc.Lookup(context.Background(), tc.identifier)
			if err != nil {
				t.Fatal(err)
			}
			if got.Account.ID != tc.wantID {
				t.Errorf("account %d, want %d", got.Account.ID, tc.wantID)
			}
			// The counts are the warning the admin reads before confirming.
			if got.Counts == nil || got.Counts.PaidOrders != 1 {
				t.Errorf("counts = %+v, want the paid order surfaced", got.Counts)
			}
		})
	}
}

func TestLookup_RejectsWhatItCannotResolve(t *testing.T) {
	for _, tc := range []struct{ name, identifier string }{
		{"empty", "  "},
		{"not a mobile number", "12345"},
		{"unknown number", "9999999999"},
		{"unknown email", "nobody@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewAccountResetService(storeWithAccount())
			if _, err := svc.Lookup(context.Background(), tc.identifier); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
