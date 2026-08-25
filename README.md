# credit-report-service

Go backend for a credit-report Android app. REST API, PostgreSQL, and a
multi-step user registration flow with OTP verification and PAN-card OCR.

## Stack

- Go 1.22+ (built with Go 1.26)
- [Fiber](https://gofiber.io) — web framework
- [pgx](https://github.com/jackc/pgx) + [scany](https://github.com/georgysavva/scany) — Postgres driver / row mapper, no ORM
- [golang-migrate](https://github.com/golang-migrate/migrate) — embedded migrations
- [Viper](https://github.com/spf13/viper) — config (config.yaml + env overrides)
- [bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt) — password + OTP-at-rest hashing
- [JWT](https://github.com/golang-jwt/jwt) — HS256 session tokens
- [swaggo/swag](https://github.com/swaggo/swag) + [gofiber/swagger](https://github.com/gofiber/swagger) — OpenAPI docs & Swagger UI

## Project layout

```
credit-report-service/
├── cmd/server/main.go            entry point (general API info for swag)
├── config.yaml                   default config
├── config.dev.yaml               dev profile overlay
├── docs/                         generated OpenAPI spec (docs.go, swagger.json/yaml)
└── internal/
    ├── apperr/                   typed errors + Fiber error handler
    ├── config/                   Viper loader + Config struct
    ├── db/                       pgxpool + embedded migrations
    │   └── migrations/           golang-migrate *.sql files
    ├── handler/                  Fiber HTTP handlers (swag annotations)
    ├── models/                   plain row structs
    ├── ocr/                      OCR provider (Stub + Google Vision)
    ├── repository/               pgx + scany repositories
    ├── server/                   Fiber app wiring + routes
    └── service/                  business logic (auth, OTP, mail, credit reports)
```

## Run

```bash
# 1. Postgres (or point at an existing one via DB_URL)
docker run -d --name credit-db \
  -e POSTGRES_DB=credit_report -e POSTGRES_USER=serpapp -e POSTGRES_PASSWORD=serp1234 \
  -p 5432:5432 postgres:16

# 2. Run (migrations run at startup)
go run ./cmd/server
```

The server listens on `:8080` by default. Override with the `PORT` env var.

## Configuration

Defaults live in `config.yaml`. Override:

- **Profile overlay** — set `APP_PROFILE=dev` to merge `config.dev.yaml` on top.
- **Env vars** — uppercased, dot/dash to underscore. Examples:
  - `DB_URL=postgres://user:pass@host:5432/db?search_path=credit_report`
  - `REGISTRATION_OTP_LENGTH=8`
  - `MAIL_HOST=smtp.gmail.com MAIL_USERNAME=... MAIL_PASSWORD=...`

When `mail.host` is empty, OTPs are printed to stdout (dev stub).

## API docs (Swagger)

Interactive docs are served by Swagger UI once the app is running:

```
http://localhost:8080/swagger/index.html
```

Protected endpoints expose an **Authorize** button — paste a JWT obtained from
`POST /api/auth/login` (or `/api/auth/verify-email`) and it will be sent as
`Authorization: Bearer <jwt>`.

The spec is generated from `// @...` annotations on each handler via
[swaggo/swag](https://github.com/swaggo/swag). Regenerate after editing
annotations:

```bash
go install github.com/swaggo/swag/cmd/swag@latest
swag init -g cmd/server/main.go -o docs --parseDependency --parseInternal
```

The generated `docs/` package is committed; `cmd/server/main.go` blank-imports it
and `internal/server/server.go` mounts the UI at `/swagger/*`.

## Endpoints

All routes are under `/api`. 🔒 = requires `Authorization: Bearer <jwt>`.

| Method | Path                                       | Auth     | Description                                  |
|--------|--------------------------------------------|----------|----------------------------------------------|
| GET    | `/api/ping`                                |          | Liveness                                     |
| POST   | `/api/auth/signup`                         |          | Create account, email verification OTP       |
| POST   | `/api/auth/verify-email`                   |          | Verify OTP, activate account, return JWT     |
| POST   | `/api/auth/otp/resend`                     |          | Resend signup OTP                            |
| POST   | `/api/auth/login`                          |          | Email + password login, return JWT           |
| POST   | `/api/auth/phone/send`                     | 🔒       | Register a mobile number: send an SMS OTP    |
| POST   | `/api/auth/phone/verify`                   | 🔒       | Verify it, attach the number to the account  |
| POST   | `/api/auth/email/send`                     | 🔒       | Link an email address: send an OTP           |
| POST   | `/api/auth/email/verify`                   | 🔒       | Verify it, attach the address to the account |
| POST   | `/api/auth/password/forgot`                |          | Email a password-reset OTP                   |
| POST   | `/api/auth/password/verify-otp`            |          | Reset OTP → single-use `resetToken`          |
| POST   | `/api/auth/password/reset`                 |          | Set a new password, sign out every device    |
| GET    | `/api/profile`                             | 🔒       | Get current account                          |
| PUT    | `/api/profile`                             | 🔒       | Update first/last name, DOB                  |
| GET    | `/api/credit-reports`                      | 🔒       | List all                                     |
| GET    | `/api/credit-reports/:id`                  | 🔒       | Get by id                                    |
| GET    | `/api/credit-reports/by-subject/:subjectId`| 🔒       | Get by subject id                            |
| POST   | `/api/credit-reports`                      | 🔒       | Create (`subjectId`, `score?`, `status?`)    |
| DELETE | `/api/credit-reports/:id`                  | 🔒       | Delete                                       |

## Auth flow

1. `POST /api/auth/signup` with `{email, password}` — creates a `PENDING` account
   and emails a verification OTP (logged to stdout when SMTP host is empty).
2. `POST /api/auth/verify-email` with `{email, otp}` — verifies the identity,
   activates the account, and returns `{token, expiresAt, account}`.
3. Use the `token` as `Authorization: Bearer <token>` for protected routes.
   `POST /api/auth/login` re-issues a token for a verified account.
4. `POST /api/auth/phone/send` then `/api/auth/phone/verify`, both with that
   bearer token — registers a mobile number onto the account. Mandatory for an
   email signup: PAN verification checks the PAN against the account's number
   (`internal/service/pan_prefill.go`), so an account without one has no route
   through KYC. These are **not** `/api/auth/otp/phone/*`, which are public,
   find-or-create, and sign a number *in* — sending a signed-in user down that
   path would switch them into whichever account already owns the number.
   See `internal/service/phone_register.go`.
5. `POST /api/auth/email/send` then `/api/auth/email/verify` — the mirror, for an
   account that signed up by phone. Optional: nothing is blocked without an email.
   Nothing is written until the code passes. The identity row is created with a
   NULL password hash, so `login` still rejects the address while
   `password/forgot` accepts it — the route by which a phone-first user gives
   themselves a password. See `internal/service/email_link.go`; the challenge
   handling both flows share is in `internal/service/link_identity.go`.

## Forgot password

1. `POST /api/auth/password/forgot` with `{email}` — emails a reset OTP. It answers
   `200` even when no account exists, so the endpoint cannot be used to discover which
   addresses are registered; client copy therefore has to be conditional ("if that
   email has an account…"). Call it again to resend, subject to the same
   `auth.otp.resend-cooldown` (30s) and `max-sends` as signup.
2. `POST /api/auth/password/verify-otp` with `{email, otp}` — consumes the code and
   returns `{resetToken, expiresAt}`: a single-use, 15-minute grant. It exists so the
   client can move to a "choose a password" screen without holding a live OTP for as
   long as the user takes to type one.
3. `POST /api/auth/password/reset` with `{email, resetToken, password}` — writes the
   new password, burns the grant, and revokes **every** session (`revoked_reason =
   'password_reset'`). The user signs in again; already-issued access tokens keep
   working until they expire, as everywhere else in this service.

Only verified email+password identities can reset. A Google-only account has no
password to reset and an unverified signup is finished with the signup OTP instead —
both get the same non-committal `200`.

## OCR providers

- **stub** (default) — deterministic mock returning PAN `ABCDE1234F`, name `SAMPLE USER`. Lets the flow run without cloud credentials.
- **google-vision** — Google Cloud Vision `DOCUMENT_TEXT_DETECTION`. Build with the `googlevision` build tag and set `GOOGLE_APPLICATION_CREDENTIALS`:

  ```bash
  go build -tags googlevision -o bin/server ./cmd/server
  set registration.ocr.provider=google-vision  # via config or env REGISTRATION_OCR_PROVIDER=google-vision
  ```

## Build & test

```bash
go build ./...
go vet ./...
go test ./...
```

## Production notes

- **JWT secret** — override `auth.jwt-secret` (env `AUTH_JWT_SECRET`) before exposing publicly; the default is insecure.
- **PAN authenticity** is NOT verified against the income-tax DB — only format + OCR consistency.
- **No HTTP rate limiter** — add one (e.g. a middleware) before exposing publicly.
- **Swagger UI** is mounted publicly at `/swagger/*`; restrict it (or put it behind auth) in production if you don't want the spec exposed.
