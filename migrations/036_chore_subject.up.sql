-- 036: a chore can name its school subject.
--
-- The media library (parent-side proof browser) groups uploaded proof by
-- subject — 数学/语文/英语/物理/化学, same vocabulary as error_items —
-- so "all math evidence this month" is one tap. The column is free text,
-- empty meaning "not classified": most household chores are not school
-- work, and a CHECK enum would only force junk values. The five canonical
-- values share the i18n labels of admin.analysis.subject_*.

ALTER TABLE chores ADD COLUMN subject TEXT NOT NULL DEFAULT '';
