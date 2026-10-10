-- Reference photo for a chore: the "this is what done looks like" example
-- photo a parent can attach so the kid sees the expected end state before
-- taking their completion photo, and the AI reviewer can compare the two
-- images side by side. Empty string means no reference photo (default).
ALTER TABLE chores ADD COLUMN reference_photo_url TEXT NOT NULL DEFAULT '';
