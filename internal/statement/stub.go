package statement

// Stub is a dev-only parser that returns a deterministic multi-transaction
// statement so the analyze flow runs end-to-end without a real PDF. Mirrors the
// offline-stub convention used by ocr.Stub and the Digitap stub client.
type Stub struct{}

func NewStub() *Stub { return &Stub{} }

// stubStatement is a synthetic HDFC-style statement with salary, EMI, UPI,
// card, ATM, NEFT, and cheque transactions across two months — enough to
// exercise every analyze() path in dev and CI.
const stubStatement = `Account Statement
Account No: 50100123456789
From 01/04/2024 to 31/05/2024

Date       Narration                                  Withdrawal     Deposit
05/04/2024 INFOSYS LTD SALARY                                         85000.00
06/04/2024 UPI/412345678/To GROCERY STORE              1200.50
07/04/2024 ATM/WITHDRAWAL                              2000.00
10/04/2024 NEFT/HOMELOAN EMI/NACH                      18500.00
12/04/2024 VISA/CARD SWIPE/AMAZON                      3500.00
15/04/2024 UPI/To FRIEND                               500.00
20/04/2024 CHEQUE/DEPOSIT                                             10000.00
28/04/2024 NETFLIX SUBSCRIPTION                        649.00
05/05/2024 INFOSYS LTD SALARY                                         86000.00
06/05/2024 UPI/412345679/To FUEL STATION               2500.00
10/05/2024 NEFT/HOMELOAN EMI/NACH                      18500.00
14/05/2024 VISA/CARD SWIPE/FLIPKART                    4200.00
18/05/2024 ATM/WITHDRAWAL                              3000.00
28/05/2024 NETFLIX SUBSCRIPTION                        649.00
`

// Extract implements Parser. It returns the canned statement text.
func (s *Stub) Extract(pdfBytes []byte) (text string, numPages int, err error) {
	return stubStatement, 1, nil
}
