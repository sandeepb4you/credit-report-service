"""Generate Digitap credit-report fixtures for four score bands.

Written as a generator rather than by hand so each report is internally
consistent: the CAIS summary counts match the tradelines, the history months
march back from the report date without gaps, and the outstanding-balance split
adds up. Hand-edited JSON of this size drifts on the first change.

Output: internal/digitap/testdata/score_<band>_*.json
"""

import json
import os
from datetime import date

REPORT_DATE = date(2026, 8, 30)
REPORT_TIME = "101500"

OUT_DIR = os.path.join("internal", "digitap", "testdata")

# The subject is synthetic on purpose. Nothing in the report drives the name the
# app shows -- that comes from the accounts table -- so these fields are
# cosmetic, and inventing a real-looking person here would be worse than
# obviously-fake placeholders.
SUBJECT = {
    "first": "SAMPLE",
    "last": "SUBJECT",
    "dob": "19900615",
    "gender": "1",
    "email": "sample.subject@example.invalid",
    "pan_masked": "ABCPS XXXXQ",
    "mobile_masked": "XXXXXX0005",
    "addr1": "12 MG ROAD",
    "addr2": "INDIRANAGAR BENGALURU",
    "state": "29",
    "pin": "560038",
}


def month_back(n, anchor=None):
    """The (year, month) n months before `anchor` (default: the report month)."""
    anchor = anchor or REPORT_DATE
    total = anchor.year * 12 + (anchor.month - 1) - n
    return total // 12, total % 12 + 1


def history(months, late=None, anchor=None):
    """Newest-first CAIS history, counting back from `anchor`.

    `late` maps a month index (0 = the month before the anchor) to days past
    due. Everything else is a clean month. Asset classification follows the DPD
    so the two never contradict each other, even though the parser prefers DPD.

    The anchor is the close date for a closed tradeline and the report date for
    a live one: a lender stops reporting the month it closes the account, so a
    settled 2023 loan carrying rows up to last month is not a report any bureau
    would send.
    """
    late = late or {}
    out = []
    for i in range(months):
        # Index 0 is the month BEFORE the anchor month: a report dated the 30th
        # does not yet carry that month's own performance.
        y, m = month_back(i + 1, anchor)
        dpd = late.get(i, 0)
        if dpd == 0:
            cls = "STD"
        elif dpd < 90:
            cls = "STD"
        elif dpd < 120:
            cls = "SUB"
        elif dpd < 180:
            cls = "DBT"
        else:
            cls = "LSS"
        out.append({
            "Asset_Classification": cls,
            "Days_Past_Due": f"{dpd:03d}",
            "Month": f"{m:02d}",
            "Year": str(y),
        })
    return out


def profile_string(months, late):
    """Payment_History_Profile, kept in step with the history array.

    The parser prefers CAIS_Account_History and only falls back to this, but a
    report where the two disagree is not a report any bureau would send.
    """
    chars = []
    for i in range(months):
        dpd = late.get(i, 0)
        chars.append("0" if dpd == 0 else str(min(dpd // 30, 9)))
    # 36 slots is the Experian convention; unreported months are "?".
    return "".join(chars)[:36].ljust(36, "?")


def account(
    number, acct_type, status, portfolio, subscriber, ident,
    opened, reported, months, late=None, tenure="0", original="0",
    balance="0", limit="0", roi=None, emi=None, closed=None,
    written_off=None, past_due=None, first_delinquency=None,
    last_payment=None,
):
    late = late or {}
    # A closed account stops reporting when it closes; a live one reports up to
    # the bureau pull.
    anchor = date(int(closed[:4]), int(closed[4:6]), int(closed[6:])) if closed else None
    return {
        "AccountHoldertypeCode": "1",
        "Account_Number": number,
        "Account_Status": status,
        "Account_Type": acct_type,
        "Amount_Past_Due": past_due,
        "CAIS_Account_History": history(months, late, anchor),
        "CAIS_Holder_Address_Details": [{
            "Address_indicator_non_normalized": "02",
            "CountryCode_non_normalized": "IB",
            "First_Line_Of_Address_non_normalized": SUBJECT["addr1"],
            "Second_Line_Of_Address_non_normalized": SUBJECT["addr2"],
            "State_non_normalized": SUBJECT["state"],
            "ZIP_Postal_Code_non_normalized": SUBJECT["pin"],
        }],
        "CAIS_Holder_Details": [{
            "Date_of_birth": SUBJECT["dob"],
            "Gender_Code": SUBJECT["gender"],
            "Surname_Non_Normalized": SUBJECT["last"],
        }],
        "CAIS_Holder_ID_Details": [{
            "EMailId": SUBJECT["email"],
            "Income_TAX_PAN": SUBJECT["pan_masked"],
        }],
        "Credit_Limit_Amount": limit,
        "CurrencyCode": "INR",
        "Current_Balance": balance,
        "DateOfAddition": opened,
        "Date_Closed": closed,
        "Date_Reported": reported,
        "Date_of_First_Delinquency": first_delinquency,
        "Date_of_Last_Payment": last_payment,
        "Highest_Credit_or_Original_Loan_Amount": original,
        "Identification_Number": ident,
        "Occupation_Code": "S",
        "Open_Date": opened,
        "Payment_History_Profile": profile_string(months, late),
        "Payment_Rating": "0" if not late else "1",
        "Portfolio_Type": portfolio,
        "Rate_of_Interest": roi,
        "Repayment_Tenure": tenure,
        "Scheduled_Monthly_Payment_Amount": emi,
        "Subscriber_Name": subscriber,
        "Written_off_Settled_Status": written_off,
    }


CLOSED_STATUSES = {"12", "13", "14", "15", "16", "17", "132", "133", "138"}


def summarize(accounts):
    """CAIS_Summary derived from the tradelines, not asserted independently."""
    active = closed = default = 0
    secured = unsecured = 0
    for a in accounts:
        status = a["Account_Status"].lstrip("0") or "0"
        if status in CLOSED_STATUSES:
            closed += 1
            continue
        active += 1
        bal = int(a["Current_Balance"] or 0)
        # Revolving is unsecured; installment against an asset is secured. Good
        # enough for the split the summary reports, which is what the app shows.
        if a["Portfolio_Type"] == "R" or a["Account_Type"] in ("05", "06"):
            unsecured += bal
        else:
            secured += bal
        if a["Account_Status"] == "97" or (a["Written_off_Settled_Status"] or "") not in ("", "?", "0", "00"):
            default += 1
    total = secured + unsecured
    pct = lambda v: str(round(v * 100 / total)) if total else "0"
    return {
        "Credit_Account": {
            "CADSuitFiledCurrentBalance": "0",
            "CreditAccountActive": str(active),
            "CreditAccountClosed": str(closed),
            "CreditAccountDefault": str(default),
            "CreditAccountTotal": str(len(accounts)),
        },
        "Total_Outstanding_Balance": {
            "Outstanding_Balance_All": str(total),
            "Outstanding_Balance_Secured": str(secured),
            "Outstanding_Balance_Secured_Percentage": pct(secured),
            "Outstanding_Balance_UnSecured": str(unsecured),
            "Outstanding_Balance_UnSecured_Percentage": pct(unsecured),
        },
    }


def envelope(score, accounts, enquiries, ref_suffix):
    """The full Digitap response, exactly as the client receives it."""
    rd = REPORT_DATE.strftime("%Y%m%d")
    caps = {
        "CAPSLast180Days": str(enquiries["d180"]),
        "CAPSLast90Days": str(enquiries["d90"]),
        "CAPSLast30Days": str(enquiries["d30"]),
        "CAPSLast7Days": str(enquiries["d7"]),
    }
    total_caps = {
        "TotalCAPSLast180Days": str(enquiries["d180"]),
        "TotalCAPSLast90Days": str(enquiries["d90"]),
        "TotalCAPSLast30Days": str(enquiries["d30"]),
        "TotalCAPSLast7Days": str(enquiries["d7"]),
    }
    return {
        "http_response_code": 200,
        "client_ref_num": f"CA-SCENARIO-{ref_suffix}",
        "request_id": f"scenario-{ref_suffix}",
        "result_code": 101,
        "message": "success",
        "result": {
            "result_json": {
                "INProfileResponse": {
                    "Header": {
                        "SystemCode": "0",
                        "MessageText": None,
                        "ReportDate": rd,
                        "ReportTime": REPORT_TIME,
                    },
                    "UserMessage": {"UserMessageText": "Normal Response"},
                    "CreditProfileHeader": {
                        "Enquiry_Username": "customized_match_v3__decimusfin_~DS",
                        "ReportDate": rd,
                        "ReportNumber": f"scenario{ref_suffix}",
                        "ReportTime": REPORT_TIME,
                        "Subscriber": None,
                        "Subscriber_Name": "Bureau Disclosure Report with Customized Match V3",
                        "Version": "V2.4",
                    },
                    "Current_Application": {
                        "Current_Application_Details": {
                            "Amount_Financed": "0",
                            "Current_Applicant_Address_Details": [{
                                "City": None, "Country_Code": "IB",
                                "FlatNoPlotNoHouseNo": None, "PINCode": None, "State": None,
                            }],
                            "Current_Applicant_Details": {
                                "Date_Of_Birth_Applicant": None,
                                "EMailId": None,
                                "First_Name": SUBJECT["first"],
                                "Gender_Code": None,
                                "Last_Name": SUBJECT["last"],
                                "Middle_Name1": None,
                                "MobilePhoneNumber": SUBJECT["mobile_masked"],
                            },
                            "Current_Other_Details": {
                                "Employment_Status": None, "Income": "0", "Marital_Status": None,
                            },
                            "Duration_Of_Agreement": "0",
                            "Enquiry_Reason": "6",
                            "Finance_Purpose": None,
                        }
                    },
                    "CAIS_Account": {
                        "CAIS_Account_DETAILS": accounts,
                        "CAIS_Summary": summarize(accounts),
                    },
                    "Match_result": {"Exact_match": "Y"},
                    "TotalCAPS_Summary": total_caps,
                    "CAPS": {"CAPS_Summary": caps},
                    "NonCreditCAPS": {"NonCreditCAPS_Summary": {
                        "NonCreditCAPSLast180Days": "0", "NonCreditCAPSLast90Days": "0",
                        "NonCreditCAPSLast30Days": "0", "NonCreditCAPSLast7Days": "0",
                    }},
                    "SCORE": {"BureauScore": str(score), "BureauScoreConfidLevel": "H"},
                }
            }
        },
    }


# ---------------------------------------------------------------------------
# The four scenarios.
#
# Each is tuned so the report card the parser derives matches the band a user
# would expect from the score, because the score and the card are shown on the
# same screen: a 500 whose factors all grade A reads as a bug.
# ---------------------------------------------------------------------------

SCENARIOS = {}

# --- 800: long, clean, low-utilisation file. Every factor A+. ---------------
SCENARIOS["score_800_excellent.json"] = envelope(
    score=800,
    enquiries={"d180": 0, "d90": 0, "d30": 0, "d7": 0},
    ref_suffix="800",
    accounts=[
        account("XXXXXXXXXXXXXXX4417", "02", "11", "I", "HDFC Bank Ltd", "PVTHDFC441",
                opened="20160512", reported="20260812", months=36,
                tenure="240", original="6500000", balance="3120000",
                roi="8.35", emi="55900", last_payment="20260805"),
        account("XXXXXXXXXXXXXXX9032", "10", "11", "R", "Axis Bank Limited", "PVTAXIS903",
                opened="20110308", reported="20260810", months=36,
                limit="500000", balance="28000", roi="42.00", last_payment="20260808"),
        account("XXXXXXXXXXXXXXX2288", "01", "13", "I", "Kotak Mahindra Bank", "PVTKOTK228",
                opened="20180701", reported="20230710", months=24,
                tenure="60", original="850000", balance="0", roi="9.10",
                closed="20230705", last_payment="20230705"),
        account("XXXXXXXXXXXXXXX7754", "05", "13", "I", "ICICI Bank Limited", "PVTICIC775",
                opened="20190211", reported="20220225", months=24,
                tenure="36", original="400000", balance="0", roi="13.50",
                closed="20220220", last_payment="20220220"),
    ],
)

# --- 700: solid file with real blemishes. Card lands on B. ------------------
SCENARIOS["score_700_good.json"] = envelope(
    score=700,
    enquiries={"d180": 3, "d90": 2, "d30": 1, "d7": 0},
    ref_suffix="700",
    accounts=[
        account("XXXXXXXXXXXXXXX3160", "10", "11", "R", "HDFC Bank Ltd", "PVTHDFC316",
                opened="20190614", reported="20260812", months=36,
                late={5: 30, 14: 30, 22: 30},
                limit="250000", balance="92000", roi="41.88", last_payment="20260807"),
        account("XXXXXXXXXXXXXXX8821", "05", "11", "I", "Bajaj Finance Limited", "PVTBAJF882",
                opened="20240302", reported="20260810", months=29,
                late={3: 30, 11: 30},
                tenure="48", original="500000", balance="322000",
                roi="15.25", emi="13950", last_payment="20260805"),
        account("XXXXXXXXXXXXXXX5507", "13", "13", "I", "TVS Credit Services Ltd", "PVTTVSC550",
                opened="20210118", reported="20230205", months=24,
                tenure="24", original="95000", balance="0", roi="18.50",
                closed="20230201", last_payment="20230201"),
    ],
)

# --- 600: stressed but NOT defaulted. No write-offs, so the rebuild plan can
#          still open with something true and reassuring. ---------------------
SCENARIOS["score_600_fair.json"] = envelope(
    score=600,
    enquiries={"d180": 5, "d90": 3, "d30": 2, "d7": 1},
    ref_suffix="600",
    accounts=[
        account("XXXXXXXXXXXXXXX6041", "10", "11", "R", "SBI Cards and Payment Services", "PVTSBIC604",
                opened="20220405", reported="20260814", months=29,
                late={1: 30, 4: 60, 9: 30, 15: 30, 20: 60, 26: 30},
                limit="150000", balance="118000", roi="43.20", last_payment="20260726",
                past_due="11800"),
        account("XXXXXXXXXXXXXXX4473", "05", "11", "I", "Fullerton India Credit Co Ltd", "PVTFULL447",
                opened="20230821", reported="20260811", months=24,
                late={2: 30, 6: 60, 10: 90, 17: 30, 21: 30},
                tenure="36", original="300000", balance="186000",
                roi="19.75", emi="11080", last_payment="20260722",
                first_delinquency="20240415", past_due="11080"),
        account("XXXXXXXXXXXXXXX1290", "06", "13", "I", "Bajaj Finance Limited", "PVTBAJF129",
                opened="20220210", reported="20230215", months=12,
                late={7: 30},
                tenure="12", original="42000", balance="0", roi="22.00",
                closed="20230210", last_payment="20230210"),
    ],
)

# --- 500: derogatory. One written-off loan, one settled account, a maxed card.
SCENARIOS["score_500_poor.json"] = envelope(
    score=500,
    enquiries={"d180": 9, "d90": 6, "d30": 3, "d7": 1},
    ref_suffix="500",
    accounts=[
        account("XXXXXXXXXXXXXXX2015", "10", "11", "R", "Axis Bank Limited", "PVTAXIS201",
                opened="20231102", reported="20260813", months=22,
                late={0: 60, 1: 30, 3: 90, 4: 60, 6: 30, 8: 120, 10: 90,
                      12: 60, 14: 30, 16: 60, 18: 30, 20: 30},
                limit="60000", balance="59500", roi="45.60", last_payment="20260610",
                first_delinquency="20241118", past_due="8900"),
        account("XXXXXXXXXXXXXXX7736", "05", "97", "I", "Money View Finance", "PVTMONY773",
                opened="20240115", reported="20260810", months=20,
                late={0: 180, 1: 180, 2: 150, 3: 150, 4: 120, 5: 120, 6: 90,
                      7: 90, 8: 60, 9: 60, 10: 30, 11: 30},
                tenure="24", original="180000", balance="143500",
                roi="26.50", emi="9850", last_payment="20250228",
                written_off="01", first_delinquency="20250310", past_due="143500"),
        account("XXXXXXXXXXXXXXX9948", "06", "13", "I", "Home Credit India Finance", "PVTHOME994",
                opened="20240508", reported="20250620", months=12,
                late={2: 60, 3: 30, 5: 90, 7: 30},
                tenure="12", original="35000", balance="0", roi="28.00",
                closed="20250615", last_payment="20250615",
                written_off="03"),
    ],
)


def main():
    os.makedirs(OUT_DIR, exist_ok=True)
    for name, doc in SCENARIOS.items():
        path = os.path.join(OUT_DIR, name)
        with open(path, "w", encoding="utf-8") as fh:
            json.dump(doc, fh, indent=2, sort_keys=True)
            fh.write("\n")
        print(f"wrote {path}")


if __name__ == "__main__":
    main()
