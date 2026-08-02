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
