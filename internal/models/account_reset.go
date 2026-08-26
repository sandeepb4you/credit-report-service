package models

// AccountResetCounts is what an account has accumulated since it signed up —
// shown before a reset as a preview, and returned after one as a receipt.
//
// The point of showing it first is that the numbers are the warning: "3 reports,
// 1 paid order" reads very differently from a bare confirmation dialog, and an
// admin about to reset the wrong account is most likely to notice it here.
type AccountResetCounts struct {
	Reports           int  `json:"reports"`
	Orders            int  `json:"orders"`
	PaidOrders        int  `json:"paidOrders"`
	BankStatements    int  `json:"bankStatements"`
	CouponRedemptions int  `json:"couponRedemptions"`
	PrefillLookups    int  `json:"prefillLookups"`
	OTPChallenges     int  `json:"otpChallenges"`
	ActiveSessions    int  `json:"activeSessions"`
	HasKYCRecord      bool `json:"hasKycRecord"`
	HasProfileName    bool `json:"hasProfileName"`
	HasDateOfBirth    bool `json:"hasDateOfBirth"`
	HasReferralCredit bool `json:"hasReferralCredit"`
}

// AccountResetResult reports what a reset actually removed.
//
// PDFObjectURIs carries the stored report PDFs so the caller can delete them
// from object storage: the database rows are gone inside the transaction, and
// without this the encrypted files would outlive the reports they belong to.
type AccountResetResult struct {
	AccountID     int64              `json:"accountId"`
	Removed       AccountResetCounts `json:"removed"`
	PDFObjectURIs []string           `json:"-"`
	// TokenEpoch after the reset. Every access token minted before it is dead,
	// which is how the target lands back on the login screen.
	TokenEpoch int `json:"tokenEpoch"`
}
