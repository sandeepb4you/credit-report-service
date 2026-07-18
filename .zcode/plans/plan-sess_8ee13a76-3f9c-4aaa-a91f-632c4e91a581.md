## Multi-step User Registration Flow

### Design decision (flagging explicitly)
Your original message says "user enters mobile … verify mobile by sending OTP," but you chose **Email OTP** (no SMS gateway). OTP cannot verify a phone number it isn't sent to. My reconciliation: **Stage 1 collects both `mobile` and `email`, and OTP is sent to the `email`** (verifying ownership of the email). The mobile is stored as provided but not independently verified. If you'd rather collect mobile-only and gate on email-from-account, or add real SMS later, say so and I'll adjust.

### Flow & endpoints (`/api/registration`)
1. **`POST /api/registration/otp/send`** — body `{ mobile, email }`. Validates formats, creates/refreshes a `registration_attempts` row, generates a 6-digit OTP, hashes it (BCrypt), sends via email. Enforces resend cooldown + max-send count. Returns `{ attemptId, expiresAt }`.
2. **`POST /api/registration/otp/verify`** — body `{ attemptId, otp }`. Checks status, expiry, attempt count; on match marks `OTP_VERIFIED` and returns `{ attemptId, status }` (attemptId acts as the session token for step 3).
3. **`POST /api/registration/pan`** — `multipart/form-data`: `attemptId`, `panNumber`, `fullName`, `dateOfBirth`, `image` (the PAN photo). Validates PAN regex, stores image to disk, runs OCR, compares OCR PAN (exact) and OCR name (fuzzy) against the submitted values. On success → creates `users` row, marks `PAN_VERIFIED`, returns `{ userId, status }`. On mismatch → `PanValidationException` (422) and the attempt stays retryable.

### Data model — new Flyway migration `V2__registration.sql`
Two tables (schema `credit_report`):
- **`users`** — `id`, `mobile` (unique), `email` (unique), `pan_number` (unique), `full_name`, `date_of_birth`, `pan_image_path`, `status`, `created_at`, `updated_at`. Indexes on mobile/email/pan.
- **`registration_attempts`** — `id`, `mobile`, `email`, `status`, `otp_hash`, `otp_expires_at`, `otp_attempts`, `otp_send_count`, `last_otp_sent_at`, `pan_number`, `pan_name`, `date_of_birth`, `pan_image_path`, `ocr_pan_number`, `ocr_pan_name`, `user_id` (nullable FK), `created_at`, `updated_at`.

### New packages & files (following existing layer-based convention)
- **controller** — `RegistrationController`
- **service** — `RegistrationService` (orchestrates flow), `OtpService` (gen + BCrypt-verify + rate checks), `MailService` (wraps `JavaMailSender`), `PanValidator` (regex + OCR comparison with Levenshtein fuzzy name match)
- **service/ocr** — `OcrClient` interface, `OcrResult` record, `GoogleVisionOcrClient` (real), `StubOcrClient` (dev). Bean selected by `app.registration.ocr.provider`.
- **repository** — `RegistrationAttemptRepository`, `UserRepository`
- **model** — `User`, `RegistrationAttempt`, `RegistrationStatus` enum
- **dto** — `SendOtpRequest`, `VerifyOtpRequest`, `OtpSentResponse`, `OtpVerifiedResponse`, `SubmitPanRequest` (for the multipart fields), `RegistrationCompleteResponse`
- **exception** — `OtpVerificationException`, `RegistrationStateException`, `PanValidationException`; extend `GlobalExceptionHandler` to map these (400 / 409 / 422) and add a catch-all 500 handler + `MaxUploadSizeExceededException` handler
- **config** — `RegistrationProperties` (`@ConfigurationProperties("app.registration")`), `AppSecurityConfig`? **No** — no Spring Security added (out of scope; will note as a follow-up). `PasswordEncoder` bean (BCrypt).

### Dependencies added to `pom.xml`
- `spring-boot-starter-mail` (SMTP OTP)
- `com.google.cloud:google-cloud-vision` (OCR; runtime-optional, only used when provider=google-vision)
- `org.springframework.security:spring-security-crypto` (BCrypt `PasswordEncoder` without pulling all of Spring Security)

### Config additions (`application.yml`)
- `app.registration.*`: `otp.length=6`, `otp.ttl-seconds=300`, `otp.resend-cooldown-seconds=60`, `otp.max-attempts=5`, `otp.max-sends=5`, `pan-image-dir`, `ocr.provider=stub` (default, so app runs with no cloud creds), `ocr.min-confidence=0.8`, `pan.name-match-distance=2`
- `spring.mail.*`: `host`, `port`, `username`, `password`, `properties.mail.smtp.*` (env-var overridable)
- `spring.servlet.multipart.max-file-size=5MB`, `max-request-size=10MB`
- Dev profile: `app.registration.ocr.provider=stub`, mail stubbed (logs OTP).

### Validation rules
- Mobile: 10-digit Indian (`^[6-9]\d{9}$`).
- Email: `@Email`.
- PAN: `^[A-Z]{5}[0-9]{4}[A-Z]$` (uppercase-normalized before check).
- OCR PAN match: exact after normalization.
- OCR name match: Levenshtein distance ≤ threshold after case/whitespace/punctuation normalization.

### Notes / explicit non-goals
- **PAN authenticity** (vs. income-tax DB) is **not** performed — per your choice, only format + image/OCR consistency. The OCR provider is Google Cloud Vision, but defaults to a stub so the app builds and runs without Google credentials; flip `ocr.provider=google-vision` and set `GOOGLE_APPLICATION_CREDENTIALS` to enable.
- **No Spring Security / JWT** — the `attemptId` serves as a weak session token for the flow. Production-grade auth is a follow-up.
- Existing `CreditReport` sample code is left untouched.
- I'll verify with `mvn -q -DskipTests compile` (and run tests if they pass quickly).

### Execution order
1. `pom.xml` deps → 2. `V2` migration → 3. models + enum → 4. repositories → 5. DTOs → 6. exceptions + handler extension → 7. `RegistrationProperties` + `PasswordEncoder` bean → 8. `OcrClient` + impls → 9. `OtpService`, `MailService`, `PanValidator` → 10. `RegistrationService` → 11. `RegistrationController` → 12. `application.yml` → 13. `mvn compile`.