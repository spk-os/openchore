-- 042 down: taxonomy 持久化结构完整回滚。
-- 全部为本迁移新建的对象（表/索引/列），按依赖逆序 drop；
-- 不触碰 028/031/032 的既有数据（knowledge_tags 扩列直接 DROP COLUMN，
-- 既有 AI tag 行与其错题绑定零影响）。

DROP TABLE IF EXISTS diagnostic_shares;
DROP TABLE IF EXISTS diagnostic_attempts;
DROP TABLE IF EXISTS diagnostic_sessions;
DROP TABLE IF EXISTS unit_knowledge_tags;
DROP TABLE IF EXISTS knowledge_tag_standards;
DROP TABLE IF EXISTS curriculum_standards;
DROP TABLE IF EXISTS knowledge_questions;
DROP TABLE IF EXISTS knowledge_tag_prerequisites;
DROP TABLE IF EXISTS subjects;

-- knowledge_tags 扩列（SQLite 3.35+ DROP COLUMN；modernc/sqlite v1.59 内置新于该版本）。
-- 028 建的列（name/subject/parent_id/code/sort_order/is_system）与 UNIQUE 约束原样保留。
DROP INDEX IF EXISTS idx_kt_parent;
DROP INDEX IF EXISTS idx_kt_subject_grade;
DROP INDEX IF EXISTS idx_kt_external_ref;
ALTER TABLE knowledge_tags DROP COLUMN aliases_json;
ALTER TABLE knowledge_tags DROP COLUMN mappings_json;
ALTER TABLE knowledge_tags DROP COLUMN standards_json;
ALTER TABLE knowledge_tags DROP COLUMN external_ref;
ALTER TABLE knowledge_tags DROP COLUMN source;
ALTER TABLE knowledge_tags DROP COLUMN diagnostic_question_json;
ALTER TABLE knowledge_tags DROP COLUMN mastery_criteria_json;
ALTER TABLE knowledge_tags DROP COLUMN description;
ALTER TABLE knowledge_tags DROP COLUMN topic_type;
ALTER TABLE knowledge_tags DROP COLUMN chapter;
ALTER TABLE knowledge_tags DROP COLUMN domain;
ALTER TABLE knowledge_tags DROP COLUMN semester;
ALTER TABLE knowledge_tags DROP COLUMN grade;

-- course_outcomes 的可空知识点关联列（032 的行数据不动）。
ALTER TABLE course_outcomes DROP COLUMN knowledge_tag_id;
