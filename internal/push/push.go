// Package push sends FCM notifications over the HTTP v1 API.
//
// Deliberately not the Firebase Admin SDK: the send is one authenticated POST,
// and golang.org/x/oauth2/google (already in the module via the Vision client)
// mints the bearer token from the same service-account JSON the SDK would read.
// The credentials file is push.credentials-file in config; empty selects a
// log-only stub, the same unconfigured-upstream convention as S3, SMS and mail.
//
// Everything here is BEST-EFFORT by design. A notification is an announcement
// about work that already succeeded — a bureau pull that ran, a PAN that got
// verified — and a push failure must never fail that work or even surface to
// its caller. Failures are logged; the one failure acted on is UNREGISTERED,
// which is FCM's only signal that the app was uninstalled (there is no
// uninstall callback anywhere), and it clears the dead token.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// tokenStore is the slice of the session repository the sender needs: who to
// notify, and how to drop a token FCM has declared dead. Narrow so the package
// can be tested without a database.
type tokenStore interface {
	LivePushTokens(ctx context.Context, accountID int64) ([]string, error)
	DropPushToken(ctx context.Context, token string) error
}

// Notification is one message. Title and Body are what the user reads; Image
// is an optional https URL shown as the notification's picture; Data travels
// invisibly to the app (e.g. a route to open on tap).
type Notification struct {
	Title string
	Body  string
	Image string
	Data  map[string]string
}

// Sender fans a notification out to every live device of an account.
type Sender struct {
	store   tokenStore
	http    *http.Client // nil = stub (no credentials configured)
	sendURL string
}

// New builds a Sender. Two credential shapes, because Google's
// secure-by-default org policy (iam.disableServiceAccountKeyCreation) often
// forbids the first:
//
//   - credentialsFile set: a service-account JSON key. The project id rides
//     inside it, so projectID may be empty.
//   - credentialsFile empty, projectID set: Application Default Credentials —
//     locally `gcloud auth application-default login`, on GCP the metadata
//     server, or GOOGLE_APPLICATION_CREDENTIALS. The ADC file carries no
//     project id (it is a user, not a project member), hence the explicit one.
//
// Both empty = stub: NotifyAccount logs what it would have sent and succeeds.
// Credentials that are asked for but unusable are an error — a deployment that
// meant to notify and cannot should fail loudly at boot, not drop messages
// quietly forever.
func New(ctx context.Context, credentialsFile, projectID string, store tokenStore) (*Sender, error) {
	switch {
	case credentialsFile != "":
		raw, err := os.ReadFile(credentialsFile)
		if err != nil {
			return nil, fmt.Errorf("push: read credentials: %w", err)
		}
		// The send URL is derived from the key's own project_id rather than
		// configured separately — one fewer key to skew.
		var sa struct {
			ProjectID string `json:"project_id"`
		}
		if err := json.Unmarshal(raw, &sa); err != nil || sa.ProjectID == "" {
			return nil, fmt.Errorf("push: credentials file carries no project_id")
		}
		creds, err := google.CredentialsFromJSON(ctx, raw, fcmScope)
		if err != nil {
			return nil, fmt.Errorf("push: parse credentials: %w", err)
		}
		return &Sender{
			store:   store,
			http:    oauth2.NewClient(ctx, creds.TokenSource),
			sendURL: "https://fcm.googleapis.com/v1/projects/" + sa.ProjectID + "/messages:send",
		}, nil

	case projectID != "":
		creds, err := google.FindDefaultCredentials(ctx, fcmScope)
		if err != nil {
			return nil, fmt.Errorf(
				"push: project-id is set but no default credentials found "+
					"(locally: gcloud auth application-default login): %w", err)
		}
		return &Sender{
			store:   store,
			http:    oauth2.NewClient(ctx, creds.TokenSource),
			sendURL: "https://fcm.googleapis.com/v1/projects/" + projectID + "/messages:send",
		}, nil

	default:
		return &Sender{store: store}, nil
	}
}

// IsStub reports whether the sender is the no-credentials stub.
func (s *Sender) IsStub() bool { return s.http == nil }

// NotifyAccount sends n to every live session token the account holds.
// Best-effort: errors are logged, never returned — see the package comment.
// The context should be request-independent (the caller's work is already
// done); callers typically pass a short background context.
func (s *Sender) NotifyAccount(ctx context.Context, accountID int64, n Notification) {
	tokens, err := s.store.LivePushTokens(ctx, accountID)
	if err != nil {
		slog.Error("push: token lookup failed", "account_id", accountID, "error", err)
		return
	}
	if len(tokens) == 0 {
		slog.Debug("push: no live tokens", "account_id", accountID)
		return
	}
	if s.IsStub() {
		slog.Info("push (stub): would send",
			"account_id", accountID, "devices", len(tokens), "title", n.Title)
		return
	}
	delivered := 0
	for _, token := range tokens {
		if s.sendOne(ctx, token, n) {
			delivered++
		}
	}
	slog.Info("push: sent", "account_id", accountID,
		"devices", len(tokens), "delivered", delivered, "title", n.Title)
}

// sendOne posts one message; reports whether FCM accepted it. An UNREGISTERED
// verdict drops the token — the only uninstall signal that exists.
func (s *Sender) sendOne(ctx context.Context, token string, n Notification) bool {
	notif := map[string]string{"title": n.Title, "body": n.Body}
	if n.Image != "" {
		notif["image"] = n.Image
	}
	payload := map[string]any{"message": map[string]any{
		"token":        token,
		"notification": notif,
	}}
	if len(n.Data) > 0 {
		payload["message"].(map[string]any)["data"] = n.Data
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Error("push: marshal failed", "error", err)
		return false
	}

	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, s.sendURL, bytes.NewReader(body))
	if err != nil {
		slog.Error("push: build request failed", "error", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		slog.Warn("push: fcm unreachable", "error", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true
	}

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	// 404/UNREGISTERED: the installation is gone (uninstall, cleared data, or a
	// rotated token). Clearing it here is the uninstall handling.
	if resp.StatusCode == http.StatusNotFound || strings.Contains(string(respBody), "UNREGISTERED") {
		slog.Info("push: token unregistered; dropping it")
		if err := s.store.DropPushToken(context.WithoutCancel(ctx), token); err != nil {
			slog.Warn("push: dead token not dropped", "error", err)
		}
		return false
	}
	slog.Warn("push: fcm rejected message", "status", resp.StatusCode, "body", string(respBody))
	return false
}
