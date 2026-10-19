-- 037: paper_analyses.instructions — the parent's free-text focus for one
-- parse (「重点分析第3题」). Nullable: the auto path (kid submission) has
-- no parent in the loop, so empty stays NULL and the stage prompts skip
-- the block entirely. The API echoes it back on the analysis row so the
-- report can show 「本次分析要求」.

ALTER TABLE paper_analyses ADD COLUMN instructions TEXT;
