-- The weekly equivalent of signal_day_announcements: which Monday-to-Sunday
-- weeks the bridge has already posted a recap for. Its own table for the
-- reason 0011 gives against a shared one.
--
-- Keyed by the week's Monday puzzle number, so a week is named in the same
-- space as a day and needs no second notion of "which week" — ISO week
-- numbers restart every January, and a Monday's puzzle never does.
CREATE TABLE signal_week_announcements (
    first_puzzle INTEGER NOT NULL PRIMARY KEY,
    announced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
