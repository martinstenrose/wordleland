-- Every timestamp is stored in UTC, in SQLite's own form: 2026-09-23 03:29:14.
-- CURRENT_TIMESTAMP has always written that. A time bound from Go did not:
-- the driver wrote Go's own string for it, in the container's zone, as
--
--     2026-09-23 05:29:14.067 +0200 CEST
--
-- sometimes followed by " m=+123.456", Go's monotonic-clock reading. The
-- connection now writes the UTC form for those too (see store.dsn), and
-- this rewrites the values already stored the old way, in the columns a
-- Go time was ever bound into.
--
-- The old form is recognised by being longer than the 19 characters of the
-- UTC one. Its offset is the five characters after the first space past
-- the seconds (and their fraction, when there is one); the value is moved
-- by minus that many minutes, which also drops the fraction and anything
-- after the zone name. A value already in the UTC form is left alone, so
-- the result is the same instant in every row, spelled one way.
--
-- Each statement reads the same way from the inside out: v is the stored
-- value, rest is everything after the seconds, off is the "+0200", and the
-- outer select moves the first 19 characters by minus that offset.

UPDATE results SET posted_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT posted_at AS v, substr(posted_at, 20) AS rest)))
WHERE length(posted_at) > 19 AND substr(posted_at, 20, 1) IN ('.', ' ');

UPDATE pending_results SET posted_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT posted_at AS v, substr(posted_at, 20) AS rest)))
WHERE length(posted_at) > 19 AND substr(posted_at, 20, 1) IN ('.', ' ');

UPDATE sessions SET expires_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT expires_at AS v, substr(expires_at, 20) AS rest)))
WHERE length(expires_at) > 19 AND substr(expires_at, 20, 1) IN ('.', ' ');

UPDATE password_reset_tokens SET expires_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT expires_at AS v, substr(expires_at, 20) AS rest)))
WHERE length(expires_at) > 19 AND substr(expires_at, 20, 1) IN ('.', ' ');

UPDATE invitations SET expires_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT expires_at AS v, substr(expires_at, 20) AS rest)))
WHERE length(expires_at) > 19 AND substr(expires_at, 20, 1) IN ('.', ' ');

UPDATE api_tokens SET expires_at = (
    SELECT datetime(substr(v, 1, 19), printf('%+d minutes',
        -(CASE substr(off, 1, 1) WHEN '-' THEN -1 ELSE 1 END)
         * (CAST(substr(off, 2, 2) AS INTEGER) * 60 + CAST(substr(off, 4, 2) AS INTEGER))))
    FROM (SELECT v, substr(rest, instr(rest, ' ') + 1, 5) AS off
          FROM (SELECT expires_at AS v, substr(expires_at, 20) AS rest)))
WHERE length(expires_at) > 19 AND substr(expires_at, 20, 1) IN ('.', ' ');
