-- Recovery codes: the way back in when the authenticator app is gone.
--
-- Without them the only remedy is the CLI's reset-2fa, which needs shell
-- access to the host. That is fine for the person who runs the server and
-- useless for everyone else in the group.
CREATE TABLE totp_recovery_codes (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER   NOT NULL REFERENCES users(id) ON DELETE CASCADE,

    -- Only the hash is stored, so a database read cannot mint a working
    -- code. SHA-256 rather than argon2id for the same reason as the reset
    -- tokens: these are generated, not chosen, and carry 80 bits of
    -- entropy, so there is no low-entropy guess to slow down.
    code_hash  TEXT      NOT NULL UNIQUE,

    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- Single use. Kept rather than deleted so the count of codes left is
    -- honest and a used code is distinguishable from one that never
    -- existed.
    used_at    TIMESTAMP
);

CREATE INDEX idx_recovery_codes_user ON totp_recovery_codes(user_id, used_at);
