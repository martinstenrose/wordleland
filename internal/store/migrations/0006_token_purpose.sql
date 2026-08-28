-- password_reset_tokens carries two kinds of link: a password reset and an
-- address confirmation. They are the same shape -- single-use, short-lived,
-- hash-stored, tied to a user -- so one table holds both. Without a purpose,
-- one issued for either is spendable at the other: a confirmation link would
-- set a password, and a reset link would confirm an address.
--
-- Neither is an escalation as things stood, since a reset link only ever goes
-- to the address already on the account. But it was a shared token space held
-- apart by circumstance rather than by the schema, and circumstance changes.
ALTER TABLE password_reset_tokens
    ADD COLUMN purpose TEXT NOT NULL DEFAULT 'reset'
    CHECK (purpose IN ('reset', 'verify'));

-- Rows already issued keep the default. Both kinds are live for at most an
-- hour, so any confirmation link that predates this migration expires on its
-- own rather than being mislabelled for long; the alternative, guessing which
-- was which from a column that does not exist, is not available.
