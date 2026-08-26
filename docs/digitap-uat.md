# Digitap UAT

Two Digitap environments, switched by the VS Code runner you launch. Nothing
else differs between them: the DB, the MSG91 stub, and the OTP master code all
come from the `dev` profile either way, so picking a runner picks a credit
bureau and changes nothing else about the local stack.

| Runner (Run and Debug panel) | Credit Analytics upstream | PAN verification |
| --- | --- | --- |
| `Run server (dev · Digitap LIVE)` | `api.digitap.ai`, client `36537966` | real prefill name-lookup |
| `Run server (dev · Digitap UAT)` | `apidemo.digitap.work`, client `43398827` | demo mode, or the offline stub — see below |

Each also has a `debug logs` variant. LIVE credentials live in `config.dev.yaml`;
UAT credentials in `.env.digitap-uat`, which the UAT runner loads via `envFile`.
Both files are gitignored — `.vscode/launch.json` is tracked, so no secret goes
in it.

If VS Code says the envFile is missing, create `.env.digitap-uat` at the repo
root with `DIGITAP_BASE_URL`, `DIGITAP_CLIENT_ID`, `DIGITAP_CLIENT_SECRET` for
UAT, plus `DIGITAP_PREFILL_CLIENT_ID=stub`.

## Test identities

UAT holds no real bureau data — a genuine PAN returns nothing useful. These are
the accounts Digitap provisions in the UAT database, so a report only comes back
for one of these five. They are Digitap's own synthetic records (note the
`@digitap.ai` addresses), which also makes them the only identities that are
safe to probe by hand.

| First | Last | DOB | PAN | Mobile | Email |
| --- | --- | --- | --- | --- | --- |
| Shubhra | Dutta | 1991-09-24 | `FAWPD4345T` | 7908096603 | shubhra.dutta@digitap.ai |
| Piyush | Shukla | 1991-09-13 | `VDRPS3454R` | 9305553595 | piyush.shukla@digitap.ai |
| Deepti | Singh | 1990-09-15 | `BDRPS5609Y` | 8416986878 | deepti.singh@digitap.ai |
| Sukhjinder | Singh | 1990-08-19 | `TGHPS7231K` | 9822616123 | sukhjinder@digitap.ai |
| Trisha | Dhawe | 1990-07-17 | `WLCPD4323E` | 9584324371 | trisha.dhawe@digitap.ai |

DOB is written ISO (`YYYY-MM-DD`) above because that is how the source sheet
gives the values, even though its column header says `DD-MM-YYYY`. **The API
itself uses `DD-MM-YYYY`** — a prefill response carries `"dob": "24-09-1991"` —
so convert when comparing. We never send DOB; only Credit Analytics' `timestamp`
has a date format we control, and that is `DDMMYYYY-HH:MM:SS`.

Verified 2026-08-26: a full payload for Shubhra Dutta / `FAWPD4345T` /
7908096603 against `apidemo.digitap.work` returns 200, `result_code` 101, and a
complete Experian `INProfileResponse`.

## Hosts do not serve the same routes

Established by probing all four hosts with the UAT credentials on 2026-08-26.
Read **400 as success**: it means authentication passed and only the (empty)
payload was rejected.

| Host | `/credit_analytics/request` | `/mobile_prefill/request` |
| --- | --- | --- |
| `apidemo.digitap.work` | 400 | **404 — no such route** |
| `svcdemo.digitap.work` | 400 | 400 |

So the UAT hostname Digitap hands out for Credit Analytics does not serve Mobile
to Prefill at all. `digitap.base-url` and `digitap.prefill.base-url` are separate
keys precisely so the two products can point at different hosts.

## Walking the flow

Verified end to end on 2026-08-26 against `apidemo.digitap.work`. Prefill runs
only at the PAN step; the report pull never touches it.

| Step | Notes |
| --- | --- |
| `POST /auth/otp/phone/send` | `{"phone":"7908096603"}` — SMS provider is the stub, nothing is sent |
| `POST /auth/otp/phone/verify` | `{"phone":"7908096603","otp":"1234"}` — the `auth.otp.master-code`. Returns the bearer token |
| `POST /kyc/pan` | `{"pan":"FAWPD4345T","fullName":"Shubhra Dutta"}` → 201 VERIFIED |
| `PUT /profile` | `{"firstName":"Shubhra","lastName":"Dutta","dateOfBirth":"1991-09-24"}` — **required**, see below |
| `POST /credit-analytics/request` | `{"device_ip":"1.2.3.4"}` → 201. Upstream 200, `result_code` 101, ~1.8s |

That returned credit score 772, one active auto loan, ₹150,000 outstanding of
₹200,000 original.

**`PUT /profile` is not optional.** `buildPayload` refuses to assemble a Credit
Analytics request without a first and last name on the account, and the PAN step
does not always supply them: real prefill fills them from the provider's spelling
(which is what lets a phone signup skip a profile form), but neither demo mode
nor the offline stub does. Skip it and the report request fails with
`first_name`/`last_name` validation errors rather than anything about the bureau.

Submit her **real** PAN and name at the PAN step, not stub values — see the next
section for which of the two paths you are on.

## What verifies the PAN on UAT

Two different things can satisfy the PAN step, and which one runs decides what
you type. Neither is the real prefill check.

**Demo mode, if `demo.enabled` is true** — currently the default in the tracked
`config.yaml`. `POST /api/kyc/pan` is auto-verified immediately with
`provider = 'demo'`, no provider call at all, and the PAN and name are stored
**exactly as submitted**. This is the path the walkthrough above takes, and it is
the only reason a report for a specific test identity is reachable: enter the
real `FAWPD4345T` / `Shubhra Dutta` so that the payload Credit Analytics builds
matches a UAT record.

**The offline prefill stub, if demo mode is off.** `.env.digitap-uat` sets
`DIGITAP_PREFILL_CLIENT_ID=stub`, which forces it the way `sms.provider: stub`
forces the log-only sender. The stub claims every mobile belongs to
**`JOHN DOE` / `ABCDE1234F`**, so those become the only values that pass — and
because it overwrites the account name with its own spelling, the report is then
pulled for JOHN DOE and UAT has no such record. Useful for exercising the
screen, not for getting a report back.

Rows from either path are marked (`provider = 'demo'` / `'stub'`) so they can
never be mistaken for a provider confirmation.

### Why the stub is needed at all

A provisioning gap, not a shortcut. On `svcdemo.digitap.work` — the UAT host that
does serve the route — client `43398827` answers:

```
401  {"message": "name look-up service is not enabled"}
```

The service always sends `name_lookup: 1`, deliberately: passing our own
first/last name instead makes the API search for the name we supplied, so a
"match" would only confirm our own input came back. There is no degraded mode, so
the real prefill check cannot complete against UAT at all.

Without the sentinel, an empty `digitap.prefill.client-id` falls back to the
Credit Analytics credentials (`cmd/server/main.go`), so there was previously no
way to run PAN verification offline while credit-analytics talked to a real
upstream.

To use the real thing, ask Digitap to enable the name-lookup service on client
id `43398827`, then swap the sentinel for:

```
DIGITAP_PREFILL_BASE_URL=https://svcdemo.digitap.work/
DIGITAP_PREFILL_CLIENT_ID=43398827
DIGITAP_PREFILL_CLIENT_SECRET=<the UAT secret>
```

## Seeing the outgoing request

`digitap.log-request-curl` prints each Credit Analytics call as a runnable curl
just before it goes out, which is the quickest way to compare what we sent
against what the spec wants. It is on in both runners and refuses to boot under
a non-dev profile — the command embeds the PAN, name, mobile and client secret.
See the `Warn` banner around it in `internal/digitap/client.go`.
