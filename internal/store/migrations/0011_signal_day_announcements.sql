-- The daily equivalent of signal_month_announcements: which puzzles the
-- bridge has already posted a recap for. A day is announced at most once,
-- whether the trigger was the last active player filing or the run just
-- after midnight, and this row is what stops the second of those from
-- repeating the first.
--
-- Keyed by puzzle number, which is the same identifier the results table
-- and the parser use, so there is no second notion of "which day" to keep
-- in step with wordle.PuzzleForDate. A separate table from the monthly one
-- rather than a shared "announcements" table with a kind column: the two
-- have different natural keys, and a shared table would have to allow the
-- wrong half of each row to be null.
CREATE TABLE signal_day_announcements (
    puzzle_no    INTEGER NOT NULL PRIMARY KEY,
    announced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
