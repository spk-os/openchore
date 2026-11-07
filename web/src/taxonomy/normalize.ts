// 契约归一化（卡 TX-FE-FIX2）：把服务端返回的引擎 camelCase tag
// （RootCause.pendingPrereqs / PathItem.isRoot / Summary.limitReached）
// 归一成前端统一读的 snake_case，并把 detail.question 的两段结构
// {topic_id, attempt, question} 拍平成渲染层直接用的 DiagnosticQuestion。
//
// 归一是纯函数（可单测），所有入口（client 调用方）只做一次；
// 组件不感知原始 tag 形状，测试也不靠 helper 掩盖组件错误——
// normalizeDetail 处理的是「传输形状」，不是组件状态。

import type {
  BlindSpotEntry,
  DiagnosticAttempt,
  DiagnosticDetail,
  DiagnosticQuestion,
  DiagnosticQuestionEnvelope,
  DiagnosticSummary,
  PathItem,
  RootCause,
} from './types';

/** 根因归一：camel tag → snake（camel 值优先级低于 snake，两者都在时以 snake 为准）。 */
export function normalizeRootCause(r: RootCause): RootCause {
  return {
    ...r,
    pending_prereqs: r.pending_prereqs ?? r.pendingPrereqs ?? null,
    out_of_scope_prereqs: r.out_of_scope_prereqs ?? r.outOfScopePrereqs ?? null,
    external_prereqs: r.external_prereqs ?? r.externalPrereqs ?? null,
  };
}

/** 路径项归一：isRoot/isTarget → is_root/is_target。 */
export function normalizePathItem(p: PathItem): PathItem {
  return {
    ...p,
    is_root: p.is_root ?? p.isRoot ?? false,
    is_target: p.is_target ?? p.isTarget ?? false,
  };
}

/** summary 归一：limitReached → limit_reached（顺带归一 roots/path 深层）。 */
export function normalizeSummary(summary: DiagnosticSummary | null | undefined): DiagnosticSummary | null {
  if (!summary) return null;
  return {
    ...summary,
    limit_reached: summary.limit_reached ?? summary.limitReached ?? false,
    roots: (summary.roots ?? []).map(normalizeRootCause),
    path: (summary.path ?? []).map(normalizePathItem),
  };
}

/** detail.question 拍平：{topic_id, attempt, question:{...}} → 单层。 */
export function normalizeQuestion(
  q: DiagnosticQuestionEnvelope | null | undefined,
): DiagnosticQuestion | null {
  if (!q || !q.question) return null;
  return {
    ...q.question,
    topic_id: q.topic_id,
    attempt: q.attempt ?? null,
  };
}

/** detail 整体归一（summary + question 一次到位）。 */
export function normalizeDetail(detail: DiagnosticDetail): DiagnosticDetail {
  return {
    ...detail,
    summary: normalizeSummary(detail.summary),
    question: detail.question,
  };
}

/** 从 share url（/api/taxonomy/shared/{token}）提取 token。 */
export function tokenFromShareURL(url: string): string {
  const idx = url.lastIndexOf('/');
  return idx >= 0 ? url.slice(idx + 1) : url;
}

/** 盲区行的知识点引用：真实后端 blindspotRow 用 ref（external ref），
 *  兼容旧 mock/快照的 id；两处都缺时返回 null（该行禁用再测）。 */
export function blindspotRef(entry: BlindSpotEntry): string | null {
  const candidates = [entry.ref, entry.id];
  for (const c of candidates) {
    if (typeof c === 'string' && c.trim().length > 0) return c;
  }
  return null;
}

/** 作答记录的结果值：真实后端 DiagnosticAttempt 没有 result 字段，
 *  从独立 verdict correct 派生（true→correct / false→wrong / null→skip）；
 *  旧 mock/快照带 result 时优先读 result（correct 未定义才回退）。 */
export function attemptResult(a: DiagnosticAttempt): 'correct' | 'wrong' | 'skip' | '' {
  if (a.result) return a.result as 'correct' | 'wrong' | 'skip';
  if (a.correct === true) return 'correct';
  if (a.correct === false) return 'wrong';
  return 'skip';
}
