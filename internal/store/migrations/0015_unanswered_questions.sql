-- Questions asked of the Signal bot that it could not place.
--
-- The one place the group's own words are stored, and deliberately a small
-- one: the text of the question and when it was asked, nothing about who
-- asked it. It exists so the owner can see what people actually ask and add
-- a kind of answer for it; it is swept after thirty days because that is a
-- to-do list, not a record. No sender, so nothing here ties a person to
-- their words.
CREATE TABLE unanswered_questions (
    id       INTEGER PRIMARY KEY,
    question TEXT NOT NULL,
    asked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
