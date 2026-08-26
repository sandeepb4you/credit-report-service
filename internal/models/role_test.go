package models

import "testing"

func TestRoleSatisfies(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
		ok   bool
	}{
		{"user meets user", RoleUser, RoleUser, true},
		{"admin meets admin", RoleAdmin, RoleAdmin, true},
		{"admin meets user", RoleAdmin, RoleUser, true},
		{"user does not meet admin", RoleUser, RoleAdmin, false},
		{"legacy empty role meets user", "", RoleUser, true},
		{"legacy empty role does not meet admin", "", RoleAdmin, false},
		{"unknown holder denied", "superuser", RoleUser, false},
		{"unknown requirement denied", RoleAdmin, "superuser", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RoleSatisfies(tt.got, tt.want); got != tt.ok {
				t.Errorf("RoleSatisfies(%q, %q) = %v, want %v", tt.got, tt.want, got, tt.ok)
			}
		})
	}
}

// ---- permissions ---------------------------------------------------------

// Permissions are inherited upward: an entry is written once at the role that
// introduces it, and every higher role gets it for free.
func TestHasPermission_InheritsUpward(t *testing.T) {
	tests := []struct {
		role string
		perm string
		want bool
	}{
		{RoleUser, PermCouponCreate, false},
		{RoleUser, PermKycVerify, false},

		{RoleAgent, PermCouponCreate, true},
		{RoleAgent, PermCouponManage, true},
		{RoleAgent, PermKycVerify, false},      // agents are not admins
		{RoleAgent, PermAccountSetRole, false}, // an agent must not mint more agents
		{RoleAgent, PermAccountReset, false},   // nor wipe somebody's paid reports
		{RoleAgent, PermCouponAdmin, false},    // only their own coupons

		{RoleAdmin, PermCouponCreate, true}, // inherited from agent
		{RoleAdmin, PermCouponManage, true},
		{RoleAdmin, PermCouponAdmin, true},
		{RoleAdmin, PermKycVerify, true},
		{RoleAdmin, PermAccountSetRole, true},
		{RoleAdmin, PermAccountReset, true},

		{RoleUser, PermAccountReset, false},
	}
	for _, tt := range tests {
		if got := HasPermission(tt.role, tt.perm); got != tt.want {
			t.Errorf("HasPermission(%q, %q) = %v, want %v", tt.role, tt.perm, got, tt.want)
		}
	}
}

// A legacy token carries no role and must be treated as a plain user, not as
// something that slips past a permission gate.
func TestHasPermission_LegacyEmptyRoleIsUser(t *testing.T) {
	if HasPermission("", PermCouponCreate) {
		t.Error("empty role must not carry the agent permission")
	}
	if len(PermissionsFor("")) != len(PermissionsFor(RoleUser)) {
		t.Error("empty role must resolve to exactly the user permission set")
	}
}

// Unknown roles and unknown permissions both deny — this gate fails closed.
func TestHasPermission_FailsClosed(t *testing.T) {
	if HasPermission("superuser", PermCouponCreate) {
		t.Error("unknown role must have no permissions")
	}
	if HasPermission(RoleAdmin, "coupon:obliterate") {
		t.Error("unknown permission must not be granted, even to admin")
	}
	if HasPermission(RoleAdmin, "") {
		t.Error("empty permission must never be granted")
	}
	if PermissionsFor("superuser") != nil {
		t.Error("unknown role must have a nil permission set")
	}
}

func TestPermissionsFor_AdminIsSupersetOfAgent(t *testing.T) {
	agent := PermissionsFor(RoleAgent)
	admin := PermissionsFor(RoleAdmin)
	if len(agent) == 0 {
		t.Fatal("agent should have permissions")
	}
	for p := range agent {
		if !admin[p] {
			t.Errorf("admin is missing agent permission %q", p)
		}
	}
	if len(admin) <= len(agent) {
		t.Error("admin should carry strictly more permissions than agent")
	}
}

func TestRoleRank_AdminOutranksUser(t *testing.T) {
	user, ok := RoleRank(RoleUser)
	if !ok {
		t.Fatal("RoleUser has no rank")
	}
	admin, ok := RoleRank(RoleAdmin)
	if !ok {
		t.Fatal("RoleAdmin has no rank")
	}
	if admin <= user {
		t.Errorf("admin rank %d must exceed user rank %d", admin, user)
	}
}

func TestRoleRank_Unknown(t *testing.T) {
	if _, ok := RoleRank("superuser"); ok {
		t.Error("unknown role must not have a rank")
	}
}

func TestNormalizeRole(t *testing.T) {
	if got := NormalizeRole(""); got != RoleUser {
		t.Errorf("NormalizeRole(\"\") = %q, want %q", got, RoleUser)
	}
	if got := NormalizeRole(RoleAdmin); got != RoleAdmin {
		t.Errorf("NormalizeRole(%q) = %q, want unchanged", RoleAdmin, got)
	}
}
