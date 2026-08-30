"""Emit docs/examples/load_score_scenarios.sql from the four fixtures.

Run from the repository root, after gen_score_scenarios.py.
"""

import json
import os

FIXTURES = [
    ("score_800_excellent.json", 800, "excellent",
     "Long, clean file: 15-year history, 5.6% utilisation, no enquiries. Every "
     "report-card factor A+, nothing to fix, so the app shows the "
     "protect/optimise journey."),
    ("score_700_good.json", 700, "good",
     "Solid borrower with real blemishes: five late months across 89 reported, "
     "and a card running at 37%. Overall B, the blended journey."),
    ("score_600_fair.json", 600, "fair",
     "Stressed but NOT defaulted: 79% utilisation, twelve late months, five "
     "enquiries in six months. Overall C. No write-offs, which is what lets the "
     "rebuild plan still open with something true and reassuring."),
    ("score_500_poor.json", 500, "poor",
     "Derogatory: a written-off personal loan still owed, a settled consumer "
     "loan, and a card at 99%. Overall D, two derogatory tradelines - the "
     "fixture that reaches the code paths a clean file never does."),
]

SRC = os.path.join("internal", "digitap", "testdata")
OUT = os.path.join("docs", "examples", "load_score_scenarios.sql")

HEADER = """\
-- Score-band demo reports: 800 / 700 / 600 / 500.
--
-- GENERATED FILE - edit docs/examples/generators/gen_score_scenarios.py and
-- re-run both generators instead. See that directory's README.
--
-- Loads a hand-built Digitap credit report into credit_analytics_requests so
-- every score band can be walked through in the app without paying for a bureau
-- pull. The JSON comes from internal/digitap/testdata/score_*.json and is
-- guarded by internal/service/score_scenarios_test.go, so what the app renders
-- from these rows is what the real parser makes of a real-shaped response.
--
-- ---------------------------------------------------------------------------
-- READ THIS BEFORE RUNNING IT ANYWHERE REAL
--
-- These reports are FABRICATED. They are not anybody's credit file, and the app
-- presents whatever is in this table as the signed-in user's own bureau data --
-- score, tradelines, missed payments and all. Load them only onto an account
-- you control and that no real customer can sign into.
--
-- Every row written here carries client_ref_num LIKE 'CA-SCENARIO-%', which is
-- what lets the cleanup below remove exactly these rows and nothing else. Do
-- not strip that prefix.
--
-- One consequence worth knowing: a stored report is reusable, so the paywall may
-- serve one of these in place of a fresh bureau pull until it ages out of
-- credit-analytics.reuse-window. Run the cleanup when you are done.
-- ---------------------------------------------------------------------------
--
-- Usage (psql):
--
--   \\set account_id 5
--   \\set scenario 800          -- 800 | 700 | 600 | 500 | history | clean
--   \\i docs/examples/load_score_scenarios.sql
--
-- Nothing is commented out and nothing needs editing: the scenario variable
-- selects the block. That is deliberate -- asking someone to uncomment a
-- multi-line INSERT by hand is one missed line away from a syntax error at
-- best, and half a statement at worst.

SET search_path TO report;

-- Defaults, so a run that forgets a \\set does nothing rather than guessing.
\\if :{?account_id} \\else \\set account_id 0 \\endif
\\if :{?scenario}   \\else \\set scenario none \\endif

SELECT :account_id = 0      AS no_account,
       :'scenario' = '800'  AS want_800,
       :'scenario' = '700'  AS want_700,
       :'scenario' = '600'  AS want_600,
       :'scenario' = '500'  AS want_500,
       :'scenario' = 'history' AS want_history,
       :'scenario' = 'clean'   AS want_clean
\\gset

\\if :no_account
\\echo '>> No account_id set. Run:  \\\\set account_id 5   \\\\set scenario 800'
\\q
\\endif
"""

FOOTER = """
-- ---------------------------------------------------------------------------
-- clean - remove every scenario report from this account, leaving real bureau
-- pulls untouched.
-- ---------------------------------------------------------------------------
\\if :want_clean
DELETE FROM credit_analytics_requests
 WHERE account_id = :account_id
   AND client_ref_num LIKE 'CA-SCENARIO-%';
\\echo '>> Scenario reports removed.'
\\endif

-- What the account is holding now.
SELECT id, credit_score, client_ref_num, created_at
  FROM credit_analytics_requests
 WHERE account_id = :account_id
 ORDER BY created_at DESC;
"""


def insert(score, body, days_ago=None):
    """One INSERT. days_ago back-dates the row for the history block."""
    extra_cols = ", created_at, data_fetched_at" if days_ago else ""
    extra_vals = (
        f", now() - interval '{days_ago} days', now() - interval '{days_ago} days'"
        if days_ago else ""
    )
    suffix = f"-{days_ago}d" if days_ago else ""
    tag = f"scenario{score}{days_ago or ''}"
    return f"""INSERT INTO credit_analytics_requests
    (account_id, client_ref_num, mobile_no, request_id, result_code, http_status,
     message, request_body, response_body, credit_score{extra_cols})
VALUES
    (:account_id, 'CA-SCENARIO-{score}{suffix}', 'XXXXXX0005', 'scenario-{score}{suffix}',
     101, 200, 'success', '{{}}'::jsonb,
     ${tag}${body}${tag}$::jsonb,
     {score}{extra_vals});"""


def load(name):
    with open(os.path.join(SRC, name), encoding="utf-8") as fh:
        doc = json.load(fh)
    # response_body stores the inner `result` object, exactly as the service
    # writes it (env.Result) - not the whole envelope.
    return json.dumps(doc["result"], separators=(",", ":"), sort_keys=True)


def main():
    parts = [HEADER]

    for name, score, label, description in FIXTURES:
        parts.append(f"""
-- ---------------------------------------------------------------------------
-- {score} - {label}
--
-- {description}
--
-- Source: internal/digitap/testdata/{name}
-- ---------------------------------------------------------------------------
\\if :want_{score}
{insert(score, load(name))}
\\echo '>> Loaded the {score} scenario.'
\\endif
""")

    parts.append("""
-- ---------------------------------------------------------------------------
-- history - all four at once as a rising trend (500 -> 800 over four months).
--
-- For the score-journey screen, which derives its trend from past pulls: one
-- report draws a point, four draw a recovery. The newest row wins everywhere
-- else, so afterwards the app behaves as though the account scores 800.
-- ---------------------------------------------------------------------------
\\if :want_history
""")
    for offset, (name, score, _, _) in zip((120, 90, 60, 30), reversed(FIXTURES)):
        parts.append(insert(score, load(name), days_ago=offset) + "\n")
    parts.append("""\\echo '>> Loaded all four scenarios as a 500 -> 800 history.'
\\endif
""")

    parts.append(FOOTER)

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "w", encoding="utf-8") as fh:
        fh.write("".join(parts))
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
