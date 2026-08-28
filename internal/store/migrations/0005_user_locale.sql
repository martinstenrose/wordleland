-- The language a person reads the app and their mail in.
--
-- It was a cookie only, which is enough for a browser and useless for a
-- message: a reset link or an invitation went out in the default language
-- whatever the recipient had chosen. A cookie also lives on one device,
-- and the account is the thing that has a language.
ALTER TABLE users ADD COLUMN locale TEXT NOT NULL DEFAULT 'en';

-- An invitation has no user yet, so it carries the choice made when it was
-- sent: the message itself has to be written in something, and the account
-- it creates starts in the same language rather than reverting.
ALTER TABLE invitations ADD COLUMN locale TEXT NOT NULL DEFAULT 'en';
