-- Records which claimed identity wrote an automated result, so a later
-- reassignment can move exactly the results one identity produced without
-- touching results another identity (or a human) wrote for the same player.
-- Nullable and ON DELETE SET NULL: hand-entered results never set it, and no
-- code path deletes a player_identities row today, but a future one should
-- not be blocked by old history.
ALTER TABLE results ADD COLUMN identity_id INTEGER REFERENCES player_identities(id) ON DELETE SET NULL;
