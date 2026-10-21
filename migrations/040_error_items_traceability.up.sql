-- 040: 错题可追溯（任务②）——error_items 补三条硬链：
--   chore_schedule_id  来自哪次任务（拍卷打卡挂的 schedule）
--   errored_date       错的是哪天的卷（completion_date，非解析时间）
--   chore_title        来源任务标题快照（schedule/chore 删了标题仍在）
-- 写侧 recordErrorItem 从 completion 一并落列；管理端错题本/孩子端错题
-- 卡按此显示「哪天 · 哪个任务错的」。ALTER ADD COLUMN 即可（新列可空，
-- 老行回填见下），028/035 的建表重建模式此处不需要。
--
-- 老行回填：有 completion 的错题从 chore_completions 取
-- completion_date 与 chore_schedule_id；标题经 chore_schedules→chores
-- join 取，取不到保持 NULL（UI 显示「试卷分析」回退）。

ALTER TABLE error_items ADD COLUMN chore_schedule_id INTEGER REFERENCES chore_schedules(id) ON DELETE SET NULL;
ALTER TABLE error_items ADD COLUMN errored_date DATE;
ALTER TABLE error_items ADD COLUMN chore_title TEXT;

UPDATE error_items
   SET errored_date = (SELECT completion_date FROM chore_completions cc WHERE cc.id = error_items.completion_id),
       chore_schedule_id = (SELECT chore_schedule_id FROM chore_completions cc WHERE cc.id = error_items.completion_id)
 WHERE completion_id IS NOT NULL;

UPDATE error_items
   SET chore_title = (SELECT c.title
                        FROM chore_schedules cs JOIN chores c ON c.id = cs.chore_id
                       WHERE cs.id = error_items.chore_schedule_id)
 WHERE chore_schedule_id IS NOT NULL;

CREATE INDEX idx_error_items_errored_date ON error_items(user_id, errored_date);
