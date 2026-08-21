# Phone sign-in SMS (MSG91)

How the phone-OTP codes get delivered, and what has to be true on the MSG91 / DLT side for
them to arrive.

## The flow

```
app  POST /api/auth/otp/phone/send   { "phone": "+919876543210" }
        └─ AuthService.SendPhoneOTP
             ├─ normalizePhone      -> "+91XXXXXXXXXX"
             ├─ OTPService.Issue    -> 6-digit code, bcrypt-hashed, 10m TTL,
             │                         30s resend cooldown, max 5 sends
             ├─ commit the challenge
             └─ sms.Sender.SendOTP  -> MSG91 Flow API   (or the log-only stub)

app  POST /api/auth/otp/phone/verify { "phone": "...", "otp": "123456" }
        └─ AuthService.VerifyPhoneOTP -> find-or-create the account, issue a session
```

Delivery happens **after** the challenge is committed, matching the email path
(`issueAndSend`). A provider failure therefore burns that send slot: the caller gets the
error and can retry once the 30s cooldown lapses. The alternative — send first, store
second — risks a code the user can read but the server has never heard of, which is worse.

The code itself never leaves this service in a readable form: it is bcrypt-hashed at rest and
passed to MSG91 only as a template variable.

## Configuration

```yaml
sms:
  provider: msg91
  msg91:
    auth-key: ""                          # SECRET. Empty -> log-only stub sender
    template-id: "6a845b1f9fa9adf1da0c06d3"
    sender-id: "REAOUT"
    otp-var: "OTP"
    app-signature: ""                     # see "Android auto-read" below
    app-signature-var: ""
    base-url: ""                          # empty -> https://control.msg91.com/api/v5
    timeout: 15s
```

Every key is overridable by env var (`SMS_MSG91_AUTH_KEY`, `SMS_MSG91_TEMPLATE_ID`, …).

**Only `auth-key` is a secret.** Keep it out of git — put it in the gitignored
`config.dev.yaml`, or export `SMS_MSG91_AUTH_KEY`. An empty auth key selects `StubSender`,
which logs the code instead of sending it, exactly like the empty-`MAIL_HOST` mail stub. The
boot log says which one is live:

```
level=WARN msg="sms.msg91.auth-key is empty; phone OTPs will be logged, not sent"
level=INFO msg="service starting" ... sms_stub=true
```

## The template

MSG91's Flow API cannot send arbitrary text. In India an SMS is only deliverable through a
template already approved on the DLT registry, so the wording lives at MSG91 and this service
only fills in variables. The approved template is:

```
##OTP## is your OTP for verification on Reachout. This OTP is valid for 10 minutes.
Please do not share it with anyone.
- Reachout
```

`otp-var: "OTP"` is what maps the generated code onto `##OTP##`. **Variable names are
case-sensitive and an unknown one is silently dropped** — get it wrong and the SMS arrives
with the literal `##OTP##` still in it, which is why `TestSendOTP_RequestShape` asserts the
exact request body rather than just a 200.

`template-id` is **MSG91's own template id** (`6a845b1f9fa9adf1da0c06d3`), from the panel's
SMS → Templates. It is *not* the 19-digit DLT template id (`1777178704589774509`) that
the wording is registered under on the telecom registry — those are two different
identifiers for the same template, and the Flow API only accepts MSG91's. Passing the DLT id
comes back as `{"type":"error"}` naming the template.

### MSG91 reports failures behind HTTP 200

A rejected send — unknown template, blocked number, no balance — comes back as
`200 {"type":"error","message":"..."}`. The client treats any `type != "success"` as a
failure; trusting the status code alone would report success to a user who never gets a text.

## Android auto-read — currently NOT enabled

Design S3 wants the app to read the code out of the SMS automatically (Google's SMS Retriever
API). Play Services will only hand a message to the app if that message:

1. begins with `<#>`,
2. contains the code,
3. **ends with the app's 11-character signing hash**, and
4. is **no longer than 140 bytes**.

The approved template above delivers 129 bytes. Adding `<#> ` and a trailing hash makes it
**145 bytes — 5 over the limit**, so auto-read cannot be switched on with this template. It is
not a code problem: the app-side listener is already implemented
(`shared/.../feature/auth/SmsOtpRetriever.android.kt`) and simply never fires, falling back to
manual entry.

To enable it, get a **second, shorter** DLT template approved that ends in a variable, e.g.

```
<#> ##OTP## is your Reachout OTP. Valid 10 min. Do not share. ##HASH##
```

(72 bytes with a real hash substituted — comfortably inside the limit), then set:

```yaml
    template-id: "<the new template's id>"
    app-signature: "FA+9qCX9VSu"   # the app's hash, NOT this placeholder
    app-signature-var: "HASH"
```

Both `app-signature` and `app-signature-var` must be set; either alone is ignored, so a
half-configured deploy can't put a stray variable on the wire.

Get the hash from the app's own log — it is printed when the OTP screen starts listening:

```
I/SmsOtpRetriever: SMS Retriever app signature hash(es) for the SMS template: [FA+9qCX9VSu]
```

**Debug and release builds are signed differently and have different hashes.** If Play App
Signing is on, the hash that matters is the one for Google's re-signed release build, not the
locally signed one. A template can only carry one, so plan on one template per signing
identity (or accept that auto-read works on the release build only).

## Testing without credentials

To complete a phone sign-in on a machine with no MSG91 key, leave `auth-key` empty and use the
**master OTP**: `123456` (`masterOTP` in `internal/service/otp.go`), accepted for any challenge
on every OTP flow in the service.

The generated code is **not** recoverable in that mode — nothing logs it (see below) — so the
master OTP is the only way through. That also means the master OTP is load-bearing for local
development, and it is still an unconditional auth bypass gated by nothing, whose value is the
first one anyone would guess. It **must be deleted before production**; budget for replacing it
with a real dev path (a test-only endpoint, or a fixed challenge for a whitelisted number)
rather than discovering at cutover that removing it makes the app untestable.

## PII in logs

No sender logs the OTP, in any mode. A one-time code in a log file is a live credential in
plaintext wherever those logs are shipped, tailed or shared, and "only the dev stub does it"
is not a boundary that holds in practice.

Both senders log a masked number (`+91******3210`) instead of the full one, so support can
match a "my OTP never arrived" ticket to a log line without a plaintext mobile landing in
every sink. The real sender adds MSG91's `request_id` on success, which is what you quote to
MSG91 support. Inbound request bodies are separately masked by the PII middleware
(`internal/server/middleware/pii.go` has `otp` and `phone` in its field set), so the code does
not leak through the request logger either.
