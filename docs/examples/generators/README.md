# Score-scenario generators

Two scripts, run from the repository root, that rebuild the 800 / 700 / 600 /
500 demo credit reports:

```
python docs/examples/generators/gen_score_scenarios.py      # -> internal/digitap/testdata/score_*.json
python docs/examples/generators/gen_score_scenarios_sql.py  # -> docs/examples/load_score_scenarios.sql
```

Run them in that order — the SQL embeds the JSON.

## Why they are generated

Each report is ~25 KB of interlocking fields: the CAIS summary counts have to
match the tradelines, every month of payment history has to march back from the
right anchor without gaps, and the outstanding-balance split has to add up.
Hand-edited JSON of that size drifts on the first change, and the drift is
invisible until a screen renders something absurd.

Editing a scenario means editing `SCENARIOS` in the first script and re-running
both. `internal/service/score_scenarios_test.go` then re-checks the result
through the real insights parser, including that each band's report card still
agrees with its headline score — which is the property the whole set exists for.

## Tuning a scenario

The grades come out of `buildReportCard`, so the thresholds worth knowing are:

| Factor | Weight | Driven by |
| --- | --- | --- |
| Payment history | 35% | on-time share across every reported month; A+ needs >=99% and zero misses |
| Credit utilisation | 30% | revolving balance / limit; A+ under 10%, D at 75%+ |
| Credit age | 15% | oldest `Open_Date`; A+ at 10 years, D under 1 |
| Enquiries | 10% | `TotalCAPSLast180Days`; A+ at 0, D above 6 |
| Credit mix | 10% | distinct `Account_Type` products, closed ones included; A+ at 4 |

A derogatory tradeline is `Account_Status` 97 or any non-empty
`Written_off_Settled_Status` — that count, not the score, is what decides
whether the rebuild plan can open with a reassurance.
