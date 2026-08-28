-- Initial schema. Every table from docs/PRD.md, including columns nothing
-- reads yet: the build order settles the identity model before thousands of
-- rows reference it, so the schema is written once and completely.
--
-- Foreign keys carry the lifecycle rules rather than leaving them to
-- application code. The rules are load-bearing enough that they should be
-- impossible to violate, not merely discouraged.

-- A login. Created by the CLI; there is no self-registration.
CREATE TABLE users (
    id                             INTEGER PRIMARY KEY,

    -- Opaque WebAuthn user handle, generated at creation even though passkeys
    -- are deferred. A sequential id would leak the user count to any
    -- relying party, and changing the strategy later invalidates credentials
    -- already registered against it.
    handle                         BLOB    NOT NULL UNIQUE,

    email                          TEXT    NOT NULL UNIQUE,
    email_verified_at              TIMESTAMP,

    -- A retired account: cannot log in, but its entered_by attributions stay
    -- intact. This exists because such a user cannot be deleted at all.
    disabled_at                    TIMESTAMP,

    password_hash                  TEXT    NOT NULL,

    -- A plain boolean, not an enum: "player" is not a global role (it is
    -- expressed by having a players row), and any future group-admin role
    -- belongs on group membership.
    is_admin                       BOOLEAN NOT NULL DEFAULT 0,

    -- AES-GCM, key from the environment. NULL until enrolment completes.
    totp_secret_encrypted          BLOB,
    -- Holds the secret between showing the QR code and the user proving a
    -- valid code, so a mis-scanned QR cannot lock anyone out. Never used for
    -- verification at login.
    totp_pending_secret_encrypted  BLOB,
    -- Last accepted TOTP time step, to reject replay inside the 30s window.
    totp_last_step                 INTEGER
);

-- Capabilities, kept out of users to keep that table narrow.
CREATE TABLE user_permissions (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    permission TEXT    NOT NULL
        CHECK (permission IN ('add_own_results', 'edit_own_results', 'view')),
    PRIMARY KEY (user_id, permission)
);

CREATE TABLE sessions (
    id           BLOB      PRIMARY KEY,
    user_id      INTEGER   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at   TIMESTAMP NOT NULL,
    -- Set when the password step succeeded but TOTP has not been verified. A
    -- session in this state grants access to nothing but the TOTP prompt.
    pending_totp BOOLEAN   NOT NULL DEFAULT 0
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);

-- A scoreboard entity, independent of whether a login exists.
CREATE TABLE players (
    id      INTEGER PRIMARY KEY,

    -- Stable identifier for the CLI (--player martin) and URLs (/p/martin).
    -- Display names are neither unique nor stable: the roster contains two
    -- members whose names differ by one letter, and anyone can change theirs.
    slug    TEXT    NOT NULL UNIQUE,

    name    TEXT    NOT NULL,

    -- NULL = no login, the common case initially. Linking a user is how
    -- self-report is granted. SET NULL on delete: unlinking a login from a
    -- scoreboard entity is normal, reversible, and loses nothing.
    --
    -- UNIQUE because ownership is an invariant: one account holding two
    -- players could self-report as two people. SQLite permits multiple NULLs
    -- in a unique column, so unlinked players are unaffected.
    user_id INTEGER UNIQUE REFERENCES users(id) ON DELETE SET NULL,

    -- Membership, not recency. False means the person has left the group.
    -- Admin-set and never derived: whether someone has quit is not inferable
    -- from a gap in results. Recency is computed's ranking eligibility.
    active  BOOLEAN NOT NULL DEFAULT 1,

    -- The shape requires, held here rather than only in Go so a future
    -- write path cannot introduce a slug that breaks a URL. Together these
    -- match ^[a-z0-9]+(-[a-z0-9]+)*$.
    CHECK (
        slug <> ''
        AND slug NOT GLOB '*[^a-z0-9-]*'
        AND slug NOT LIKE '-%'
        AND slug NOT LIKE '%-'
        AND slug NOT LIKE '%--%'
    )
);

-- Maps external identities to players, source-agnostic.
CREATE TABLE player_identities (
    id           INTEGER PRIMARY KEY,
    player_id    INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    source       TEXT    NOT NULL,

    -- The sender's stable id in that source. For Signal this is the account
    -- UUID (ACI), never sourceName: a profile name can be changed at any time,
    -- and two members can hold the same one. The UUID survives both profile
    -- name and phone number changes.
    external_id  TEXT    NOT NULL,

    -- Last sourceName seen, purely so a human can tell which UUID is whom.
    -- Never used for resolution, and deliberately not unique.
    display_hint TEXT,

    UNIQUE (source, external_id)
);
CREATE INDEX idx_player_identities_player_id ON player_identities(player_id);

-- Results from senders that resolve to no player yet. Holds the full payload
-- rather than a sighting count, so claiming an identity recovers everything
-- that arrived while it was unclaimed.
CREATE TABLE pending_results (
    source        TEXT      NOT NULL,
    external_id   TEXT      NOT NULL,
    display_hint  TEXT,

    puzzle_no     INTEGER   NOT NULL,
    solved        BOOLEAN   NOT NULL,
    guesses       INTEGER   CHECK (guesses BETWEEN 1 AND 6),
    hard_mode     BOOLEAN   NOT NULL DEFAULT 0,

    received_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    -- A repost of the same puzzle overwrites rather than accumulating.
    UNIQUE (source, external_id, puzzle_no),

    -- Mirrors the results constraint below, so a payload that could never be
    -- stored as a result cannot be held as a pending one either.
    CHECK ((solved = 1 AND guesses IS NOT NULL) OR (solved = 0 AND guesses IS NULL))
);

CREATE TABLE results (
    id         INTEGER PRIMARY KEY,
    puzzle_no  INTEGER NOT NULL,

    -- Derived from puzzle_no, in the configured local timezone.
    date       DATE    NOT NULL,

    -- CASCADE is why players are never hard-deleted: a delete would take the
    -- player's entire history with it. Retirement is active = false.
    player_id  INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,

    -- NULL = failed (X/6). SQLite evaluates NULL BETWEEN ... as NULL, which
    -- satisfies a CHECK, so this constrains 1-6 without excluding failures.
    guesses    INTEGER CHECK (guesses BETWEEN 1 AND 6),

    solved     BOOLEAN NOT NULL,
    hard_mode  BOOLEAN NOT NULL DEFAULT 0,

    -- NULL = written by a token or the bridge; set = entered by a human.
    --
    -- RESTRICT, never SET NULL. Ingest reads "entered_by IS NULL" as "written by a
    -- token, therefore safe for a token to overwrite", so nulling this on
    -- delete would convert every row a user entered into an overwritable one.
    -- Deleting an admin would silently arm the bridge to revert every
    -- correction they ever made, including the entire backfill. A user who has
    -- entered results therefore cannot be deleted; disabled_at retires them.
    entered_by INTEGER REFERENCES users(id) ON DELETE RESTRICT,

    UNIQUE (puzzle_no, player_id),

    -- The two representations must agree: solved carries a guess count,
    -- failed carries none. Not in the PRD's field list, but it makes the
    -- score model unrepresentable-if-wrong rather than merely
    -- documented. A missed day remains the absence of a row.
    CHECK ((solved = 1 AND guesses IS NOT NULL) OR (solved = 0 AND guesses IS NULL))
);
CREATE INDEX idx_results_player_id ON results(player_id);
CREATE INDEX idx_results_puzzle_no ON results(puzzle_no);

CREATE TABLE password_reset_tokens (
    -- SHA-256 of a random token; the plaintext exists only in the emailed
    -- link, so a database read cannot mint a working reset.
    token_hash TEXT      PRIMARY KEY,
    user_id    INTEGER   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMP NOT NULL,
    -- Single use; a consumed token is never valid again.
    used_at    TIMESTAMP
);
CREATE INDEX idx_password_reset_tokens_user_id ON password_reset_tokens(user_id);

CREATE TABLE api_tokens (
    id         INTEGER   PRIMARY KEY,
    -- So the operator can tell one token from another.
    label      TEXT      NOT NULL,
    -- Stored hashed, never plaintext.
    token_hash TEXT      NOT NULL UNIQUE,
    -- NULL = never expires.
    expires_at TIMESTAMP,
    -- How a token is revoked. Deleting the row is not an alternative:
    -- audit_log references tokens under RESTRICT, so a token that has done
    -- anything cannot be deleted. That is intended — revocation should
    -- preserve the record of what the token did.
    revoked_at TIMESTAMP
);

-- Append-only record of mutations. The admin view is deferred, but
-- events are recorded from v1: a log that starts being written the day its UI
-- is built has no history in it.
CREATE TABLE audit_log (
    id             INTEGER   PRIMARY KEY,
    at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,

    actor_kind     TEXT      NOT NULL
        CHECK (actor_kind IN ('admin', 'token', 'player', 'system')),

    -- Whichever applies; both NULL for system. RESTRICT for the same reason
    -- results.entered_by is: a trail that can be erased by deleting a row
    -- elsewhere is not a trail. This is also why a token is revoked by
    -- setting revoked_at rather than by deletion.
    actor_user_id  INTEGER   REFERENCES users(id)      ON DELETE RESTRICT,
    actor_token_id INTEGER   REFERENCES api_tokens(id) ON DELETE RESTRICT,

    action         TEXT      NOT NULL,
    subject_type   TEXT      NOT NULL,
    subject_id     INTEGER,

    -- JSON. On an overwrite this carries the previous value, which is what
    -- makes the log usable as a correction trail rather than a list of events.
    detail         TEXT,

    -- Exactly one actor, matching the kind. Each clause both requires the
    -- right column and forbids the other, so 'token' cannot arrive carrying a
    -- user id: an entry that names two actors describes neither.
    CHECK (
        (actor_kind = 'system' AND actor_user_id IS NULL AND actor_token_id IS NULL)
        OR (actor_kind = 'token' AND actor_token_id IS NOT NULL AND actor_user_id IS NULL)
        OR (actor_kind IN ('admin', 'player') AND actor_user_id IS NOT NULL AND actor_token_id IS NULL)
    )
);
CREATE INDEX idx_audit_log_at ON audit_log(at);
CREATE INDEX idx_audit_log_subject ON audit_log(subject_type, subject_id);

-- Single-row table for values an admin changes at runtime, which therefore
-- cannot live in the environment.
CREATE TABLE settings (
    -- The CHECK is what makes "single row" a property of the schema rather
    -- than a convention every caller has to honour.
    id         INTEGER PRIMARY KEY CHECK (id = 1),

    -- The read-only share link's path segment, generated on first
    -- startup and rotatable from the CLI.
    share_slug TEXT    NOT NULL
);
