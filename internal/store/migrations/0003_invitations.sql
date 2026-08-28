-- Invitations, so an admin can ask somebody to claim a player rather than
-- creating a login and handing over a password.
--
-- The account is created when the invitation is accepted, not when it is
-- sent: an unaccepted invitation should leave nothing behind, and a login
-- that exists but has never been used is a credential nobody is watching.
CREATE TABLE invitations (
    id          INTEGER PRIMARY KEY,
    token_hash  TEXT    NOT NULL UNIQUE,
    email       TEXT    NOT NULL,

    -- The player the invitation claims. RESTRICT rather than CASCADE: a
    -- player with an invitation out is one somebody is about to become,
    -- and deleting them silently would strand the link.
    player_id   INTEGER NOT NULL REFERENCES players(id) ON DELETE RESTRICT,
    invited_by  INTEGER          REFERENCES users(id)   ON DELETE SET NULL,

    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at  TIMESTAMP NOT NULL,
    used_at     TIMESTAMP
);

CREATE INDEX invitations_player ON invitations(player_id);
CREATE INDEX invitations_email  ON invitations(email);
