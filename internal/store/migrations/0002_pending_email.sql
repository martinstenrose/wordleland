-- A change of address is not applied until the new one is confirmed
-- reachable, so it waits here rather than overwriting the address the
-- account currently signs in with. Confirming promotes it; abandoning it
-- leaves the account exactly as it was.
ALTER TABLE users ADD COLUMN pending_email TEXT;
