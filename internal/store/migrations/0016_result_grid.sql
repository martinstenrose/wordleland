-- grid is the coloured squares a result was shared with, as letters: one
-- row per guess, five of g (in place), y (elsewhere in the word) or n (not in
-- it), rows joined by "/". See wordle.Grid.
--
-- NULL means no grid is known, which is every result from before this
-- column, every result entered by hand, and any share whose squares did not
-- agree with its score. Nothing is backfilled: the squares were never kept,
-- and the chat history is not re-read to recover them.
--
-- The shape is checked where a grid arrives (ingest refuses one that does
-- not agree with the score), not here, where the check would have to
-- restate the score rules in SQL.
ALTER TABLE results ADD COLUMN grid TEXT;

-- A result held for an unclaimed sender carries its grid through the wait,
-- like its posting time.
ALTER TABLE pending_results ADD COLUMN grid TEXT;
