package models

// Account roles. The role is assigned via the auth.admin-emails config
// allowlist (auto-promoted at verify/login) and rides in the JWT as a claim.
const (
	RoleUser  = "user"
	RoleAdmin = "admin"
)

// roleRank orders roles from least to most privileged. A role satisfies a
// requirement when its rank is >= the required rank, so gating a route on
// RoleUser also admits admins.
//
// Ranks are spaced by 10 so an intermediate role (e.g. "support" at 50) can be
// slotted in without renumbering the existing ones. A role that is not in this
// map is unknown and satisfies nothing — checks fail closed.
var roleRank = map[string]int{
	RoleUser:  10,
	RoleAdmin: 100,
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
