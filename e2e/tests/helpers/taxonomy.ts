// Shared API helpers for the 042 taxonomy e2e specs.
//
// The specs drive the real API (Go + SQLite) and only use Playwright for the
// UI assertions. Two contract details in the current tree are intentionally
// normalised here instead of guessed:
//   - GET /diagnostics/{id} currently nests the next question as
//     `question: {topic_id, attempt, question: {...}}`; the UI expects it
//     flattened. Normalise both shapes so the tests exercise the engine even
//     while the frontend/backend contract is being finished.
//   - Blindspot rows may expose the KC id as `ref` (backend) or `id`
//     (frontend type); tests read either.
//
// No business API is mocked. Every helper below hits the real server.
import { expect, type Page } from '@playwright/test';
import { authHeaders } from './setup';

export const ADMIN_ID = 1;

/** Deterministic math topics from the imported pack. */
export const TOPIC = {
  countToFive: 'cnm_p001',
  compareWithinFive: 'cnm_p002',
} as const;

export interface SeedUser {
  id: number;
  name: string;
  role: string;
}

export interface Choice {
  key: string;
  text: string;
}

export interface QuestionView {
  id: string;
  type: string;
  prompt: string;
  topic_id?: string | null;
  attempt?: number | null;
  choices?: Choice[] | null;
  answer?: string | null;
  accept?: string[] | null;
}

export interface SessionView {
  id: number;
  status: string;
  mode: string;
  subject: string;
  user_id?: number | null;
  label?: string | null;
  limit?: number | null;
}

export interface SummaryView {
  /** Engine summary (camelCase: finished/weak/roots/path...). */
  engine: Record<string, unknown>;
  /** camelCase → snake_case aliases read by the current UI contracts. */
  ui: Record<string, unknown>;
}

export interface DiagnosticView {
  session: SessionView;
  question: QuestionView | null;
  summary: SummaryView | null;
  attempts: Array<Record<string, unknown>>;
}

export interface TopicDetail {
  topic: (Record<string, unknown> & { id: string; name: string }) | null;
  prerequisites: Array<Record<string, unknown>> | null;
  dependents: Array<Record<string, unknown>> | null;
  questions: QuestionView[] | null;
  standards: Array<Record<string, unknown>> | null;
}

export interface ExportPayload {
  schema_version: number;
  // 导出文档的 pack 溯源戳（后端 learningExportPackVersion =
  // "cn-math-taxonomy@embedded-v1"）；导入端校验非空（taxonomy_learning.go
  // 400 "pack_version is required"）。payload 来自真实下载，本就含此字段。
  pack_version: string;
  // 导出文档刻意不写 target_user_id（model.LearningExport 的 omitempty，0 =
  // 文档自身 user）；恢复端点则强制要求显式目标（API 400 "target_user_id
  // is required"）。UI 正确先例是发送时补（client.ts adminImportLearning）。
  target_user_id?: number;
  exported_at: string;
  user: { id: number; name: string; role: string };
  // 后端 model.LearningMastery（internal/model/model.go:1300-1307）：external_ref
  // 是跨机稳定键（pack 级 id），SELECT 不含 knowledge_tags.name——不要凭空
  // 声明 name/tag_id 字段。
  mastery: Array<{
    external_ref: string;
    correct_count: number;
    total_count: number;
    mastery: number;
    low_confidence?: boolean;
    last_event_at?: string | null;
  }>;
  events: Array<Record<string, unknown>>;
  diagnostics: Array<{ id: number; status?: string; user_id?: number }>;
  attempts: Array<Record<string, unknown>>;
  review_items: Array<Record<string, unknown>>;
}

export interface ShareResult {
  share_id: number;
  url: string;
  token: string;
  expires_at?: string | null;
}

export interface CourseView {
  course: { id: number; user_id: number; status: string; title: string };
  units: Array<{
    id: number;
    title: string;
    position: number;
    prerequisite_unit_ids: number[];
    resources: Array<Record<string, unknown>>;
  }>;
  outcomes: Array<{ id: number; title: string; knowledge_tag_id?: number | null }>;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

/** The real family list (public endpoint; no auth headers needed). */
export async function findUser(page: Page, name: string): Promise<SeedUser> {
  const res = await page.request.get('/api/users');
  expect(res.ok(), `GET /api/users returned ${res.status()}`).toBeTruthy();
  const users = (await res.json()) as SeedUser[];
  const user = users.find((u) => u.name === name);
  if (!user) throw new Error(`seed user not found: ${name}`);
  return user;
}

/** Raw authenticated request (relative paths go through Playwright's baseURL). */
export async function apiRequest(
  page: Page,
  method: 'GET' | 'POST' | 'PUT' | 'DELETE',
  apiPath: string,
  userId = ADMIN_ID,
  data?: unknown,
) {
  return page.request.fetch(apiPath, {
    method,
    headers: await authHeaders(userId),
    ...(data === undefined ? {} : { data }),
  });
}

/** Authenticated request that must succeed; returns the parsed JSON body. */
export async function apiJson<T>(
  page: Page,
  method: 'GET' | 'POST' | 'PUT' | 'DELETE',
  apiPath: string,
  userId = ADMIN_ID,
  data?: unknown,
): Promise<T> {
  const res = await apiRequest(page, method, apiPath, userId, data);
  if (!res.ok()) {
    throw new Error(`${method} ${apiPath} -> ${res.status()} ${await res.text()}`);
  }
  return (await res.json()) as T;
}

/** Admin view of a topic: questions include `answer` (kids' view strips it). */
export function getTopicDetail(page: Page, externalRef: string): Promise<TopicDetail> {
  return apiJson<TopicDetail>(
    page,
    'GET',
    `/api/taxonomy/topics/${encodeURIComponent(externalRef)}`,
  );
}

/**
 * Static correct answers for the deterministic seed questions.
 *
 * Source of truth: `internal/taxonomy/data/questions.json` (seed pack). When
 * the seed questions change, update this table in the same change.
 *
 * These tables exist because the API deliberately strips `question.answer`
 * for *everyone* (see `internal/api/taxonomy.go`: `GetTopic` — "Bank
 * questions: answerless for everyone" — and `sessionDetail` with
 * `includeAnswers=false`). That anti-leak decision means the helpers below
 * must NOT rely on the runtime `answer` field: before this table,
 * `wrongAnswerOf` picked the first choice key ('A') as a "wrong" answer,
 * which silently *matched* seed questions whose correct key is 'A'.
 */
const CORRECT_ANSWERS: Record<string, string> = {
  // cnm_p001 (countToFive)
  cnm_p001_q1: '4',
  cnm_p001_q2: '3',
  cnm_p001_q3: '4',
  cnm_p001_q4: '4',
  // cnm_p002 (compareWithinFive)
  cnm_p002_q1: 'A',
  cnm_p002_q2: '2',
  cnm_p002_q3: 'A',
  cnm_p002_q4: '3',
};

/**
 * Guaranteed-wrong answers for the same seed questions. For choice questions
 * the key must exist among the choices and differ from the correct key; for
 * the grade-1 "within 5" numeric seed questions used by this suite '999' is
 * wrong (note: not a universal guarantee for any numeric question — the seed
 * bank does contain numeric answers like 999, e.g. cnm_p053_q2).
 */
const WRONG_ANSWERS: Record<string, string> = {
  // cnm_p001 (countToFive, all numeric within 5)
  cnm_p001_q1: '999',
  cnm_p001_q2: '999',
  cnm_p001_q3: '999',
  cnm_p001_q4: '999',
  // cnm_p002 (compareWithinFive)
  cnm_p002_q1: 'B',
  cnm_p002_q2: '999',
  cnm_p002_q3: 'B',
  cnm_p002_q4: '999',
};

/** A deterministic wrong answer for the engine (numeric/choice only in 042). */
export function wrongAnswerOf(question: QuestionView): string {
  const known = WRONG_ANSWERS[question.id];
  if (known !== undefined) return known;
  // Unknown numeric question: '999' is only safe for the within-5 seeds this
  // suite actually hits — it is not a universal "wrong for any numeric" pick.
  if (question.type === 'numeric') return '999';
  // Unknown choice question: fail loudly instead of guessing a key that may
  // be the correct one (the API no longer tells us which key is right).
  if (question.type === 'choice') {
    throw new Error(
      `unknown seed question ${question.id}: add it to WRONG_ANSWERS`,
    );
  }
  return '不对';
}

/** The correct answer (choice key for choice questions). */
export function correctAnswerOf(question: QuestionView): string {
  const known = CORRECT_ANSWERS[question.id];
  if (known !== undefined) return known;
  // Fallback chain kept for questions outside the seeded pack.
  if (question.answer) return question.answer;
  if (question.accept?.[0]) return question.accept[0];
  if (question.type === 'choice' && question.choices?.[0]) return question.choices[0].key;
  throw new Error(`question ${question.id} has no known answer`);
}

/** camelCase engine keys → snake_case keys the UI/API contracts read. */
const SUMMARY_ALIASES: Record<string, string> = {
  limitReached: 'limit_reached',
  pendingPrereqs: 'pending_prereqs',
  outOfScopePrereqs: 'out_of_scope_prereqs',
  externalPrereqs: 'external_prereqs',
  isRoot: 'is_root',
  isTarget: 'is_target',
};

function aliasKeys(raw: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...raw };
  for (const [from, to] of Object.entries(SUMMARY_ALIASES)) {
    if (!(to in out) && from in out) out[to] = raw[from];
  }
  return out;
}

function normaliseSummary(raw: Record<string, unknown>): SummaryView {
  const engine = raw;
  const ui = aliasKeys(raw);
  for (const key of ['roots', 'path']) {
    const rows = raw[key];
    if (Array.isArray(rows)) {
      ui[key] = rows.map((row) => {
        const rec = asRecord(row);
        return rec ? aliasKeys(rec) : row;
      });
    }
  }
  return { engine, ui };
}

/** Normalise the session detail shape (flat or currently nested question). */
export function normaliseDiagnostic(raw: unknown): DiagnosticView {
  const root = asRecord(raw);
  if (!root) throw new Error('diagnostic detail is not an object');

  const sessionRaw = asRecord(root.session);
  if (!sessionRaw) throw new Error('diagnostic detail has no session');
  const session: SessionView = {
    id: Number(sessionRaw.id),
    status: String(sessionRaw.status ?? ''),
    mode: String(sessionRaw.mode ?? ''),
    subject: String(sessionRaw.subject ?? ''),
    user_id: sessionRaw.user_id == null ? null : Number(sessionRaw.user_id),
    label: typeof sessionRaw.label === 'string' ? sessionRaw.label : null,
    limit: sessionRaw.limit == null ? null : Number(sessionRaw.limit),
  };
  if (!Number.isFinite(session.id) || session.id <= 0) {
    throw new Error(`diagnostic detail has an invalid session id: ${String(sessionRaw.id)}`);
  }

  let question: QuestionView | null = null;
  const questionWrap = asRecord(root.question);
  if (questionWrap) {
    const nested = asRecord(questionWrap.question);
    const q = nested ?? questionWrap;
    const id = typeof q.id === 'string' ? q.id : '';
    if (id) {
      question = {
        id,
        type: typeof q.type === 'string' ? q.type : '',
        prompt: typeof q.prompt === 'string' ? q.prompt : '',
        topic_id:
          typeof questionWrap.topic_id === 'string'
            ? questionWrap.topic_id
            : typeof q.topic_id === 'string'
              ? q.topic_id
              : null,
        attempt:
          typeof questionWrap.attempt === 'number'
            ? questionWrap.attempt
            : typeof q.attempt === 'number'
              ? q.attempt
              : null,
        choices: Array.isArray(q.choices) ? (q.choices as Choice[]) : null,
        answer: typeof q.answer === 'string' ? q.answer : null,
        accept: Array.isArray(q.accept) ? (q.accept as string[]) : null,
      };
    }
  }

  const summaryRaw = asRecord(root.summary);
  return {
    session,
    question,
    summary: summaryRaw ? normaliseSummary(summaryRaw) : null,
    attempts: Array.isArray(root.attempts)
      ? root.attempts.map(asRecord).filter((a): a is Record<string, unknown> => !!a)
      : [],
  };
}

export async function createDiagnostic(
  page: Page,
  userId: number,
  targetRef: string,
  options: { subject?: string; limit?: number; confirm?: number } = {},
): Promise<DiagnosticView> {
  const res = await apiRequest(page, 'POST', '/api/taxonomy/diagnostics', ADMIN_ID, {
    user_id: userId,
    mode: 'target',
    subject: options.subject ?? 'math',
    targets: [targetRef],
    limit: options.limit ?? 8,
    confirm: options.confirm ?? 2,
  });
  expect(res.status(), `create diagnostic failed: ${await res.text()}`).toBe(201);
  return normaliseDiagnostic(await res.json());
}

export async function answerNext(
  page: Page,
  sessionId: number,
  question: QuestionView,
  answer: string,
): Promise<DiagnosticView> {
  const res = await apiRequest(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/answers`,
    ADMIN_ID,
    { question_id: question.id, answer },
  );
  expect(res.ok(), `answer ${question.id} failed: ${res.status()} ${await res.text()}`).toBeTruthy();
  return normaliseDiagnostic(await res.json());
}

export async function undoDiagnostic(page: Page, sessionId: number): Promise<DiagnosticView> {
  const res = await apiRequest(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/undo`,
    ADMIN_ID,
    { count: 1 },
  );
  expect(res.ok(), `undo failed: ${res.status()} ${await res.text()}`).toBeTruthy();
  return normaliseDiagnostic(await res.json());
}

export async function finishDiagnostic(page: Page, sessionId: number): Promise<DiagnosticView> {
  const res = await apiRequest(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/finish`,
    ADMIN_ID,
  );
  expect(res.ok(), `finish failed: ${res.status()} ${await res.text()}`).toBeTruthy();
  return normaliseDiagnostic(await res.json());
}

export async function retestDiagnostic(page: Page, sessionId: number): Promise<DiagnosticView> {
  const res = await apiRequest(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/retest`,
    ADMIN_ID,
  );
  expect(res.status(), `retest failed: ${await res.text()}`).toBe(201);
  return normaliseDiagnostic(await res.json());
}

/** Parent verdict for POST /correct — overrides one historical attempt's result. */
export type Verdict = 'correct' | 'wrong' | 'skip';

export async function correctDiagnostic(
  page: Page,
  sessionId: number,
  attemptId: number,
  verdict: Verdict,
): Promise<DiagnosticView> {
  const res = await apiRequest(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/correct`,
    ADMIN_ID,
    { attempt_id: attemptId, verdict },
  );
  expect(res.ok(), `correct failed: ${res.status()} ${await res.text()}`).toBeTruthy();
  return normaliseDiagnostic(await res.json());
}

/**
 * Answer until the engine has no next question (the session limit). This is
 * the only path that leaves a completed session with a real `finished`
 * summary and no "next question" — a forced `POST /finish` keeps the engine
 * view of an unfinished run (see the DEF in the parent spec).
 */
export async function exhaustDiagnostic(
  page: Page,
  detail: DiagnosticView,
): Promise<DiagnosticView> {
  let current = detail;
  const limit = current.session.limit ?? 8;
  for (let i = 0; i < limit && current.question; i += 1) {
    current = await answerNext(
      page,
      current.session.id,
      current.question!,
      i === 0 ? wrongAnswerOf(current.question!) : '不会',
    );
  }
  return current;
}

/** Create a completed target diagnostic whose weak set contains targetRef. */
export async function createWeakFinishedDiagnostic(
  page: Page,
  userId: number,
  targetRef = TOPIC.compareWithinFive,
): Promise<DiagnosticView> {
  let detail = await createDiagnostic(page, userId, targetRef);
  detail = await exhaustDiagnostic(page, detail);
  if (detail.session.status !== 'completed') {
    detail = await finishDiagnostic(page, detail.session.id);
  }
  expect(detail.session.status).toBe('completed');
  expect(summaryIds(detail.summary, 'weak')).toContain(targetRef);
  return detail;
}

export interface DiagnosticsPage {
  sessions: Array<Record<string, unknown>>;
}

export function listDiagnostics(page: Page, userId: number): Promise<DiagnosticsPage> {
  return apiJson<DiagnosticsPage>(
    page,
    'GET',
    `/api/taxonomy/diagnostics?user_id=${userId}`,
  );
}

export interface BlindspotRow {
  id?: string;
  ref?: string;
  name?: string;
  subject?: string;
  tag_id?: number;
  mastery?: number;
}

export interface BlindspotsPage {
  weak: BlindspotRow[];
  shaky: BlindspotRow[];
  mastered: BlindspotRow[];
  by_chapter: Array<Record<string, unknown>>;
}

export function getBlindspots(page: Page, userId: number): Promise<BlindspotsPage> {
  return apiJson<BlindspotsPage>(
    page,
    'GET',
    `/api/taxonomy/blindspots?user_id=${userId}`,
  );
}

export function exportLearning(page: Page, userId: number): Promise<ExportPayload> {
  return apiJson<ExportPayload>(
    page,
    'GET',
    `/api/taxonomy/export?user_id=${userId}`,
  );
}

/**
 * Restore an export document (final contract: POST /api/admin/taxonomy/import).
 *
 * 恢复目标必须显式传：端点对 `TargetUserID <= 0` 返回 400
 * "target_user_id is required"（internal/api/taxonomy_learning.go
 * ImportLearningDocument），store 侧从不读取文档自身的 TargetUserID/User
 * （internal/store/learning_restore.go: "the document's own TargetUserID/User
 * fields are never consulted"）。导出文档刻意不带该字段，因此这里非变更地
 * 在发送副本上补 `target_user_id: targetUserId`（与 UI 先例
 * web/src/taxonomy/client.ts adminImportLearning 一致）。
 */
export function importLearning(
  page: Page,
  payload: ExportPayload,
  targetUserId: number,
): Promise<{
  imported: number;
  skipped: number;
  // per-section 统计（model.LearningRestoreResult）：顶层 imported 是五段
  // 总和（learning_restore.go:219），只有 diagnostics 段对应新增 session。
  mastery: { imported: number; skipped: number };
  events: { imported: number; skipped: number };
  diagnostics: { imported: number; skipped: number };
  attempts: { imported: number; skipped: number };
  review_items: { imported: number; skipped: number };
}> {
  return apiJson<{
    imported: number;
    skipped: number;
    mastery: { imported: number; skipped: number };
    events: { imported: number; skipped: number };
    diagnostics: { imported: number; skipped: number };
    attempts: { imported: number; skipped: number };
    review_items: { imported: number; skipped: number };
  }>(
    page,
    'POST',
    '/api/admin/taxonomy/import',
    ADMIN_ID,
    { ...payload, target_user_id: targetUserId },
  );
}

export function generateCourse(
  page: Page,
  userId: number,
  sessionId: number,
  title: string,
): Promise<{ course_id: number }> {
  return apiJson<{ course_id: number }>(
    page,
    'POST',
    '/api/admin/taxonomy/courses/generate',
    ADMIN_ID,
    { user_id: userId, title, subject: 'math', diagnostic_session_id: sessionId },
  );
}

export function getAdminCourse(page: Page, courseId: number): Promise<CourseView> {
  return apiJson<CourseView>(page, 'GET', `/api/admin/courses/${courseId}`);
}

export function createShare(page: Page, sessionId: number): Promise<ShareResult> {
  return apiJson<ShareResult>(
    page,
    'POST',
    `/api/taxonomy/diagnostics/${sessionId}/share`,
    ADMIN_ID,
    { kind: 'diagnosis' },
  );
}

export function revokeShare(page: Page, shareId: number): Promise<{ ok: boolean }> {
  return apiJson<{ ok: boolean }>(
    page,
    'DELETE',
    `/api/admin/taxonomy/shares/${shareId}`,
  );
}

/** String list field from a summary (snake_case alias first, engine key second). */
export function summaryIds(summary: SummaryView | null, key: string): string[] {
  const value = summary?.ui[key] ?? summary?.engine[key];
  return Array.isArray(value) ? value.filter((v): v is string => typeof v === 'string') : [];
}
