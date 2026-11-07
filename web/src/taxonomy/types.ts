// 学科知识工作区的契约类型（卡 TX-FE-FIX2）。
// 契约与后端实现逐一核对过（internal/api/taxonomy.go、internal/store/taxonomy.go、
// internal/taxonomy/engine.go 的 json tag 是事实源）；所有服务端返回的数组/嵌套
// 对象都标注可空，组件渲染时一律走防御分支（null → 空数组/空串）。
//
// 引擎侧 JSON 是驼峰 tag（Go json tag 原样序列化，不是 snake_case）：
//  - Summary: limitReached / limit / asked / weak / shaky / mastered / untested /
//    unconfirmed / roots / path / targets / finished；
//  - RootCause: pendingPrereqs / outOfScopePrereqs / externalPrereqs；
//  - PathItem: isRoot / isTarget。
// types 以真实契约为准（驼峰为主键），旧 snake_case 读法由组件层兜底兼容，
// 两个字段都在类型上保留（驼峰为主、snake 为可选兼容），避免后端改 tag 时静默漂移。
//
// ID 语义（按类型明确处理，不混用）：
//  - TopicRef / QuestionRef：字符串（上游稳定 external_ref，如 cnm_p001 / cnm_p001_q1）；
//  - SessionID / CourseID / AttemptID / ShareID：数字（服务端自增主键）。

/** 学科 key（开放 string：服务端 subjects 表驱动，运行时学科不由前端词表封死）。 */
export type SubjectKey = string;

/** GET /api/taxonomy/subjects 的一行。 */
export interface SubjectRow {
  id: SubjectKey;
  name: string;
  sb_code: string | null;
  topic_count: number;
  question_count: number;
  grades: number[] | null;
}

/** 知识点列表行（浏览/搜索结果）。
 *  真实 API 行：id 是 DB 自增整数（knowledge_tags.id，如 1），
 *  external_ref 才是寻址键（cnm_p001 —— 详情/练习/诊断契约都用它），
 *  后端 model.KnowledgeTag 序列化必带（is_system 导入行幂等键）；
 *  家长/AI 自建 tag 无 ref 时组件须禁用导航，绝不能拿整数 id 顶替。 */
export interface TopicRow {
  id: string;
  /** 上游稳定 id（cnm_p001）。所有 topic 选择路径必须传它，不得传本地整数 id。 */
  external_ref: string;
  name: string;
  grade: number | null;
  semester: string | null;
  domain: string | null;
  chapter: string | null;
  subject?: string | null;
  question_count?: number | null;
  description?: string | null;
  external?: boolean;
}

/** 知识点详情（GET /api/taxonomy/topics/{externalRef} 的 topic）。 */
export interface TopicDetail extends TopicRow {
  type?: string | null;
  mastery_criteria?: string[] | null;
  root_reason?: string | null;
}

/** 一条先修/被依赖边（详情接口返回，已带上对端概要）。
 *  对端是 model.KnowledgeTag 序列化行：id 为 DB 整数、external_ref 为
 *  寻址键（真数据必带）；external_ref 兼容位仅容旧 mock 形状。 */
export interface TopicEdge {
  id: string;
  external_ref?: string | null;
  name: string;
  strength: 'hard' | 'soft' | string;
  reason: string | null;
  grade: number | null;
  chapter: string | null;
}

/** 选择题选项。figure 是不可信内联 SVG 字符串，只经 SafeFigure 净化后渲染。 */
export interface QuestionChoice {
  key: string;
  text: string;
  figure?: string | null;
}

/** 题库一题。孩子视角 answer/accept 缺省（服务端剥除），家长视角带答案。 */
export interface QuestionRow {
  id: string;
  topic_id?: string | null;
  difficulty: 'basic' | 'standard' | 'challenge' | 'advanced' | string;
  type: 'numeric' | 'choice' | 'short' | 'self' | string;
  prompt: string;
  choices: QuestionChoice[] | null;
  answer?: string | null;
  accept?: string[] | null;
  explanation?: string | null;
  figure?: string | null;
  hide_choice_text?: boolean;
  unordered?: boolean;
  percent?: boolean;
}

/** 课标条目（详情接口 standards 数组元素）。 */
export interface StandardRow {
  id: string;
  confidence: string | null;
  note: string | null;
  text: string | null;
  section?: string | null;
  clause?: string | null;
}

/** GET /api/taxonomy/topics/{externalRef} 的完整响应。 */
export interface TopicDetailResponse {
  topic: TopicDetail | null;
  prerequisites: TopicEdge[] | null;
  dependents: TopicEdge[] | null;
  questions: QuestionRow[] | null;
  standards: StandardRow[] | null;
}

/** 诊断会话（列表与详情共用壳；mode 取值 diagnose|retest|confirm）。 */
export interface DiagnosticSession {
  id: number;
  user_id?: number | null;
  subject: SubjectKey | string;
  mode: 'target' | 'screen' | 'diagnose' | 'retest' | 'confirm' | string;
  status: 'active' | 'completed' | 'voided' | string;
  scope_json?: string | null;
  summary_json?: string | null;
  label?: string | null;
  created_at?: string | null;
  updated_at?: string | null;
  completed_at?: string | null;
  asked?: number | null;
  limit?: number | null;
  confirm?: number | null;
  weak_count?: number | null;
  shaky_count?: number | null;
  mastered_count?: number | null;
  untested_count?: number | null;
}

/** 诊断当前题（active 会话的 detail.question，服务端两段结构）。
 *  外层 {topic_id, attempt, question}，内层 question 才是题本体。
 *  这里拍平成一层：question 字段合并进顶层，渲染直接用。 */
export interface DiagnosticQuestion extends QuestionRow {
  topic_id: string;
  topic_name?: string | null;
  chapter?: string | null;
  attempt?: number | null;
  need_confirm?: number | null;
  reason?: string | null;
}

/** detail.question 的原始两段结构（拍平前的服务端形状）。 */
export interface DiagnosticQuestionEnvelope {
  topic_id: string;
  attempt?: number | null;
  question: QuestionRow | null;
}

/** 根因知识点（REST 统一 snake_case；camel 是引擎旧 tag 的兼容位，
 *  渲染前经 normalizeSummary 归一，组件只读 snake 字段）。 */
export interface RootCause {
  id: string;
  name?: string | null;
  pending_prereqs?: string[] | null;
  out_of_scope_prereqs?: string[] | null;
  external_prereqs?: string[] | null;
  /** 引擎驼峰 tag（limitReached 同族）的兼容位。 */
  pendingPrereqs?: string[] | null;
  outOfScopePrereqs?: string[] | null;
  externalPrereqs?: string[] | null;
}

/** 学习路径一项（snake_case 为主，camel 兼容位同上）。 */
export interface PathItem {
  id: string;
  name?: string | null;
  status: string;
  is_root?: boolean;
  is_target?: boolean;
  isRoot?: boolean | null;
  isTarget?: boolean | null;
  grade?: number | null;
}

/** 按章汇总一行。 */
export interface ChapterSummaryRow {
  key?: string | null;
  label: string | null;
  volume?: string | null;
  chapter_no?: string | null;
  chapter_name?: string | null;
  total: number;
  mastered: number;
  shaky: number;
  weak: number;
  untested: number;
  weak_ids?: string[] | null;
  shaky_ids?: string[] | null;
  untested_ids?: string[] | null;
}

/** 诊断终态汇总（detail.summary，REST 统一 snake_case）。 */
export interface DiagnosticSummary {
  mode?: string | null;
  targets?: string[] | null;
  asked?: number | null;
  limit?: number | null;
  finished?: boolean;
  limit_reached?: boolean;
  limitReached?: boolean | null;
  mastered?: string[] | null;
  shaky?: string[] | null;
  weak?: string[] | null;
  unconfirmed?: string[] | null;
  untested?: string[] | null;
  roots?: RootCause[] | null;
  path?: PathItem[] | null;
  chapters?: ChapterSummaryRow[] | null;
  names?: Record<string, string> | null;
}

/** 一条作答记录（detail.attempts 元素，答题记录可更正）。
 *  服务端字段（model.DiagnosticAttempt）：answer_json（孩子提交的
 *  原文）、seq、role、status；correct 是独立 verdict（manual/self 题
 *  为 null，不得当 false）。真实后端没有 result 字段——更正下拉的
 *  显示值用 attemptResult() 从 correct 派生，result 仅作旧 mock 兼容。 */
export interface DiagnosticAttempt {
  id: number;
  session_id?: number | null;
  seq?: number | null;
  topic_id?: string | null;
  question_id?: string | null;
  role?: 'probe' | 'confirm' | string | null;
  answer_json?: string | null;
  /** 兼容位：历史 UI 读法。 */
  answer?: string | null;
  prompt?: string | null;
  result?: 'correct' | 'wrong' | 'skip' | string | null;
  correct?: boolean | null;
  status?: 'answered' | 'voided' | 'corrected' | string | null;
  reference_answer?: string | null;
  created_at?: string | null;
}

/** GET /api/taxonomy/diagnostics/{id} 的响应（answers/undo/correct/finish/retest 同形）。
 *  question 是服务端两段结构 {topic_id, attempt, question}（拍平在 normalizeDetail）。 */
export interface DiagnosticDetail {
  session: DiagnosticSession | null;
  question?: DiagnosticQuestionEnvelope | null;
  summary?: DiagnosticSummary | null;
  attempts: DiagnosticAttempt[] | null;
}

/** 盲区清单一行（GET /api/taxonomy/blindspots 的 weak/shaky 元素）。
 *  真实后端 blindspotRow 的知识点引用字段是 ref（external ref），
 *  id 仅作兼容位：组件统一通过 blindspotRef(entry) 读取，
 *  重测（createDiagnostic targets）只能传后端可解的 ref，绝不为 undefined。 */
export interface BlindSpotEntry {
  id?: string | null;
  /** 后端 blindspotRow.ref（知识点 external ref）。 */
  ref?: string | null;
  tag_id?: number | null;
  name?: string | null;
  subject?: string | null;
  mastery?: number | null;
  correct_count?: number | null;
  total_count?: number | null;
  low_confidence?: boolean | null;
  status: 'weak' | 'shaky' | string;
  root?: boolean;
  chapter?: string | null;
  grade?: number | null;
  wrong_count?: number | null;
  hits?: number | null;
  last_at?: string | null;
  session_id?: number | null;
  /** 后端盲区行 external_ref 的兼容读法（与 ref 同源）。 */
  external_ref?: string | null;
  /** 根因标记（盲区行的 root_cause 列）。 */
  root_cause?: boolean | null;
  /** 来源诊断会话（盲区行的 last_diagnostic_id 列）。 */
  last_diagnostic_id?: number | null;
}

/** GET /api/taxonomy/blindspots 的响应。 */
export interface BlindspotsResponse {
  weak: BlindSpotEntry[] | null;
  shaky: BlindSpotEntry[] | null;
  mastered: BlindSpotEntry[] | string[] | null;
  by_chapter: ChapterSummaryRow[] | null;
}

/** POST /api/taxonomy/diagnostics/{id}/correct 的判定值（卡 TX-FE-CORRECT）。
 *  更正只上送独立 verdict（对/错/不会），不再携带 answer——
 *  服务端保留孩子提交的原答案并按 verdict 重放。 */
export type CorrectionVerdict = 'correct' | 'wrong' | 'skip';

/** POST /api/taxonomy/practice 的响应。
 *  verdict 是判题四值 correct|wrong|manual|self；correct 仅在确定性
 *  判题时有意义（manual/self 时 undefined，不得当作 false 渲染）。
 *  reference_answer/explanation 在 self/skip/judge 响应里下发
 *  （practice ask 永不下发答案，只在提交后回参考答案）。 */
export interface PracticeResult {
  correct?: boolean;
  verdict?: 'correct' | 'wrong' | 'manual' | 'self' | string;
  explanation?: string | null;
  mastery?: number | null;
  review_item_id?: number | null;
  reference_answer?: string | null;
  question?: QuestionRow | null;
}

/** POST /api/taxonomy/diagnostics 的请求体。 */
export interface CreateDiagnosticRequest {
  user_id?: number | null;
  mode: 'target' | 'screen';
  subject: SubjectKey | string;
  targets?: string[];
  /** screen 模式的筛查范围（后端 diagnosticsScope；grade 是 *int）。 */
  scope?: {
    grade?: number | null;
    semester?: string | null;
    domain?: string | null;
    chapter?: string | null;
  } | null;
  limit?: number | null;
  confirm?: number | null;
  seed?: string | null;
  avoid?: string[] | null;
}

/** POST /api/admin/taxonomy/courses/generate 的请求体与响应。 */
export interface GenerateCourseRequest {
  user_id: number;
  title: string;
  subject: SubjectKey | string;
  diagnostic_session_id?: number | null;
  topic_ids?: string[] | null;
}
export interface GenerateCourseResult {
  course_id: number;
}

/** POST /api/taxonomy/diagnostics/{id}/share 的响应（卡 TX-FE-RESTORE）。
 *  url 是前端路由页 /share/taxonomy/{token}（分享 UI 直接用）；
 *  token 与 share_id 供吊销等管理操作（DELETE /api/admin/taxonomy/shares/{share_id}）。 */
export interface ShareResult {
  share_id: number;
  url: string;
  token: string;
  expires_at: string | null;
}

/** 公开分享报告（GET /api/taxonomy/shared/{token}，无会话）。
 *  服务端保留 {report_kind, expires_at, snapshot} wrapper —— snapshot
 *  才是报告本体（session_id/subject/mode/created_at/summary），必须
 *  unwrap 后渲染，wrapper 不是扁平快照。 */
export interface SharedReport {
  report_kind?: string | null;
  expires_at?: string | null;
  snapshot?: SharedSnapshot | null;
}

/** 分享快照本体（SharedReport.snapshot）。字段宽松防御。 */
export interface SharedSnapshot {
  kind?: string | null;
  label?: string | null;
  subject?: string | null;
  user_name?: string | null;
  generated_at?: string | null;
  headline?: string | null;
  session_id?: number | null;
  mode?: string | null;
  created_at?: string | null;
  summary?: DiagnosticSummary | null;
  stats?: {
    asked?: number | null;
    weak?: number | null;
    shaky?: number | null;
    mastered?: number | null;
    untested?: number | null;
  } | null;
  roots?: (RootCause & { name?: string | null })[] | null;
  path?: PathItem[] | null;
  chapters?: ChapterSummaryRow[] | null;
  standards?: (StandardRow & { topic_ids?: string[] | null })[] | null;
  answers?: DiagnosticAttempt[] | null;
  weak?: (BlindSpotEntry | string)[] | null;
  shaky?: (BlindSpotEntry | string)[] | null;
}

/** 分享链接状态（服务端派生，冻结次序 revoked > expired > active：
 *  吊销后的链接即使同时过期也仍是 revoked）。 */
export type ShareStatus = 'active' | 'expired' | 'revoked';

/** GET /api/admin/taxonomy/shares?user_id= 的一行（卡 TX-PARENT-SHARE-PREREQ-API）。
 *  家长吊销 UI 的数据源：只有吊销需要的列——绝无 token/token_hash/
 *  snapshot 或孩子答案。id/owner_id/session_id 是服务端自增整数
 *  （session_id 可空）；时间是 RFC3339 字符串。 */
export interface ShareRow {
  id: number;
  owner_id: number;
  session_id: number | null;
  report_kind: string;
  expires_at: string | null;
  revoked_at: string | null;
  created_at: string;
  status: ShareStatus;
}

/** GET /api/admin/taxonomy/shares?user_id= 的响应（空列表是 []，非 null）。 */
export interface SharesPage {
  shares: ShareRow[] | null;
}

/** DELETE /api/admin/taxonomy/shares/{id} 的响应。 */
export interface RevokeShareResult {
  ok: boolean;
}

/** 先修边强度（042 边表冻结词表）。 */
export type PrerequisiteStrength = 'hard' | 'soft';

/** 先修边复核状态：rejected 行留在库里审计，只退出生效图（不删行）。 */
export type PrerequisiteReviewStatus = 'reviewed' | 'rejected';

/** PUT/PATCH /api/admin/taxonomy/prerequisites* 响应里的 edge。
 *  PUT 回全量（写入的 strength/reason/review_status）；PATCH 只回翻转
 *  （strength/reason 是 omitempty，通常缺省）。 */
export interface PrerequisiteEdgeResult {
  topic_id: string;
  prerequisite_id: string;
  strength?: PrerequisiteStrength;
  reason?: string;
  review_status: PrerequisiteReviewStatus;
}

/** PUT /api/admin/taxonomy/prerequisites 与 PATCH .../review 的共同响应：
 *  变更后的边 + 重建后生效图的边数。 */
export interface PrerequisiteMutationResult {
  edge: PrerequisiteEdgeResult;
  effective_edges: number;
}

/** POST /api/admin/taxonomy/import 的响应（重跑内置数据导入）。 */
export interface ImportResult {
  imported: number;
  skipped: number;
  counts?: TaxonomyCounts | null;
}

/** 导入/状态接口返回的计数（store.TaxonomyCounts）。 */
export interface TaxonomyCounts {
  topics: number;
  prerequisites: number;
  hard_edges: number;
  soft_edges: number;
  questions: number;
  standards: number;
  tag_standards: number;
}

/** POST /api/admin/taxonomy/import-learning 的响应（恢复学习数据）。
 *  与 pack import 同端点族但独立路径；imported/skipped 语义是恢复的
 *  诊断会话数。 */
export interface ImportLearningResult {
  imported: number;
  skipped: number;
}

/** 通用列表游标响应。 */
export interface TopicsPage {
  topics: TopicRow[] | null;
  next_cursor: string | null;
}

export interface SessionsPage {
  sessions: DiagnosticSession[] | null;
}

/** GET /api/taxonomy/graph 的节点（卡 TX-GRAPH-PARENT-UI）。
 *  id 是知识点 external_ref（寻址键，绝不是本地整数主键）；
 *  mastery 是解析目标（家长显式 user_id / 孩子会话身份）的真实 kc_mastery
 *  汇总，null = 没有掌握度记录 —— 与 0 是两个状态，渲染必须区分。 */
export interface GraphNode {
  id: string;
  name: string;
  subject: string;
  grade: number | null;
  domain: string;
  chapter: string;
  mastery: number | null;
}

/** GET /api/taxonomy/graph 的有向边：存储方向原样下发（topic_id 依赖
 *  prerequisite_id）；strength hard=硬先修 soft=软先修；
 *  review_status machine/reviewed（服务端已滤掉 rejected）。 */
export interface GraphEdge {
  topic_id: string;
  prerequisite_id: string;
  strength: 'hard' | 'soft' | string;
  reason: string;
  review_status: 'machine' | 'reviewed' | 'rejected' | string;
}

/** GET /api/taxonomy/graph 的查询参数（卡 TX-GRAPH-PARENT-UI）。
 *  root 必填（external_ref，如 cnm_p001）；depth 只允许 1/2；
 *  subject 是可选学科过滤（服务端只展开该学科邻居，root 自身豁免但
 *  与过滤矛盾会 400）；user_id 是 admin 显式指定的解析目标
 *  （孩子会话可省略，解析为自己）。 */
export interface GraphQuery {
  root: string;
  depth?: 1 | 2;
  subject?: string;
  user_id?: number;
}

/** GET /api/taxonomy/graph?root=&depth=&user_id= 的响应。
 *  nodes/edges 恒为数组（空为 []）；truncated=true 表示邻域超过
 *  服务端 250 节点上限被截断（看到的不是完整邻域）。 */
export interface GraphResponse {
  nodes: GraphNode[] | null;
  edges: GraphEdge[] | null;
  truncated: boolean;
}
