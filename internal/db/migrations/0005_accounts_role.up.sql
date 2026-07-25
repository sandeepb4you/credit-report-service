-- Add a role column to accounts for admin authorization.
--
-- Roles are not assigned via the API in this MVP; instead, accounts whose
-- email appears in the `auth.admin-emails` config list are auto-promoted to
-- 'admin' at signup-verify / login (see service.AuthService.applyAdminRole).
-- The role rides in the JWT as a custom claim checked by RequireRole.
ALTER TABLE accounts ADD COLUMN role VARCHAR(20) NOT NULL DEFAULT 'user';
