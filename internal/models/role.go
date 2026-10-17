package models

// Account roles. 'admin' is assigned via the auth.admin-emails config
// allowlist (auto-promoted at verify/login); 'partner' is granted by an admin.
// The role rides in the JWT as a claim.
const (
	RoleUser = "user"
	// RolePartner issues coupon codes and works Loan Assist applications (the
	// offline executive and the online support team). It was called 'agent'
	// until migration 0039; see NormalizeRole for tokens issued before it.
	RolePartner = "partner"
	RoleAdmin   = "admin"

	// roleAgentLegacy is the pre-0039 name of RolePartner, still carried by
	// access tokens issued before the rename until they expire.
	roleAgentLegacy = "agent"
)

// roleRank orders roles from least to most privileged. A role satisfies a
// requirement when its rank is >= the required rank, so gating a route on
// RoleUser also admits partners and admins.
//
// Ranks are spaced by 10 so an intermediate role can be slotted in without
// renumbering the existing ones. A role that is not in this map is unknown and
// satisfies nothing — checks fail closed.
//
// This assumes a total order, which holds while every role is a superset of
// the one below it. The first genuinely disjoint role (say "support" that can
// read tickets but not issue coupons) breaks that assumption; at that point
// drop the rank comparison and match on permission sets alone, which is
// already how routes are gated — see PermissionsFor.
var roleRank = map[string]int{
	RoleUser:    10,
	RolePartner: 50,
	RoleAdmin:   100,
}

// Permissions are the capabilities routes gate on. Gating on a permission
// rather than a role name means adding or re-scoping a role never touches a
// route declaration, and it is what lets the role -> permission mapping move
// into the database later without changing any handler.
const (
	PermKycVerify          = "kyc:verify"           // mark another account's PAN verified
	PermAccountSetRole     = "account:set-role"     // grant/revoke roles
	PermAccountReset       = "account:reset"        // wipe an account back to signup
	PermCouponCreate       = "coupon:create"        // issue a coupon code
	PermCouponManage       = "coupon:manage"        // list/revoke your own coupons
	PermCouponAdmin        = "coupon:admin"         // see and revoke anyone's coupons
	PermLoanProviderManage = "loan-provider:manage" // curate loan providers + switch settings
	PermBankOfferingManage = "bank-offering:manage" // curate score-builder bank offerings
	PermPlanManage         = "plan:manage"          // price and retire the purchasable plans
	PermReferralView       = "referral:view"        // read the referral graph across accounts
	// Review the manual-payout queue: see every pending withdrawal with the
	// full bank destination, and mark requests paid or rejected. Own
	// permission rather than referral:view — reviewing PANs, reading the
	// referral graph and moving money are three different jobs even when one
	// person does all three.
	PermWithdrawalReview = "withdrawal:review"
	// Reprice the referral programme (reward per conversion, minimum
	// withdrawal). Pricing, not payouts: changing what the programme promises
	// is a different job from working the payout queue.
	PermReferralManage = "referral:manage"
	// Read the operator's customer list: every account with its contact
	// details, latest score, whether it has paid and what it has referred.
	// Own permission rather than kyc:verify, for referral:view's reason —
	// reviewing one PAN at a time is a different job from reading the whole
	// customer book, and this one carries unmasked phone numbers.
	PermAccountView = "account:view"
	// Read every purchase across every account: what was bought, by whom, for
	// how much, and the totals. Own permission rather than account:view —
	// the customer book and the revenue line are different things to hand
	// someone, even though both carry contact details.
	PermOrderView = "order:view"
	// Run the credit report a customer paid for but did not receive: a billed
	// bureau call made on their behalf, spending their purchase. Its own
	// permission because it acts, where order:view only reads.
	PermReportRun = "report:run"
	// Correct a customer's profile name from the console. A write, so not part
	// of account:view; the name is what the credit bureau is sent.
	PermAccountEdit = "account:edit"
	// Open a read-only view of a customer's app ("View as user"). Its own
	// permission because it shows everything the customer sees — reports,
	// purchases, bank details — not the summary account:view lists.
	PermAccountImpersonate = "account:impersonate"
	// Build, import and export console dashboards. Only the right to have
	// dashboards: every widget's figures are still gated on the permission
	// that owns that data (signups on account:view, revenue on order:view…),
	// so this never lets anyone read more than they already could.
	PermDashboardManage = "dashboard:manage"
)

// PermNotificationSend sends a push notification to a customer's devices. Its
// own permission rather than account:edit: writing to someone's lock screen in
// myScorr's name is a different authority from correcting their profile.
const (
	PermNotificationSend = "notification:send"
)

// PermAudienceExport reads the retargeting lists and downloads them as CSVs
// for a WhatsApp, SMS, email or push campaign. Its own permission rather than
// account:view: reading the customer book one screen at a time is a different
// thing from taking it out of the system in bulk, sorted by who to message.
const (
	PermAudienceExport = "audience:export"
)

// Loan Assist. Applications carry Aadhaar copies, which UIDAI expects to be
// open only to the team assigned to them, so access is two permissions:
//
//   - loan-assist:review (partners): see the applications assigned to you and
//     the unassigned ones, take one, review its documents (accept, or ask the
//     customer for one again), move it through its stages. Documents open only
//     on applications assigned to you.
//   - loan-assist:manage (admins): every application, every document, and
//     assigning any application to any partner.
const (
	PermLoanAssistReview = "loan-assist:review"
	PermLoanAssistManage = "loan-assist:manage"
)

// rolePerms lists the permissions each role adds on top of the role beneath
// it. Effective permissions are the union across every role at or below your
// rank — see PermissionsFor — so an entry here is written once and inherited
// upward rather than repeated for admin.
var rolePerms = map[string][]string{
	RoleUser: {},
	RolePartner: {
		PermCouponCreate,
		PermCouponManage,
		PermLoanAssistReview,
	},
	RoleAdmin: {
		PermKycVerify,
		PermAccountSetRole,
		PermAccountReset,
		PermCouponAdmin,
		PermLoanProviderManage,
		PermBankOfferingManage,
		PermPlanManage,
		// Deliberately not granted to partners: the report names every referred
		// user, and a partner seeing their own recruits would still be reading
		// other partners' rows out of the same query.
		PermReferralView,
		PermWithdrawalReview,
		PermReferralManage,
		PermAccountView,
		PermOrderView,
		PermReportRun,
		PermAccountEdit,
		PermAccountImpersonate,
		PermNotificationSend,
		PermDashboardManage,
		PermAudienceExport,
		PermLoanAssistManage,
	},
}

// PermissionsFor returns the effective permission set for a role: everything
// granted at its own rank plus everything granted below it. An unknown role
// gets nothing.
func PermissionsFor(role string) map[string]bool {
	rank, ok := RoleRank(role)
	if !ok {
		return nil
	}
	out := make(map[string]bool)
	for r, rRank := range roleRank {
		if rRank > rank {
			continue
		}
		for _, p := range rolePerms[r] {
			out[p] = true
		}
	}
	return out
}

// HasPermission reports whether a role carries a permission. Unknown roles and
// unknown permissions both deny — this is the check routes depend on, so it
// fails closed on anything it does not recognize.
func HasPermission(role, perm string) bool {
	if perm == "" {
		return false
	}
	return PermissionsFor(role)[perm]
}

// NormalizeRole maps the empty role to RoleUser and the legacy 'agent' to
// RolePartner. Tokens issued before the role claim existed carry no role, and
// accounts default to 'user' in the DB, so "" and "user" mean the same thing;
// tokens issued before migration 0039 still say 'agent' until they expire.
func NormalizeRole(role string) string {
	switch role {
	case "":
		return RoleUser
	case roleAgentLegacy:
		return RolePartner
	}
	return role
}

// RoleRank returns the privilege rank of a role. The bool is false for roles
// outside the known set.
func RoleRank(role string) (int, bool) {
	rank, ok := roleRank[NormalizeRole(role)]
	return rank, ok
}

// RoleSatisfies reports whether an account holding role `got` meets a
// requirement for role `want`. It is a rank comparison, not equality: an admin
// satisfies a RoleUser requirement. An unknown role on either side denies.
func RoleSatisfies(got, want string) bool {
	gotRank, ok := RoleRank(got)
	if !ok {
		return false
	}
	wantRank, ok := RoleRank(want)
	if !ok {
		return false
	}
	return gotRank >= wantRank
}
