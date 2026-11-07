// @vitest-environment jsdom
// API 客户端契约测试（卡 TX-FE-CODER）：路径/方法/请求体与冻结契约逐一对应，
// 错误形状 {error}、401/404 状态码、oc.token Bearer 注入。

import { afterEach, describe, expect, it, vi } from 'vitest';
import { setFetchImpl, taxonomyApi, TaxonomyAPIError } from './client';

function mockFetch(status: number, body: unknown): ReturnType<typeof vi.fn> {
  const fn = vi.fn(async () =>
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    }),
  );
  setFetchImpl(fn as unknown as typeof fetch);
  return fn;
}

afterEach(() => {
  // 还原原生 fetch，避免跨用例泄漏。
  setFetchImpl((...args) => fetch(...args));
  localStorage.clear();
  sessionStorage.clear();
});

describe('taxonomyApi 路径与请求体', () => {
  it('subjects 命中 GET /api/taxonomy/subjects', async () => {
    const fn = mockFetch(200, { subjects: [] });
    const res = await taxonomyApi.subjects();
    expect(res.subjects).toEqual([]);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/taxonomy/subjects');
    expect(init.method ?? 'GET').toBe('GET');
    expect(init.credentials).toBe('same-origin');
  });

  it('topics 组装 query 参数（跳过空值）', async () => {
    const fn = mockFetch(200, { topics: [], next_cursor: null });
    await taxonomyApi.topics({ subject: 'math', grade: 7, q: '方程', domain: '', limit: 20 });
    const [url] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/taxonomy/topics?subject=math&grade=7&q=%E6%96%B9%E7%A8%8B&limit=20');
  });

  it('topicDetail 编码 externalRef', async () => {
    const fn = mockFetch(200, { topic: { id: 'cnm_p001', name: 'x' }, prerequisites: null, dependents: null, questions: null, standards: null });
    await taxonomyApi.topicDetail('cnm_p001');
    expect((fn.mock.calls[0] as [string])[0]).toBe('/api/taxonomy/topics/cnm_p001');
  });

  it('practice POST 请求体', async () => {
    const fn = mockFetch(200, { correct: true });
    await taxonomyApi.practice({ topic_id: 'cnm_p001', question_id: 'cnm_p001_q1', answer: '4' });
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/taxonomy/practice');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({ topic_id: 'cnm_p001', question_id: 'cnm_p001_q1', answer: '4' });
  });

  it('createDiagnostic / answers / undo / correct / finish / retest 全部命中契约路径', async () => {
    const fn = mockFetch(200, { session: { id: 1, subject: 'math', mode: 'target', status: 'active' }, attempts: [] });
    await taxonomyApi.createDiagnostic({ mode: 'target', subject: 'math', targets: ['cnm_p001'] });
    expect((fn.mock.calls[0] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics');

    await taxonomyApi.answerDiagnostic(3, { question_id: 'q1', answer: 'A' });
    expect((fn.mock.calls[1] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics/3/answers');

    await taxonomyApi.undoDiagnostic(3);
    expect((fn.mock.calls[2] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics/3/undo');

    await taxonomyApi.correctDiagnostic(3, { attempt_id: 7, verdict: 'wrong' });
    expect((fn.mock.calls[3] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics/3/correct');
    // 冻结契约：body 只带 attempt_id + verdict，不再携带 answer（服务端保留原答案）。
    expect(JSON.parse((fn.mock.calls[3] as [string, RequestInit])[1].body as string)).toEqual({
      attempt_id: 7,
      verdict: 'wrong',
    });

    await taxonomyApi.finishDiagnostic(3);
    expect((fn.mock.calls[4] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics/3/finish');

    await taxonomyApi.retestDiagnostic(3);
    expect((fn.mock.calls[5] as [string, RequestInit])[0]).toBe('/api/taxonomy/diagnostics/3/retest');
  });

  it('blindspots 带 user_id；diagnostics 列表与导出同', async () => {
    const fn = mockFetch(200, { weak: [], shaky: [], mastered: [], by_chapter: [] });
    await taxonomyApi.blindspots(42);
    expect((fn.mock.calls[0] as [string])[0]).toBe('/api/taxonomy/blindspots?user_id=42');
    await taxonomyApi.diagnostics(42);
    expect((fn.mock.calls[1] as [string])[0]).toBe('/api/taxonomy/diagnostics?user_id=42');
  });

  it('generateCourse POST /api/admin/taxonomy/courses/generate', async () => {
    const fn = mockFetch(200, { course_id: 9 });
    const res = await taxonomyApi.generateCourse({ user_id: 2, title: '数学', subject: 'math', topic_ids: ['a'] });
    expect(res.course_id).toBe(9);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/courses/generate');
    expect(init.method).toBe('POST');
  });

  it('adminSync POST /api/admin/taxonomy/sync；adminImportLearning POST /import 且 target_user_id 覆盖目标', async () => {
    // pack 重同步与学习恢复是两个独立端点（冻结契约）。
    const fn = mockFetch(200, { imported: 3, skipped: 1 });
    const res = await taxonomyApi.adminSync();
    expect(res.imported).toBe(3);
    let [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/sync');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({});

    // 学习恢复：POST /api/admin/taxonomy/import，target_user_id 是恢复目标，
    // 源 user.id 原样保留作审计（非变更地加在导出文档上）。
    const doc = { schema_version: 1, user: { id: 2, name: 'Emma', role: 'child' }, diagnostics: [] };
    await taxonomyApi.adminImportLearning(doc, 42);
    [url, init] = fn.mock.calls[1] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/import');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body as string)).toEqual({
      schema_version: 1,
      user: { id: 2, name: 'Emma', role: 'child' },
      diagnostics: [],
      target_user_id: 42,
    });
    // 发送的是副本：调用方的导出文档未被变更。
    expect(doc).toEqual({ schema_version: 1, user: { id: 2, name: 'Emma', role: 'child' }, diagnostics: [] });
  });

  it('share 命中 POST /share（body 默认空对象）', async () => {
    const fn = mockFetch(201, { share_id: 9, url: '/share/taxonomy/abc', token: 'abc', expires_at: '2026-11-12T00:00:00Z' });
    const res = await taxonomyApi.shareDiagnostic(5);
    // 响应是冻结契约：share_id/token（吊销管理）+ url（前端路由页）。
    expect(res.share_id).toBe(9);
    expect(res.token).toBe('abc');
    expect(res.url).toBe('/share/taxonomy/abc');
    const [shareUrl, shareInit] = fn.mock.calls[0] as [string, RequestInit];
    expect(shareUrl).toBe('/api/taxonomy/diagnostics/5/share');
    expect(shareInit.method).toBe('POST');
    expect(JSON.parse(shareInit.body as string)).toEqual({});

    // 公开读取按 token 寻址。
    await taxonomyApi.sharedReport('tok');
    expect((fn.mock.calls[1] as [string])[0]).toBe('/api/taxonomy/shared/tok');
  });

  it('undo 默认 count=1', async () => {
    const fn = mockFetch(200, { session: { id: 1 }, attempts: [] });
    await taxonomyApi.undoDiagnostic(9);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/taxonomy/diagnostics/9/undo');
    expect(JSON.parse(init.body as string)).toEqual({ count: 1 });
  });
});

describe('家长分享与先修管理（TX-PARENT-SHARE-PREREQ-API）', () => {
  const shareRow = {
    id: 7,
    owner_id: 42,
    session_id: 3,
    report_kind: 'diagnosis',
    expires_at: '2026-11-12T00:00:00Z',
    revoked_at: null,
    created_at: '2026-11-05T00:00:00Z',
    status: 'active',
  };

  it('listShares GET /api/admin/taxonomy/shares?user_id=，空列表是 []', async () => {
    const fn = mockFetch(200, { shares: [] });
    const empty = await taxonomyApi.listShares(42);
    expect(empty.shares).toEqual([]);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    // user_id 是服务端自增整数，走 qs 组装（不许手写 URL）。
    expect(url).toBe('/api/admin/taxonomy/shares?user_id=42');
    expect(init.method ?? 'GET').toBe('GET');

    // 非空列表：行字段逐一对应冻结契约（status 三值由服务端派生）。
    const body = mockFetch(200, { shares: [shareRow] });
    const rows = await taxonomyApi.listShares(42);
    expect((body.mock.calls[0] as [string])[0]).toBe('/api/admin/taxonomy/shares?user_id=42');
    expect(rows.shares?.[0]).toEqual(shareRow);
    expect(rows.shares?.[0]?.status).toBe('active');
  });

  it('revokeShare DELETE /api/admin/taxonomy/shares/{id} → {ok:true}', async () => {
    const fn = mockFetch(200, { ok: true });
    const res = await taxonomyApi.revokeShare(7);
    expect(res.ok).toBe(true);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/shares/7');
    expect(init.method).toBe('DELETE');
  });

  it('putPrerequisite PUT /api/admin/taxonomy/prerequisites 与响应 {edge,effective_edges}', async () => {
    const payload = {
      edge: {
        topic_id: 'cnm_p009',
        prerequisite_id: 'cnm_p001',
        strength: 'hard',
        reason: '整式乘除是因式分解的先修',
        review_status: 'reviewed',
      },
      effective_edges: 312,
    };
    const fn = mockFetch(200, payload);
    const res = await taxonomyApi.putPrerequisite({
      topic_id: 'cnm_p009',
      prerequisite_id: 'cnm_p001',
      strength: 'hard',
      reason: '整式乘除是因式分解的先修',
    });
    expect(res.edge.review_status).toBe('reviewed');
    expect(res.effective_edges).toBe(312);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/prerequisites');
    expect(init.method).toBe('PUT');
    expect(JSON.parse(init.body as string)).toEqual({
      topic_id: 'cnm_p009',
      prerequisite_id: 'cnm_p001',
      strength: 'hard',
      reason: '整式乘除是因式分解的先修',
    });
  });

  it('reviewPrerequisite PATCH /api/admin/taxonomy/prerequisites/review 与响应', async () => {
    const payload = {
      edge: { topic_id: 'cnm_p009', prerequisite_id: 'cnm_p001', review_status: 'rejected' },
      effective_edges: 311,
    };
    const fn = mockFetch(200, payload);
    const res = await taxonomyApi.reviewPrerequisite({
      topic_id: 'cnm_p009',
      prerequisite_id: 'cnm_p001',
      review_status: 'rejected',
    });
    expect(res.edge.review_status).toBe('rejected');
    expect(res.effective_edges).toBe(311);
    const [url, init] = fn.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/admin/taxonomy/prerequisites/review');
    // RequestOptions 已扩展 PATCH，review 端点是唯一的 PATCH 消费者。
    expect(init.method).toBe('PATCH');
    expect(JSON.parse(init.body as string)).toEqual({
      topic_id: 'cnm_p009',
      prerequisite_id: 'cnm_p001',
      review_status: 'rejected',
    });
  });

  it('先修端点的 400/404/409 错误统一为 TaxonomyAPIError（status+message）', async () => {
    // 400：非法 strength。
    mockFetch(400, { error: 'strength must be "hard" or "soft"' });
    let err = await taxonomyApi
      .putPrerequisite({ topic_id: 'cnm_p009', prerequisite_id: 'cnm_p001', strength: 'hard', reason: '' })
      .catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(400);
    expect((err as TaxonomyAPIError).message).toBe('strength must be "hard" or "soft"');

    // 404：unknown ref。
    mockFetch(404, { error: 'unknown topic_id' });
    err = await taxonomyApi
      .putPrerequisite({ topic_id: 'cnm_missing', prerequisite_id: 'cnm_p001', strength: 'soft', reason: '' })
      .catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(404);
    expect((err as TaxonomyAPIError).message).toBe('unknown topic_id');

    // 409：成环（error 文本含中文环提示也不影响 message 透传）。
    mockFetch(409, { error: 'prerequisite edge conflicts with the graph: 先修图存在环' });
    err = await taxonomyApi
      .putPrerequisite({ topic_id: 'cnm_p001', prerequisite_id: 'cnm_p009', strength: 'hard', reason: '' })
      .catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(409);
    expect((err as TaxonomyAPIError).message).toContain('conflicts with the graph');

    // PATCH 的 404：无存储边。
    mockFetch(404, { error: 'prerequisite edge not found' });
    err = await taxonomyApi
      .reviewPrerequisite({ topic_id: 'cnm_p001', prerequisite_id: 'cnm_p009', review_status: 'rejected' })
      .catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(404);
    expect((err as TaxonomyAPIError).message).toBe('prerequisite edge not found');
  });

  it('listShares 的 400（缺/非法 user_id）与 404（吊销不存在的 share）', async () => {
    mockFetch(400, { error: 'invalid user_id' });
    let err = await taxonomyApi.listShares(0).catch((e) => e);
    expect(err).toBeInstanceOf(TaxonomyAPIError);
    expect((err as TaxonomyAPIError).status).toBe(400);
    expect((err as TaxonomyAPIError).message).toBe('invalid user_id');

    mockFetch(404, { error: 'share not found' });
    err = await taxonomyApi.revokeShare(999).catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(404);
    expect((err as TaxonomyAPIError).message).toBe('share not found');
  });
});

describe('认证与错误形状', () => {
  it('localStorage oc.token 作为 Bearer 注入', async () => {
    localStorage.setItem('oc.token', 'ocs1.abc');
    const fn = mockFetch(200, { subjects: [] });
    await taxonomyApi.subjects();
    const init = (fn.mock.calls[0] as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer ocs1.abc');
  });

  it('sessionStorage oc.token 也生效', async () => {
    sessionStorage.setItem('oc.token', 'ocs1.xyz');
    const fn = mockFetch(200, { subjects: [] });
    await taxonomyApi.subjects();
    const init = (fn.mock.calls[0] as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>).Authorization).toBe('Bearer ocs1.xyz');
  });

  it('无 token 时同源 cookie（无 Authorization 头）', async () => {
    const fn = mockFetch(200, { subjects: [] });
    await taxonomyApi.subjects();
    const init = (fn.mock.calls[0] as [string, RequestInit])[1];
    expect((init.headers as Record<string, string>).Authorization).toBeUndefined();
    expect(init.credentials).toBe('same-origin');
  });

  it('服务端 {error} 文本抛 TaxonomyAPIError（带 status）', async () => {
    mockFetch(404, { error: '找不到这个知识点' });
    const err = await taxonomyApi.topicDetail('nope').catch((e) => e);
    expect(err).toBeInstanceOf(TaxonomyAPIError);
    expect((err as TaxonomyAPIError).status).toBe(404);
    expect((err as TaxonomyAPIError).message).toBe('找不到这个知识点');
  });

  it('无 error 字段时给出 HTTP 状态描述', async () => {
    mockFetch(502, 'upstream down');
    const err = await taxonomyApi.subjects().catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(502);
    expect((err as TaxonomyAPIError).message).toContain('502');
  });

  it('401（未登录）错误可被上层捕获展示', async () => {
    mockFetch(401, { error: 'sign in required' });
    const err = await taxonomyApi.subjects().catch((e) => e);
    expect((err as TaxonomyAPIError).status).toBe(401);
    expect((err as TaxonomyAPIError).message).toBe('sign in required');
  });

  it('exportData 返回 Blob 且错误带服务端消息', async () => {
    const blob = new Blob(['{}'], { type: 'application/json' });
    // jsdom 的 Response 构造器对 Blob body 有兼容性问题（object.stream is not a
    // function），用文本 body + blob() mock 绕开——真实浏览器无此问题。
    const ok = vi.fn(async () => ({
      ok: true,
      status: 200,
      blob: async () => blob,
    } as unknown as Response));
    setFetchImpl(ok as unknown as typeof fetch);
    const data = await taxonomyApi.exportData(7);
    expect(data).toBeInstanceOf(Blob);
    expect((ok.mock.calls[0] as unknown as [string])[0]).toBe('/api/taxonomy/export?user_id=7');

    mockFetch(403, { error: '需要管理员权限' });
    const err = await taxonomyApi.exportData().catch((e) => e);
    expect((err as TaxonomyAPIError).message).toBe('需要管理员权限');
    expect((err as TaxonomyAPIError).status).toBe(403);
  });
});
