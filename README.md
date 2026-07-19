# credit-report-service

Go backend for a credit-report Android app. REST API, PostgreSQL, and a
multi-step user registration flow with OTP verification and PAN-card OCR.

## Stack

- Go 1.22+ (built with Go 1.26)
- [Fiber](https://gofiber.io) — web framework
- [pgx](https://github.com/jackc/pgx) + [scany](https://github.com/georgysavva/scany) — Postgres driver / row mapper, no ORM
- [golang-migrate](https://github.com/golang-migrate/migrate) — embedded migrations
- [Viper](https://github.com/spf13/viper) — config (config.yaml + env overrides)
- [bcrypt](https://pkg.go.dev/golang.org/x/crypto/bcrypt) — OTP-at-rest hashing

## Project layout

```
credit-report-service/
├── cmd/server/main.go            entry point
├── config.yaml                   default config
├── config.dev.yaml               dev profile overlay
└── internal/
    ├── apperr/                   typed errors + Fiber error handler
    ├── config/                   Viper loader + Config struct
    ├── db/                       pgxpool + embedded migrations
    │   └── migrations/           golang-migrate *.sql files
    ├── handler/                  Fiber HTTP handlers
    ├── models/                   plain row structs
    ├── ocr/                      OCR provider (Stub + Google Vision)
    ├── repository/               pgx + scany repositories
    ├── server/                   Fiber app wiring + routes
    └── service/                  business logic (registration, OTP, mail, PAN)
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
  - `DB_URL=postgres://user:pass@host:5432/db?currentSchema=credit_report`
  - `REGISTRATION_OTP_LENGTH=8`
  - `MAIL_HOST=smtp.gmail.com MAIL_USERNAME=... MAIL_PASSWORD=...`

When `mail.host` is empty, OTPs are printed to stdout (dev stub).

## Endpoints

| Method | Path                                       | Description                       |
|--------|--------------------------------------------|-----------------------------------|
| GET    | `/api/ping`                                | Liveness                          |
| GET    | `/api/credit-reports`                      | List all                          |
| GET    | `/api/credit-reports/:id`                  | Get by id                         |
| GET    | `/api/credit-reports/by-subject/:subjectId`| Get by subject id                 |
| POST   | `/api/credit-reports`                      | Create (body: `subjectId`, `score?`, `status?`) |
| DELETE | `/api/credit-reports/:id`                  | Delete                            |
| POST   | `/api/registration/otp/send`               | Stage 1: send OTP (body: `mobile`, `email`) |
| POST   | `/api/registration/otp/verify`             | Stage 1: verify OTP (body: `attemptId`, `otp`) |
| POST   | `/api/registration/pan`                    | Stage 2: submit PAN + image (multipart: `attemptId`, `panNumber`, `firstName`, `lastName`, `dateOfBirth?`, `image`) |

## Registration flow

1. `POST /api/registration/otp/send` with `{mobile, email}` — returns `attemptId`.
   OTP is emailed to `email` (or logged when SMTP host is empty).
2. `POST /api/registration/otp/verify` with `{attemptId, otp}` — flips attempt
   to `OTP_VERIFIED`. `attemptId` is the session token for step 3.
3. `POST /api/registration/pan` multipart — submits PAN number, first/last name,
   optional DOB, and the PAN-card image. OCR runs on the image; PAN must match
   exactly and name within Levenshtein distance 2. On success a `users` row is
   created and the attempt becomes `PAN_VERIFIED`.

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

- **PAN authenticity** is NOT verified against the income-tax DB — only format + OCR consistency.
- **No Spring Security / JWT equivalent** here; the `attemptId` is a weak session token.
- **No HTTP rate limiter** — add one (e.g. a middleware) before exposing publicly.
- **Stale-attempt sweep** (`STARTED` rows past their OTP TTL) is not wired to a scheduler. Add a `time.Ticker` or cron to call `RegistrationService.ExpireStale`.
