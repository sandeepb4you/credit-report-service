package models

// Account roles. 'admin' is assigned via the auth.admin-emails config
// allowlist (auto-promoted at verify/login); 'agent' is granted by an admin.
// The role rides in the JWT as a claim.
const (
	RoleUser  = "user"
	RoleAgent = "agent" // referral partner: can issue coupon codes
	RoleAdmin = "admin"
)

// roleRank orders roles from least to most privileged. A role satisfies a
// requirement when its rank is >= the required rank, so gating a route on
// RoleUser also admits agents and admins.
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
	RoleUser:  10,
	RoleAgent: 50,
	RoleAdmin: 100,
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
)

// rolePerms lists the permissions each role adds on top of the role beneath
// it. Effective permissions are the union across every role at or below your
// rank — see PermissionsFor — so an entry here is written once and inherited
// upward rather than repeated for admin.
var rolePerms = map[string][]string{
	RoleUser: {},
	RoleAgent: {
		PermCouponCreate,
		PermCouponManage,
	},
	RoleAdmin: {
		PermKycVerify,
		PermAccountSetRole,
		PermAccountReset,
		PermCouponAdmin,
		PermLoanProviderManage,
		PermBankOfferingManage,
		PermPlanManage,
		// Deliberately not granted to agents: the report names every referred
		// user, and an agent seeing their own recruits would still be reading
		// other agents' rows out of the same query.
		PermReferralView,
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

// NormalizeRole maps the empty role to RoleUser. Tokens issued before the role
// claim existed carry no role, and accounts default to 'user' in the DB, so
// "" and "user" mean the same thing.
func NormalizeRole(role string) string {
	if role == "" {
		return RoleUser
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
