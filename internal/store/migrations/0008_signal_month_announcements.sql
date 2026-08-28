-- What the Signal bridge has already told the group about. A month is
-- announced at most once: a restart replays nothing that reached the store
-- before it, and a back-dated result cannot reopen a month that already has
-- a row here, because the bridge never lets one through to begin with.
--
-- Keyed by the month itself rather than an autoincrementing id: the row's
-- existence is the whole fact being recorded, and the natural key is what
-- makes "has this month been announced" one lookup instead of a query with
-- a WHERE clause to get wrong.
CREATE TABLE signal_month_announcements (
    year         INTEGER NOT NULL,
    month        INTEGER NOT NULL CHECK (month BETWEEN 1 AND 12),
    announced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (year, month)
);
