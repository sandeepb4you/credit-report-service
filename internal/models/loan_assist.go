package models

import "time"

// Loan Assist application statuses (loan_assist_requests.status). DRAFT while
// the customer chooses a loan type and adds documents; SUBMITTED once paid,
// while the partner checks the documents; then the stages the partner sets.
// "Action needed" is not a status: it is a document waiting to be sent again.
const (
	LoanAssistDraft           = "DRAFT"
	LoanAssistSubmitted       = "SUBMITTED"
	LoanAssistDocsVerified    = "DOCS_VERIFIED"
	LoanAssistExpertContacted = "EXPERT_CONTACTED"
	LoanAssistGuidance        = "GUIDANCE"
	LoanAssistClosed          = "CLOSED"
)

// LoanAssistStages is every status a partner may set, in order.
var LoanAssistStages = []string{
	LoanAssistSubmitted, LoanAssistDocsVerified, LoanAssistExpertContacted,
	LoanAssistGuidance, LoanAssistClosed,
}

// IsLoanAssistStage reports whether s is a status a partner may set.
func IsLoanAssistStage(s string) bool {
	for _, v := range LoanAssistStages {
		if v == s {
			return true
		}
	}
	return false
}

// Per-document review states (loan_assist_documents.review_state).
const (
	LoanDocPending  = "PENDING"
	LoanDocAccepted = "ACCEPTED"
	LoanDocRetake   = "RETAKE"
)

// Document slots: what each upload is for. Also the documents row's doc_type,
// prefixed (DocTypeLoanAssistPrefix), so a document found on its own still
// says what it is.
const (
	LoanSlotAadhaarFront    = "AADHAAR_FRONT"
	LoanSlotAadhaarBack     = "AADHAAR_BACK"
	LoanSlotPAN             = "PAN"
	LoanSlotUdyam           = "UDYAM"
	LoanSlotElectricityBill = "ELECTRICITY_BILL"
	LoanSlotRentAgreement   = "RENT_AGREEMENT"
	LoanSlotNameBoard       = "NAME_BOARD"

	DocTypeLoanAssistPrefix = "LOAN_"
)

// LoanDocRule is one document a loan type asks for. Group joins slots that
// are one document to the customer (the two sides of the Aadhaar card count
// once in "5 of 5 added"). MaxAgeDays bounds how old a saved copy may be and
// still be reused ("a bill within 3 months"); 0 = no limit.
type LoanDocRule struct {
	Slot         string `json:"slot"`
	Group        string `json:"group"`
	Title        string `json:"title"`
	Hint         string `json:"hint"`
	OnlyIfRented bool   `json:"onlyIfRented,omitempty"`
	MaxAgeDays   int    `json:"maxAgeDays,omitempty"`
	// Tips are what a good copy looks like, shown where it is added.
	Tips []string `json:"tips"`
}

// LoanType is one kind of loan the customer can ask for help with, and the
// documents it needs. The app draws whatever this says, so a new loan type or
// a changed document list is a server change, not an app release.
type LoanType struct {
	Code     string        `json:"code"`
	Name     string        `json:"name"`
	Subtitle string        `json:"subtitle,omitempty"`
	Active   bool          `json:"active"`
	Docs     []LoanDocRule `json:"documents"`
	// DocsNote is the line under the document chips ("Rented premises also
	// need a rent agreement.").
	DocsNote string `json:"documentsNote,omitempty"`
}

const LoanTypeBusiness = "BUSINESS"

// LoanTypes is the catalog, in the order the screen lists it. Only Business
// loan is active; the others draw as "Coming soon".
var LoanTypes = []LoanType{
	{
		Code: LoanTypeBusiness, Name: "Business loan",
		Subtitle: "For business owners with Udyam registration", Active: true,
		DocsNote: "Rented premises also need a rent agreement.",
		Docs: []LoanDocRule{
			{Slot: LoanSlotAadhaarFront, Group: "AADHAAR", Title: "Aadhaar card", Hint: "Front",
				Tips: []string{"The whole card in the frame, all four corners visible", "Every detail readable, with no glare"}},
			{Slot: LoanSlotAadhaarBack, Group: "AADHAAR", Title: "Aadhaar card", Hint: "Back",
				Tips: []string{"The whole card in the frame, all four corners visible", "Every detail readable, with no glare"}},
			{Slot: LoanSlotPAN, Group: "PAN", Title: "PAN card", Hint: "Photo or PDF",
				Tips: []string{"The whole card in the frame", "Your name and PAN readable, with no glare"}},
			{Slot: LoanSlotUdyam, Group: "UDYAM", Title: "Udyam certificate", Hint: "Photo or PDF",
				Tips: []string{"The certificate with your Udyam number readable", "A PDF from the Udyam portal works too"}},
			{Slot: LoanSlotElectricityBill, Group: "ELECTRICITY_BILL", Title: "Electricity bill",
				Hint: "Latest bill of your business address", MaxAgeDays: 90,
				Tips: []string{"Bill dated within the last 3 months", "Address and bill date are clearly readable"}},
			{Slot: LoanSlotRentAgreement, Group: "RENT_AGREEMENT", Title: "Rent agreement",
				Hint: "Needed for rented premises", OnlyIfRented: true,
				Tips: []string{"The signed agreement for your business premises", "A PDF with every page works best"}},
			{Slot: LoanSlotNameBoard, Group: "NAME_BOARD", Title: "Business name board",
				Hint: "Framed board only. Banners are not accepted.",
				Tips: []string{"The full board and your business name are clearly visible", "Take it in daylight, from the front"}},
		},
	},
	{Code: "PERSONAL", Name: "Personal loan"},
	{Code: "HOME", Name: "Home loan"},
	{Code: "LAP", Name: "Loan against property"},
	{Code: "VEHICLE", Name: "Vehicle loan"},
}

// FindLoanType returns a loan type by code, or nil.
func FindLoanType(code string) *LoanType {
	for i := range LoanTypes {
		if LoanTypes[i].Code == code {
			return &LoanTypes[i]
		}
	}
	return nil
}

// Rule returns the loan type's rule for a slot, or nil when it does not ask
// for that document.
func (t *LoanType) Rule(slot string) *LoanDocRule {
	for i := range t.Docs {
		if t.Docs[i].Slot == slot {
			return &t.Docs[i]
		}
	}
	return nil
}

// RequiredSlots is every slot the loan type needs for these premises, in
// screen order.
func (t *LoanType) RequiredSlots(rented bool) []string {
	out := make([]string, 0, len(t.Docs))
	for _, r := range t.Docs {
		if r.OnlyIfRented && !rented {
			continue
		}
		out = append(out, r.Slot)
	}
	return out
}

// LoanAssistRequest is a loan_assist_requests row.
type LoanAssistRequest struct {
	ID                int64      `db:"id"`
	AccountID         int64      `db:"account_id"`
	Status            string     `db:"status"`
	LoanType          string     `db:"loan_type"`
	AmountNeeded      int64      `db:"amount_needed"`
	Rented            bool       `db:"rented"`
	ConsentAt         *time.Time `db:"consent_at"`
	ConsentText       *string    `db:"consent_text"`
	RequestCode       *string    `db:"request_code"`
	OrderID           *int64     `db:"order_id"`
	PaidAt            *time.Time `db:"paid_at"`
	AssignedPartnerID *int64     `db:"assigned_partner_id"`
	AssignedAt        *time.Time `db:"assigned_at"`
	DocsVerifiedAt    *time.Time `db:"docs_verified_at"`
	ExpertContactedAt *time.Time `db:"expert_contacted_at"`
	GuidanceAt        *time.Time `db:"guidance_at"`
	ClosedAt          *time.Time `db:"closed_at"`
	Notes             *string    `db:"notes"`
	TeamNotifiedAt    *time.Time `db:"team_notified_at"`
	CreatedAt         time.Time  `db:"created_at"`
	UpdatedAt         time.Time  `db:"updated_at"`
}

// Code is the request code, or "" for a draft.
func (r *LoanAssistRequest) Code() string {
	if r.RequestCode == nil {
		return ""
	}
	return *r.RequestCode
}

// LoanAssistDocument is one filled slot of an application: what the file is,
// what its partner made of it, and where a reused copy was accepted — never
// where the bytes live.
type LoanAssistDocument struct {
	RequestID     int64      `json:"-"                       db:"request_id"`
	Slot          string     `json:"slot"                    db:"slot"`
	DocumentID    int64      `json:"documentId"              db:"document_id"`
	FileName      string     `json:"fileName"                db:"filename"`
	MimeType      string     `json:"mimeType"                db:"mime_type"`
	SizeBytes     *int64     `json:"sizeBytes"               db:"size_bytes"`
	UploadedAt    time.Time  `json:"uploadedAt"              db:"uploaded_at"`
	ReviewState   string     `json:"reviewState"             db:"review_state"`
	ReviewNote    *string    `json:"reviewNote,omitempty"    db:"review_note"`
	ReviewedBy    *int64     `json:"-"                       db:"reviewed_by"`
	ReviewedAt    *time.Time `json:"reviewedAt,omitempty"    db:"reviewed_at"`
	ResubmittedAt *time.Time `json:"resubmittedAt,omitempty" db:"resubmitted_at"`
	// ReusedFrom is the request code of the application this copy was
	// accepted in, when it was reused rather than uploaded here.
	ReusedFrom *string `json:"reusedFrom,omitempty" db:"reused_from"`
	// Reviewer is who reviewed it, as the customer sees them ("Priya, Online
	// Support"). Filled by the service.
	Reviewer *PartnerCard `json:"reviewer,omitempty" db:"-"`
}

// SavedLoanDocument is a document a partner accepted in one of the account's
// earlier applications: what a new application may reuse.
type SavedLoanDocument struct {
	Slot        string    `json:"slot"        db:"slot"`
	DocumentID  int64     `json:"-"           db:"document_id"`
	FileName    string    `json:"fileName"    db:"filename"`
	UploadedAt  time.Time `json:"uploadedAt"  db:"uploaded_at"`
	VerifiedAt  time.Time `json:"verifiedAt"  db:"reviewed_at"`
	RequestID   int64     `json:"-"           db:"request_id"`
	RequestCode string    `json:"requestCode" db:"request_code"`
	// Stale is set by the service when the loan type bounds the age of this
	// document and the saved copy is older ("bill older than 3 months").
	Stale bool `json:"stale" db:"-"`
}

// PartnerCard is how a partner appears to a customer: on the application
// status card ("Vikram Rao · Offline Executive · Hyderabad") and beside a
// request to upload again.
type PartnerCard struct {
	AccountID int64   `json:"id"              db:"account_id"`
	Name      string  `json:"name"            db:"display_name"`
	Team      string  `json:"team"            db:"team"`
	City      *string `json:"city,omitempty"  db:"city"`
	CallPhone *string `json:"phone,omitempty" db:"call_phone"`
}

// PartnerOption is a partner an application can be assigned to.
type PartnerOption struct {
	AccountID int64   `json:"id"             db:"id"`
	Name      string  `json:"name"           db:"name"`
	Team      *string `json:"team,omitempty" db:"team"`
	City      *string `json:"city,omitempty" db:"city"`
	Role      string  `json:"role"           db:"role"`
}

// ReviewApplication is a row of the partners' review list.
type ReviewApplication struct {
	ID                int64      `json:"id"                     db:"id"`
	RequestCode       *string    `json:"requestCode"            db:"request_code"`
	Status            string     `json:"status"                 db:"status"`
	LoanType          string     `json:"loanType"               db:"loan_type"`
	AmountNeeded      int64      `json:"amountNeeded"           db:"amount_needed"`
	Rented            bool       `json:"rented"                 db:"rented"`
	PaidAt            *time.Time `json:"paidAt"                 db:"paid_at"`
	ConsentAt         *time.Time `json:"consentAt"              db:"consent_at"`
	AccountID         int64      `json:"accountId"              db:"account_id"`
	CustomerName      string     `json:"customerName"           db:"customer_name"`
	Phone             *string    `json:"phone"                  db:"primary_phone"`
	Email             *string    `json:"email"                  db:"primary_email"`
	AmountPaid        *float64   `json:"amountPaid"             db:"amount"`
	PaymentMode       *string    `json:"paymentMode"            db:"payment_mode"`
	AssignedPartnerID *int64     `json:"assignedPartnerId"      db:"assigned_partner_id"`
	AssignedTo        *string    `json:"assignedTo"             db:"assigned_name"`
	Notes             *string    `json:"notes"                  db:"notes"`
	DocCount          int        `json:"documentCount"          db:"doc_count"`
	PendingCount      int        `json:"pendingCount"           db:"pending_count"`
	RetakeCount       int        `json:"retakeCount"            db:"retake_count"`
	UpdatedAt         time.Time  `json:"updatedAt"              db:"updated_at"`

	// Documents is filled on the detail read only.
	Documents []LoanAssistDocument `json:"documents,omitempty" db:"-"`
	// CanOpen says whether the caller may open this application's documents
	// and see its contact details: an admin, or the partner assigned to it.
	CanOpen bool `json:"canOpen" db:"-"`
}
