-- posted_at is when a result was posted in the Signal group. NULL means it
-- was not — the web form, the CLI, an API token and the backfill all leave
-- it empty — or that nobody knows: rows the bridge wrote before results
-- carried their source (see the backfill below) stay NULL too, and a reader
-- must not take NULL for "entered by hand".
--
-- It records the first known posting and is never moved after that: a
-- re-post or a correction changes the score, not when the player first
-- posted. That is why the writer fills it with COALESCE rather than
-- overwriting it.
--
-- The time is Signal's, not the bridge's — when the server accepted the
-- message — so a bridge catching up after an outage still records when
-- people actually posted, and a phone with its clock wrong is not "first"
-- every day.
ALTER TABLE results ADD COLUMN posted_at TIMESTAMP;

-- A result held for an unclaimed sender is replayed into results when the
-- sender is claimed, so it has to carry the posting time through the wait.
ALTER TABLE pending_results ADD COLUMN posted_at TIMESTAMP;

-- History. Every result the bridge has filed since it started writing as the
-- application itself left a result.created row in the activity log naming
-- the puzzle and the source, and that log's own timestamp — bridge receipt
-- rather than Signal's server time, but seconds apart in steady state — is
-- the only record of when those results arrived. Rows from before that,
-- when the bridge posted over HTTP as a token holder with no recorded
-- source, are left NULL rather than guessed from the actor kind: a token
-- could have been a script.
--
-- The first use of SQLite's JSON functions in this schema. They are built
-- into the driver's SQLite, not an extension to load.
UPDATE results SET posted_at = (
    SELECT MIN(a.at) FROM activity_log a
    WHERE a.action = 'result.created'
      AND a.subject_type = 'result'
      AND a.subject_id = results.player_id
      AND json_extract(a.detail, '$.puzzle_no') = results.puzzle_no
      AND json_extract(a.detail, '$.via') = 'signal'
);
