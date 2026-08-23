# PAN verification (Digitap Mobile to Prefill)

At signup a user enters their PAN and the name printed on it. The service asks
Digitap what identity is registered against the mobile number that user just
proved control of over SMS OTP, and compares.

A match means those three facts agree. It is **not** a check that the PAN exists
at the income-tax department, and the wording shown to users avoids implying it
is.

```
POST /api/kyc/pan  { "pan": "ABCDE1234F", "fullName": "JOHN DOE" }
   └─ KycService.SubmitPAN
        ├─ format checks, then UpsertPAN (stores the claim, PENDING)
        ├─ PrefillVerifier.Verify
        │     └─ POST <base>/mobile_prefill/request  { mobile_no, name_lookup: 1 }
        ├─ record the call in prefill_lookups (always, including failures)
        └─ match -> VERIFIED + fill the account's first/last name
```

`name_lookup` is always 1. Passing our own first/last name instead makes the API
search for the name we supplied, so a "match" would only confirm that our own
input came back — worthless as verification.

## Outcomes

| Outcome | HTTP | Effect |
| --- | --- | --- |
| PAN and name match | 201 | VERIFIED; profile name filled from the provider's spelling |
| Mismatch | 422 | Attempt counted, retry allowed |
| Attempts exhausted | 422 | Refused until the PAN changes |
| No record (102/103) or source error (503) | 201 | Stored PENDING, user **not** blocked |
| Bad credentials / service not enabled | 500 | Our misconfiguration, surfaced as a service failure |

The gap case is deliberate. Prefill coverage is not universal, and a missing row
in someone else's database says nothing about whether this user is honest.
Blocking there would strand legitimate users on the first screen after signup,
with nothing they could do about it.

The attempt cap (`registration.pan.max-verification-attempts`, default 3) exists
because PAN-plus-name is guessable for a known person, so an uncapped retry loop
is a brute-force oracle billed to us per call. Submitting a **different** PAN
resets the counter — a user who mistypes twice should not be locked out by their
own typos — while re-submitting the same one keeps it, or the cap could be
cleared by pressing the button again.

## Credentials and provisioning

```yaml
digitap:
  prefill:
    base-url: https://api.digitap.ai/      # UAT: https://svcdemo.digitap.work/
    client-id: ""                          # falls back to digitap.client-id
    client-secret: ""
```

Env: `DIGITAP_PREFILL_CLIENT_ID` / `DIGITAP_PREFILL_CLIENT_SECRET`.

**Mobile to Prefill is provisioned per client id, separately from Credit
Analytics.** A credential pair that authenticates fine for the bureau pull can
still return `401 Client Authentication Failed` here. Verified against the live
API on 2026-08-23:

| Endpoint | `api.digitap.ai` | `svc.digitap.ai` | `svcdemo.digitap.work` |
| --- | --- | --- | --- |
| `/mobile_prefill/request` | 401 | 401 | 401 |
| `/credit_analytics/request` | 400 | 400 | 401 |

(400 = the request passed authentication and failed on its payload.) The two
production hostnames behave identically, so the host is not the variable here.

That 400 is what proves the credentials themselves are valid — the request got
past authentication and failed on its payload. Enabling the product, and the
name-lookup service inside it, is a request to the Digitap RM.

Note also that `digitap.base-url` (Credit Analytics) still points at the demo
host while the credentials are production, so that pull would 401 too.

### Checking it without the app

```sh
DIGITAP_PREFILL_CLIENT_ID=... DIGITAP_PREFILL_CLIENT_SECRET=... \
  go run ./cmd/prefillcheck -mobile 9876543210        # -uat for the UAT host
```

No database, emulator or account needed. It separates "is our integration right"
from "does the provider have this person", and names the likely cause when the
call fails. A live lookup returns real personal data and is billed — use your
own number.

### With no credentials

The client falls back to an offline stub that claims **every** mobile number
belongs to `JOHN DOE` / `ABCDE1234F`. Enter those to pass verification locally.
The stub is fixed rather than echoing the request back on purpose: a stub that
agreed with whatever the user typed would rubber-stamp the comparison this whole
flow exists to perform. Rows verified that way are stored with
`provider = 'stub'`, and the boot log warns.

## Stored data and retention

Every call writes a row to `prefill_lookups`, whatever the outcome — a
verification decision is something a user can dispute months later, and without
the provider's own answer beside the decision there is no telling a provider
data gap from our bug.

`response_raw` holds only the fields the service decodes (name, dob, pan,
official documents), not the whole upstream body. That bound is deliberate:
enabling an option at Digitap — addresses, alternate numbers, employment,
income band — would otherwise start depositing that data here with no code
change and no decision to collect it.

**There is no retention policy yet, and one is required before launch.** This
table holds identity data about people, obtained from a bureau rather than from
them, so DPDP storage limitation applies. Decide a window, then purge on a
schedule rather than by hand. The table is never exposed through an API.
