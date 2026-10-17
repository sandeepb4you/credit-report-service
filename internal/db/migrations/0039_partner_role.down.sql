UPDATE accounts SET role = 'agent', updated_at = now() WHERE role = 'partner';
