// 学科知识工作区的独立 API 客户端（卡 TX-FE-CODER）。
//
// 与 web/src/api.ts 的关系：那是家务/课程/错题主客户端（HttpOnly cookie 会话）；
// 这里只覆盖 /api/taxonomy/** 与 /api/admin/taxonomy/** 的冻结契约。认证沿用同源
// cookie（credentials: 'same-origin'），并兼容旧约定里的 localStorage/sessionStorage
// `oc.token`（存在则作 Bearer 前缀补发）。错误统一 JSON {error}，带 HTTP status。
//
// 不引第三方依赖；数字 ID（session/course/attempt）与字符串 ID（external_ref）
// 在每个方法签名上明确区分。

import type {
  BlindspotsResponse,
  CorrectionVerdict,
  CreateDiagnosticRequest,
  DiagnosticDetail,
  GenerateCourseRequest,
  GenerateCourseResult,
  GraphQuery,
  ImportLearningResult,
  ImportResult,
  PracticeResult,
  PrerequisiteMutationResult,
  PrerequisiteReviewStatus,
  PrerequisiteStrength,
  RevokeShareResult,
  SessionsPage,
  ShareResult,
  SharedReport,
  SharesPage,
  SubjectRow,
  TopicDetailResponse,
  TopicsPage,
  GraphResponse,
} from './types';

const API_BASE = '/api';

/** taxonomy 客户端错误：服务端 JSON {error} 文本 + HTTP 状态码。 */
export class TaxonomyAPIError extends Error {
  status: number;
  data: unknown;
  constructor(message: string, status: number, data: unknown) {
    super(message);
    this.name = 'TaxonomyAPIError';
    this.status = status;
    this.data = data;
  }
}

/** 旧约定里的 Bearer token（photo-upload 等跨设备流程遗留键名）。 */
function storedToken(): string | null {
  try {
    if (typeof localStorage !== 'undefined') {
      const v = localStorage.getItem('oc.token');
      if (v) return v;
    }
    if (typeof sessionStorage !== 'undefined') {
      const v = sessionStorage.getItem('oc.token');
      if (v) return v;
    }
  } catch {
    // 隐私模式/测试环境禁 storage：回落到 cookie。
  }
  return null;
}

/** 允许测试注入 fetch；默认原生 fetch。 */
export let fetchImpl: typeof fetch = (...args) => fetch(...args);
export function setFetchImpl(f: typeof fetch): void {
  fetchImpl = f;
}

interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  body?: unknown;
  /** 公开快照端点也无会话要求，但默认仍带 cookie。 */
  signal?: AbortSignal;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  const token = storedToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const resp = await fetchImpl(`${API_BASE}${path}`, {
    method: opts.method ?? 'GET',
    headers,
    credentials: 'same-origin',
    ...(opts.body !== undefined ? { body: JSON.stringify(opts.body) } : {}),
    ...(opts.signal ? { signal: opts.signal } : {}),
  });
  if (resp.status === 204) return {} as T;
  const text = await resp.text();
  let data: unknown = null;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = null;
    }
  }
  if (!resp.ok) {
    const errMsg =
      data &&
      typeof data === 'object' &&
      'error' in data &&
      typeof (data as { error: unknown }).error === 'string'
        ? (data as { error: string }).error
        : `请求失败（HTTP ${resp.status}）`;
    throw new TaxonomyAPIError(errMsg, resp.status, data);
  }
  return (data ?? {}) as T;
}

/** 组装 query（跳过 null/undefined/空数组）。 */
function qs(
  params: Record<string, string | number | undefined | null>,
): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue;
    parts.push(`${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`);
  }
  return parts.length ? `?${parts.join('&')}` : '';
}

export interface TopicListQuery {
  subject?: string;
  grade?: string | number;
  q?: string;
  domain?: string;
  cursor?: string;
  limit?: number;
}

export const taxonomyApi = {
  /** 十学科浏览。 */
  subjects: () =>
    request<{ subjects: SubjectRow[] | null }>('/taxonomy/subjects'),

  /** 知识点浏览/搜索（游标分页）。 */
  topics: (query: TopicListQuery = {}) =>
    request<TopicsPage>(`/taxonomy/topics${qs({ ...query })}`),

  /** 知识点详情：先修/被依赖/题目/课标一次拿全。 */
  topicDetail: (externalRef: string) =>
    request<TopicDetailResponse>(
      `/taxonomy/topics/${encodeURIComponent(externalRef)}`,
    ),

  /** 题库练习：一次作答一次判定（服务端判分并回写）。 */
  practice: (body: {
    user_id?: number | null;
    topic_id: string;
    question_id: string;
    answer: string;
  }) => request<PracticeResult>('/taxonomy/practice', { method: 'POST', body }),

  /** 发起诊断（target 单点/整章；screen 整年级筛查）。 */
  createDiagnostic: (body: CreateDiagnosticRequest) =>
    request<DiagnosticDetail>('/taxonomy/diagnostics', {
      method: 'POST',
      body,
    }),

  /** 诊断历史列表。 */
  diagnostics: (userId?: number) =>
    request<SessionsPage>(`/taxonomy/diagnostics${qs({ user_id: userId })}`),

  /** 会话详情（active 带当前题；completed 带 summary 与 attempts）。 */
  diagnosticDetail: (sessionId: number) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}`),

  /** 提交一题答案。 */
  answerDiagnostic: (
    sessionId: number,
    body: { question_id: string; answer: string },
  ) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}/answers`, {
      method: 'POST',
      body,
    }),

  /** 撤销上一题。 */
  undoDiagnostic: (sessionId: number, count = 1) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}/undo`, {
      method: 'POST',
      body: { count },
    }),

  /** 更正历史作答（答题记录里的下拉更正）：只上送独立 verdict，
   *  不再携带 answer —— 服务端保留原答案并按 verdict 重放。 */
  correctDiagnostic: (
    sessionId: number,
    body: { attempt_id: number; verdict: CorrectionVerdict },
  ) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}/correct`, {
      method: 'POST',
      body,
    }),

  /** 到达题量上限后显式收尾（未答满也允许提前结束）。 */
  finishDiagnostic: (sessionId: number) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}/finish`, {
      method: 'POST',
    }),

  /** 盲区再测：以 weak/shaky 集为目标的确认会话。 */
  retestDiagnostic: (sessionId: number) =>
    request<DiagnosticDetail>(`/taxonomy/diagnostics/${sessionId}/retest`, {
      method: 'POST',
    }),

  /** 盲区清单（孩子=自己；家长传 user_id 看指定孩子）。 */
  blindspots: (userId?: number) =>
    request<BlindspotsResponse>(
      `/taxonomy/blindspots${qs({ user_id: userId })}`,
    ),

  /** 导出学习数据 JSON（浏览器下载）。 */
  async exportData(userId?: number): Promise<Blob> {
    const headers: Record<string, string> = {};
    const token = storedToken();
    if (token) headers.Authorization = `Bearer ${token}`;
    const resp = await fetchImpl(
      `${API_BASE}/taxonomy/export${qs({ user_id: userId })}`,
      {
        headers,
        credentials: 'same-origin',
      },
    );
    if (!resp.ok) {
      let msg = `导出失败（HTTP ${resp.status}）`;
      try {
        const data = (await resp.json()) as { error?: string };
        if (data?.error) msg = data.error;
      } catch {
        // 保留默认消息
      }
      throw new TaxonomyAPIError(msg, resp.status, null);
    }
    return resp.blob();
  },

  /** 知识点邻域图谱（卡 TX-GRAPH-PARENT-UI）：真实 DB 有效先修边的
   *  无向 1/2 跳展开，served 边保留存储方向；node.mastery 是解析目标的
   *  真实 kc_mastery（null = 无记录）。admin 必须显式 user_id。 */
  graph: (query: GraphQuery) =>
    request<GraphResponse>(
      `/taxonomy/graph${qs({ root: query.root, depth: query.depth, subject: query.subject, user_id: query.user_id })}`,
    ),

  /** 家长：内置 pack 重同步（手动重试路径，幂等）。
   *  冻结契约：POST /api/admin/taxonomy/sync（/import 不再是 pack 重同步）。 */
  adminSync: () =>
    request<ImportResult>('/admin/taxonomy/sync', { method: 'POST', body: {} }),

  /** 家长：恢复学习数据 JSON（export 导出文档）。
   *  冻结契约：POST /api/admin/taxonomy/import（/import-learning 仅后端
   *  兼容别名，前端不用）。恢复目标由 targetUserId 显式指定 —— 发送时
   *  非变更地在导出文档副本上加 target_user_id，源 user.id 原样保留作审计。 */
  adminImportLearning: (body: unknown, targetUserId: number) =>
    request<ImportLearningResult>('/admin/taxonomy/import', {
      method: 'POST',
      body: {
        ...(body as Record<string, unknown>),
        target_user_id: targetUserId,
      },
    }),

  /** 家长：按知识点/诊断路径生成 draft 课程。 */
  generateCourse: (body: GenerateCourseRequest) =>
    request<GenerateCourseResult>('/admin/taxonomy/courses/generate', {
      method: 'POST',
      body,
    }),

  /** 创建报告分享链接。
   *  响应是冻结契约 {share_id, url, token, expires_at}：url 是前端路由页
   *  /share/taxonomy/{token}（分享 UI 直接用）；token/share_id 供吊销等
   *  管理操作。公开读取走 sharedReport（API 端点）。 */
  shareDiagnostic: (sessionId: number) =>
    request<ShareResult>(`/taxonomy/diagnostics/${sessionId}/share`, {
      method: 'POST',
      body: {},
    }),

  /** 公开分享报告（无会话，token 即凭证）。
   *  服务端保留 {report_kind, expires_at, snapshot} wrapper；snapshot
   *  才是报告本体，调用方必须 unwrap（这里是原始透传）。 */
  sharedReport: (token: string) =>
    request<SharedReport>(`/taxonomy/shared/${encodeURIComponent(token)}`),

  /** 家长：孩子的分享链接列表（吊销 UI 数据源，卡 TX-PARENT-SHARE-PREREQ-API）。
   *  冻结契约：GET /api/admin/taxonomy/shares?user_id=<id>。行只含吊销
   *  需要的列（status 服务端派生 active|expired|revoked）——没有 token、
   *  token_hash、snapshot 或任何孩子答案。user_id 是服务端自增整数。 */
  listShares: (userId: number) =>
    request<SharesPage>(`/admin/taxonomy/shares${qs({ user_id: userId })}`),

  /** 家长：吊销一条分享链接。冻结契约：DELETE /api/admin/taxonomy/shares/{id}
   *  → {ok:true}；id 是分享行整数主键（ShareRow.id / ShareResult.share_id）。 */
  revokeShare: (shareId: number) =>
    request<RevokeShareResult>(`/admin/taxonomy/shares/${shareId}`, {
      method: 'DELETE',
    }),

  /** 家长：新增/改写一条先修边。冻结契约：PUT /api/admin/taxonomy/prerequisites
   *  body {topic_id, prerequisite_id, strength, reason}（两个 ref 是知识点
   *  external_ref，如 cnm_p009；reason 可空串）。成环/未知 ref/自环由服务端
   *  以 409/404/400 拒绝。响应 {edge, effective_edges}。 */
  putPrerequisite: (body: {
    topic_id: string;
    prerequisite_id: string;
    strength: PrerequisiteStrength;
    reason: string;
  }) =>
    request<PrerequisiteMutationResult>('/admin/taxonomy/prerequisites', {
      method: 'PUT',
      body,
    }),

  /** 家长：翻转一条已存先修边的复核状态（reviewed|rejected；rejected 只
   *  退出生效图、行保留审计，不删数据）。冻结契约：PATCH
   *  /api/admin/taxonomy/prerequisites/review body
   *  {topic_id, prerequisite_id, review_status} → {edge, effective_edges}。 */
  reviewPrerequisite: (body: {
    topic_id: string;
    prerequisite_id: string;
    review_status: PrerequisiteReviewStatus;
  }) =>
    request<PrerequisiteMutationResult>(
      '/admin/taxonomy/prerequisites/review',
      {
        method: 'PATCH',
        body,
      },
    ),
};
