// 真实 Chromium 下的 PWA Service Worker 安全 E2E（TX-PWA-REAL-E2E-V2）。
//
// 全部请求走真实 `vite preview` 生产包、真实 Go API 与真实浏览器
// CacheStorage/ServiceWorker；不允许 page.route 伪造 API 响应。个别用例只
// 对浏览器原生 CacheStorage 时序/失败注入做可观测包装，用来验证生产清理的
// 有界降级与代数保护，它们不改任何 API 响应，也不改生产代码。
//
// 覆盖的冻结合同（web/public/openchore-sw.js）：
//   - 公开目录：/api/taxonomy/subjects 与 q 全空白的 /api/taxonomy/topics 可缓存；
//   - 管理端含答案的 topics/{ref} 详情、非空 q（含重复 q/编码 q/U+FEFF/零宽）
//     以及所有其它 /api/* 一律 network-only；
//   - 离线 /learn 只能回放公开壳/公开目录，不能回放 admin 详情或旧用户数据；
//   - 登录/切换/登出边界持有 await 清理，旧认证响应不得复活用户或缓存；
//   - CLEAR 双轮在途写入的代数保护、删除失败的有界降级；
//   - activate 只清理 openchore-* 旧代，不能删除其它应用的 cache。
import { test, expect, type Page } from '@playwright/test';
import { enterPin, SEEDED_PINS } from './helpers/setup';
import {
  OTHER_APP_CACHE,
  OTHER_APP_MARKER,
  OLD_USER_CACHE,
  OLD_USER_MARKER,
  allCacheText,
  attachEvidence,
  cacheHit,
  cacheKeys,
  cacheSnapshot,
  hasCachedAssets,
  publicTaxonomyBodies,
  recordCacheSnapshot,
  seedMarkerIntoCache,
  seedOldUserCacheFixture,
  seedOtherAppCache,
  sendClearToServiceWorker,
  snapshotHasCacheWithBody,
  taxonomyCacheName,
  waitForCacheHit,
  waitForServiceWorker,
  type CacheSnapshot,
} from './helpers/pwa';

const PREVIEW_PORT = process.env.E2E_PREVIEW_PORT || '4175';
const APP_ORIGIN = `http://localhost:${PREVIEW_PORT}`;
const TOPIC_REF = 'cnm_p001';
const DETAIL_PATH = `/api/taxonomy/topics/${TOPIC_REF}`;
const TOPIC_SEARCH = '1到5各数的认识';

const HARNESS_NAV_KEY = 'pwa-e2e-nav-started';
const HARNESS_DELETE_AFTER_NAV_KEY = 'pwa-e2e-delete-after-navigate';
const HARNESS_CLEAR_STARTED_KEY = 'pwa-e2e-clear-started';
const HARNESS_DELETE_FAILURES_KEY = 'pwa-e2e-delete-failures';

// 非空 q 覆盖面：普通中文、重复 q（一个空一个非空）、编码 q，以及
// reviewer 指定的 U+FEFF 与零宽/格式字符。全部必须 network-only。
const NON_BLANK_Q_PATHS = [
  '/api/taxonomy/topics?subject=math&q=%E4%B8%80',
  '/api/taxonomy/topics?subject=math&q=&q=%E4%B8%80',
  '/api/taxonomy/topics?subject=math&q=%E2%80%8B',
  '/api/taxonomy/topics?subject=math&q=%E2%80%8C',
  '/api/taxonomy/topics?subject=math&q=%E2%80%8D',
  '/api/taxonomy/topics?subject=math&q=%E2%81%A0',
  '/api/taxonomy/topics?subject=math&q=%EF%BB%BF',
];

interface SensitiveMarker {
  field: 'answer' | 'accept' | 'explanation' | 'reference_answer';
  value: string;
  fragment: string;
  questionId: string;
  source: 'topic-detail' | 'practice-feedback';
}

interface FetchResult {
  ok: boolean;
  status: number;
  body: string;
  error?: string;
}

function doorLabel(name: string): RegExp {
  return new RegExp(`^(?:为 ${name} 选择资料|Select profile for ${name})$`);
}

function meButtonLabel(name: string): RegExp {
  return new RegExp(`^${name}[：:]\\s*(?:设置|settings)$`);
}

const SIGN_OUT_LABEL = /^(?:退出登录|Sign out)$/i;

function entriesOf(snapshot: CacheSnapshot): Array<{ cache: string; url: string; status: number; body: string }> {
  return snapshot.caches.flatMap((cache) =>
    cache.entries.map((entry) => ({ cache: cache.name, ...entry })));
}

function entriesWithPath(snapshot: CacheSnapshot, pathname: string): Array<{ cache: string; url: string; status: number; body: string }> {
  return entriesOf(snapshot).filter((entry) => {
    try {
      return new URL(entry.url).pathname === pathname;
    } catch {
      return false;
    }
  });
}

function topicQueryIsNonBlank(value: string): boolean {
  return value !== '' && !/^\p{White_Space}*$/u.test(value);
}

function nonBlankTopicQueryEntries(snapshot: CacheSnapshot): string[] {
  const found: string[] = [];
  for (const entry of entriesOf(snapshot)) {
    let url: URL;
    try {
      url = new URL(entry.url);
    } catch {
      continue;
    }
    if (url.pathname !== '/api/taxonomy/topics') continue;
    if (url.searchParams.getAll('q').some(topicQueryIsNonBlank)) found.push(entry.url);
  }
  return found;
}

/**
 * 在一个完整观察窗口内持续枚举 CacheStorage，返回期间出现过的非空 q 缓存键。
 * 只做一次瞬时检查会漏掉 SW 的 fire-and-forget cache.put，因此窗口内反复采样；
 * 正常实现应始终返回空数组。
 */
async function observeNonBlankTopicQueryEntries(page: Page, durationMs = 2_500): Promise<string[]> {
  const deadline = Date.now() + durationMs;
  let found: string[] = [];
  do {
    found = nonBlankTopicQueryEntries(await cacheSnapshot(page));
    if (found.length > 0) return found;
    await page.waitForTimeout(100);
  } while (Date.now() < deadline);
  return found;
}

async function fetchFromPage(
  page: Page,
  path: string,
  init?: { method?: string; body?: string },
): Promise<FetchResult> {
  return page.evaluate(async ({ path, init }) => {
    try {
      const headers: Record<string, string> = {};
      if (init?.body !== undefined) headers['Content-Type'] = 'application/json';
      const response = await fetch(path, {
        method: init?.method ?? 'GET',
        headers,
        body: init?.body,
        credentials: 'same-origin',
      });
      return { ok: true, status: response.status, body: await response.text() };
    } catch (error) {
      return { ok: false, status: 0, body: '', error: String(error) };
    }
  }, { path, init });
}

function detailSensitiveMarkers(payload: unknown): SensitiveMarker[] {
  const questions = (payload as { questions?: unknown } | null)?.questions;
  if (!Array.isArray(questions)) return [];
  const candidates: SensitiveMarker[] = [];
  for (const rawQuestion of questions) {
    if (!rawQuestion || typeof rawQuestion !== 'object') continue;
    const question = rawQuestion as Record<string, unknown>;
    const questionId = typeof question.id === 'string' ? question.id : '';
    const explanation = typeof question.explanation === 'string' ? question.explanation.trim() : '';
    if (explanation) {
      candidates.push({
        field: 'explanation',
        value: explanation,
        fragment: `"explanation":${JSON.stringify(explanation)}`,
        questionId,
        source: 'topic-detail',
      });
    }
    const answer = typeof question.answer === 'string' ? question.answer.trim() : '';
    if (answer) {
      candidates.push({
        field: 'answer',
        value: answer,
        fragment: `"answer":${JSON.stringify(answer)}`,
        questionId,
        source: 'topic-detail',
      });
    }
    if (Array.isArray(question.accept)) {
      for (const accept of question.accept) {
        if (typeof accept !== 'string' || !accept.trim()) continue;
        const value = accept.trim();
        candidates.push({
          field: 'accept',
          value,
          fragment: `"accept":${JSON.stringify([value])}`,
          questionId,
          source: 'topic-detail',
        });
      }
    }
  }
  return candidates;
}

/** 取信息量最大的真实敏感值；解释通常最长，避免把过短的 "4" 当成唯一标记。 */
function pickSensitiveMarker(candidates: SensitiveMarker[], raw: string): SensitiveMarker | null {
  if (candidates.length === 0) return null;
  candidates.sort((a, b) => b.value.length - a.value.length);
  const primary = candidates[0];
  if (!raw.includes(primary.value)) {
    throw new Error(`answer-bearing raw body does not contain the ${primary.field} marker`);
  }
  return primary;
}

function parseDetailMarker(raw: string, payload: unknown): SensitiveMarker | null {
  return pickSensitiveMarker(detailSensitiveMarkers(payload), raw);
}

function parsePracticeMarker(raw: string, payload: unknown): SensitiveMarker {
  const body = payload as { reference_answer?: unknown; explanation?: unknown; question?: { id?: unknown } } | null;
  const questionId = typeof body?.question?.id === 'string' ? body.question.id : '';
  const candidates: SensitiveMarker[] = [];
  const explanation = typeof body?.explanation === 'string' ? body.explanation.trim() : '';
  if (explanation) {
    candidates.push({
      field: 'explanation',
      value: explanation,
      fragment: `"explanation":${JSON.stringify(explanation)}`,
      questionId,
      source: 'practice-feedback',
    });
  }
  const referenceAnswer = typeof body?.reference_answer === 'string' ? body.reference_answer.trim() : '';
  if (referenceAnswer) {
    candidates.push({
      field: 'reference_answer',
      value: referenceAnswer,
      fragment: `"reference_answer":${JSON.stringify(referenceAnswer)}`,
      questionId,
      source: 'practice-feedback',
    });
  }
  const marker = pickSensitiveMarker(candidates, raw);
  if (!marker) throw new Error('practice feedback carried no non-empty reference_answer/explanation');
  return marker;
}

function firstQuestionId(payload: unknown): string {
  const questions = (payload as { questions?: unknown } | null)?.questions;
  if (!Array.isArray(questions)) throw new Error('topic detail response has no questions array');
  for (const rawQuestion of questions) {
    if (!rawQuestion || typeof rawQuestion !== 'object') continue;
    const id = (rawQuestion as { id?: unknown }).id;
    if (typeof id === 'string' && id) return id;
  }
  throw new Error('topic detail response has no question id');
}

async function findSeededUserId(page: Page, name: string): Promise<number> {
  const users = JSON.parse((await fetchFromPage(page, '/api/users')).body) as Array<{ id: number; name: string }>;
  const match = users.find((user) => user.name === name);
  if (!match) throw new Error(`seeded profile not found: ${name}`);
  return match.id;
}

/**
 * 当前生产契约（taxonomy.go questionToView(q, false)）对管理员也从 topic
 * detail 回答面剥离 answer/accept/explanation；真实答案只在练习提交反馈里
 * 出现。这里优先记录 detail 若未来重新包含答案的标记，否则用真实 practice
 * 响应取得本题敏感值。两条路径都是真实 API，且都不得进入 CacheStorage。
 */
async function getAdminSensitiveMarker(
  page: Page,
  detailRaw: string,
  detailPayload: unknown,
): Promise<SensitiveMarker> {
  const direct = parseDetailMarker(detailRaw, detailPayload);
  if (direct) {
    await attachEvidence('admin-answer-source', direct);
    return direct;
  }
  const questionId = firstQuestionId(detailPayload);
  const userId = await findSeededUserId(page, 'Alex');
  const practice = await fetchFromPage(page, '/api/taxonomy/practice', {
    method: 'POST',
    body: JSON.stringify({
      user_id: userId,
      topic_id: TOPIC_REF,
      question_id: questionId,
      answer: '__pwa_e2e_non_answer__',
    }),
  });
  if (!practice.ok || practice.status !== 200) {
    throw new Error(`real practice feedback failed: ${practice.status} ${practice.error ?? ''}`);
  }
  const marker = parsePracticeMarker(practice.body, JSON.parse(practice.body));
  await attachEvidence('admin-answer-source', {
    source: marker.source,
    detailCarriedAnswer: false,
    practiceUrl: '/api/taxonomy/practice',
    practiceStatus: practice.status,
    marker,
  });
  return marker;
}

async function goToLoginAndWaitForWorker(page: Page): Promise<void> {
  await page.goto('/favicon.svg');
  await seedOtherAppCache(page);
  await page.goto('/login');
  await waitForServiceWorker(page);
  await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });
}

async function loginViaUi(page: Page, name = 'Alex', pin = SEEDED_PINS[1]): Promise<void> {
  await page.getByRole('button', { name: doorLabel(name) }).click();
  await enterPin(page, pin);
  await page.waitForURL((url) => url.pathname === '/', { timeout: 30_000 });
  await expect(page.getByRole('button', { name: meButtonLabel(name) })).toBeVisible({ timeout: 30_000 });
}

async function openTaxonomyWorkspace(page: Page): Promise<void> {
  await page.goto('/admin/dashboard#tab=taxonomy');
  await expect(page.locator('[data-test="admin-taxonomy-page"]')).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('[data-test="tx-workspace"]')).toBeVisible({ timeout: 30_000 });
  await expect(page.locator('[data-test="tx-topic-search"]')).toBeVisible({ timeout: 30_000 });
}

async function viewTopicDetailViaUi(page: Page): Promise<{ status: number; raw: string }> {
  await page.locator('[data-test="tx-topic-search"]').fill(TOPIC_SEARCH);
  const topicRow = page.locator(`[data-test="tx-topic-${TOPIC_REF}"]`);
  await expect(topicRow).toBeVisible({ timeout: 30_000 });
  const [response] = await Promise.all([
    page.waitForResponse((candidate) => {
      try {
        return new URL(candidate.url()).pathname === DETAIL_PATH && candidate.status() === 200;
      } catch {
        return false;
      }
    }, { timeout: 30_000 }),
    topicRow.click(),
  ]);
  const raw = await response.text();
  await attachEvidence('admin-detail-real-response', {
    url: response.url(),
    status: response.status(),
    carriesDirectAnswerFields: detailSensitiveMarkers(JSON.parse(raw)).length > 0,
    carriesDiagnosticJSON: raw.includes('diagnostic_question_json'),
  });
  await expect(page.locator('[data-test="tx-topic-detail"]')).toBeVisible({ timeout: 30_000 });
  return { status: response.status(), raw };
}

async function warmPublicTaxonomy(page: Page): Promise<void> {
  const subjects = await fetchFromPage(page, '/api/taxonomy/subjects');
  expect(subjects.ok, 'online GET /api/taxonomy/subjects must complete').toBe(true);
  expect(subjects.status, 'online GET /api/taxonomy/subjects status').toBe(200);
  expect(JSON.parse(subjects.body).subjects.length, 'online subjects payload').toBeGreaterThan(0);
  const topics = await fetchFromPage(page, '/api/taxonomy/topics?subject=math');
  expect(topics.ok, 'online blank-q topics must complete').toBe(true);
  expect(topics.status, 'online blank-q topics status').toBe(200);
  expect(JSON.parse(topics.body).topics.length, 'online blank-q topics payload').toBeGreaterThan(0);
  await waitForCacheHit(page, '/api/taxonomy/subjects');
  await waitForCacheHit(page, '/api/taxonomy/topics?subject=math');
}

async function readHarnessFlag(page: Page, key: string): Promise<string | null> {
  return page.evaluate((flag) => {
    try {
      return sessionStorage.getItem(flag);
    } catch {
      return null;
    }
  }, key);
}

/**
 * 真实浏览器清理时序探针：只包装页面侧 CacheStorage.delete 与 history，
 * 让删除延迟 700ms，并记录「页面清理是否在导航之后才完成」。不伪造任何
 * API 响应；SW 内部的 delete 不受页面 prototype 包装影响。
 */
async function installSlowDeleteHarness(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const NAV_KEY = 'pwa-e2e-nav-started';
    const DELETE_AFTER_KEY = 'pwa-e2e-delete-after-navigate';
    const CLEAR_STARTED_KEY = 'pwa-e2e-clear-started';
    const markNavigation = (url: unknown) => {
      try {
        const parsed = new URL(String(url), location.href);
        if (parsed.pathname === '/') sessionStorage.setItem(NAV_KEY, '1');
      } catch {
        // 非 URL 参数：忽略。
      }
    };
    const originalPush = history.pushState;
    history.pushState = function (this: History, ...args: Parameters<History['pushState']>) {
      markNavigation(args[2]);
      return originalPush.apply(this, args);
    };
    const originalReplace = history.replaceState;
    history.replaceState = function (this: History, ...args: Parameters<History['replaceState']>) {
      markNavigation(args[2]);
      return originalReplace.apply(this, args);
    };
    const proto = CacheStorage.prototype as unknown as { delete(name: string): Promise<boolean> };
    const originalDelete = proto.delete;
    proto.delete = function (this: CacheStorage, name: string): Promise<boolean> {
      const isAppCache = String(name).startsWith('openchore-');
      if (isAppCache) {
        try {
          sessionStorage.setItem(CLEAR_STARTED_KEY, '1');
        } catch {
          // sessionStorage 不可用：只影响证据，不影响删除。
        }
      }
      const result = originalDelete.call(this, name);
      if (!isAppCache) return result;
      return result.then((deleted) => new Promise<boolean>((resolve) => {
        setTimeout(() => {
          try {
            if (sessionStorage.getItem(NAV_KEY) === '1') {
              sessionStorage.setItem(DELETE_AFTER_KEY, '1');
            }
          } catch {
            // 证据写入失败不影响删除结果。
          }
          resolve(deleted);
        }, 700);
      }));
    };
  });
}

/** 让页面侧 CacheStorage.delete 对 openchore-* 全部 reject，验证清理有界降级。 */
async function installDeleteFailureHarness(page: Page): Promise<void> {
  await page.addInitScript(() => {
    const proto = CacheStorage.prototype as unknown as { delete(name: string): Promise<boolean> };
    const originalDelete = proto.delete;
    proto.delete = function (this: CacheStorage, name: string): Promise<boolean> {
      if (String(name).startsWith('openchore-')) {
        try {
          const current = Number(sessionStorage.getItem('pwa-e2e-delete-failures') || '0');
          sessionStorage.setItem('pwa-e2e-delete-failures', String(current + 1));
        } catch {
          // 计数不可用不影响失败注入。
        }
        return Promise.reject(new Error('E2E injected CacheStorage.delete failure'));
      }
      return originalDelete.call(this, name);
    };
  });
}

async function cachedShellReady(page: Page): Promise<boolean> {
  const hit = await cacheHit(page, '/');
  return !!hit && hit.body.includes('<html');
}

test.afterEach(async ({ context }) => {
  // 任何用例失败都要复原网络状态，避免影响同一 worker 里的后续用例。
  await context.setOffline(false).catch(() => undefined);
});

test.describe('PWA service worker — real production browser', () => {
  test('admin detail answers, non-blank q and address-bar navigation never enter CacheStorage', async ({ page, context }) => {
    await goToLoginAndWaitForWorker(page);

    // activate 只删 openchore-* 旧代：SW 激活前后其它应用 cache 都必须保留。
    const afterActivate = await recordCacheSnapshot(page, 'after-activate');
    expect(
      snapshotHasCacheWithBody(afterActivate, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'activate must not delete another app cache',
    ).toBe(true);

    // 登录边界清理上一会话 fixture；登录完成时它必须已经从所有 cache 正文消失。
    await seedOldUserCacheFixture(page);
    await loginViaUi(page);
    const afterLogin = await recordCacheSnapshot(page, 'after-login');
    expect(
      snapshotHasCacheWithBody(afterLogin, OLD_USER_CACHE, OLD_USER_MARKER),
      'login must clear stale user cache before navigation',
    ).toBe(false);
    expect(allCacheText(afterLogin), 'stale user marker survived login').not.toContain(OLD_USER_MARKER);

    // 管理端通过真实 UI 打开 cnm_p001 详情；若当前契约在 detail 侧剥离答案，
    // 则用真实 practice 提交反馈取得本题的 reference_answer/explanation。
    await openTaxonomyWorkspace(page);
    const detail = await viewTopicDetailViaUi(page);
    expect(detail.status, 'admin detail status').toBe(200);
    const detailPayload = JSON.parse(detail.raw);
    const marker = await getAdminSensitiveMarker(page, detail.raw, detailPayload);
    expect(marker.value.length, 'sensitive marker must be non-empty').toBeGreaterThan(0);
    expect(detail.raw, 'admin detail must not carry diagnostic_question_json')
      .not.toContain('diagnostic_question_json');

    // 用真实 fetch 预热两个公开目录端点；等待 SW 的 cache.put 可观测落地。
    await warmPublicTaxonomy(page);

    // 非空 q 在线全部成功，但必须 network-only；U+FEFF/零宽重复 q 同样覆盖。
    const onlineQ: Array<{ path: string; status: number }> = [];
    for (const path of NON_BLANK_Q_PATHS) {
      const result = await fetchFromPage(page, path);
      expect(result.ok, `online non-blank q fetch failed: ${path} ${result.error ?? ''}`).toBe(true);
      // 搜索词不做服务端格式校验；200 是网关/API 正常可达的证据。
      expect(result.status, `online non-blank q status: ${path}`).toBe(200);
      expect(JSON.parse(result.body).topics, `non-blank q payload must be a topics page: ${path}`).toBeDefined();
      onlineQ.push({ path, status: result.status });
    }
    await attachEvidence('non-blank-q-online', onlineQ);

    const forbiddenQ = await observeNonBlankTopicQueryEntries(page, 2_500);
    expect(forbiddenQ, 'non-blank q must never enter CacheStorage').toEqual([]);

    const onlineSnapshot = await recordCacheSnapshot(page, 'online-caches');
    const publicBodies = publicTaxonomyBodies(onlineSnapshot);
    const publicTopicBodies = publicBodies.filter((entry) => {
      try {
        return new URL(entry.url).pathname === '/api/taxonomy/topics';
      } catch {
        return false;
      }
    });
    expect(publicTopicBodies.length, 'blank-q /api/taxonomy/topics must be cached').toBeGreaterThan(0);
    for (const cached of publicTopicBodies) {
      expect(cached.body, `public topics cache leaked answer: ${cached.url}`).not.toContain('"answer"');
      expect(cached.body, `public topics cache leaked diagnostic_question_json: ${cached.url}`)
        .not.toContain('diagnostic_question_json');
      expect(cached.body, `public topics cache leaked detail marker: ${cached.url}`)
        .not.toContain(marker.value);
      expect(cached.body, `public topics cache leaked detail field fragment: ${cached.url}`)
        .not.toContain(marker.fragment);
    }

    // detail URL/敏感标记不得出现在任何 cache key 或正文里。
    expect(entriesWithPath(onlineSnapshot, DETAIL_PATH), 'detail URL entered CacheStorage').toEqual([]);
    expect(entriesWithPath(onlineSnapshot, '/api/taxonomy/practice'),
      'answer-bearing practice feedback entered CacheStorage').toEqual([]);
    const onlineCacheText = allCacheText(onlineSnapshot);
    expect(onlineCacheText, 'detail path string entered cache text').not.toContain(DETAIL_PATH);
    expect(onlineCacheText, 'admin detail sensitive value entered cache text').not.toContain(marker.value);
    expect(onlineCacheText, 'admin detail sensitive field fragment entered cache text')
      .not.toContain(marker.fragment);
    expect(await cacheHit(page, DETAIL_PATH), 'detail must never have a direct cache match').toBeNull();

    // 地址栏直接导航到 admin 详情：在线响应本身可含当前契约允许的字段，
    // 但这条顶层 navigate 响应绝不能进入 CacheStorage。
    const addressBar = await page.goto(DETAIL_PATH);
    expect(addressBar?.status(), 'online address-bar detail status').toBe(200);
    const addressBarBody = await addressBar!.text();
    const directAddressBarMarker = parseDetailMarker(addressBarBody, JSON.parse(addressBarBody));
    if (directAddressBarMarker) {
      expect(addressBarBody, 'address-bar detail carried the admin answer')
        .toContain(directAddressBarMarker.value);
    } else {
      expect(addressBarBody, 'address-bar detail unexpectedly carried a sensitive marker')
        .not.toContain(marker.value);
    }
    const afterAddressBar = await recordCacheSnapshot(page, 'after-address-bar-detail');
    expect(entriesWithPath(afterAddressBar, DETAIL_PATH), 'address-bar detail was cached').toEqual([]);
    expect(allCacheText(afterAddressBar), 'address-bar detail body was cached')
      .not.toContain(marker.fragment);

    // 回到应用后切离线：公开目录 q 请求失败、detail 失败且无缓存。
    await page.goto('/');
    await expect(page.getByRole('button', { name: meButtonLabel('Alex') })).toBeVisible({ timeout: 30_000 });
    await context.setOffline(true);

    const offlineQ = await page.evaluate(async (paths) => {
      const result: Record<string, { failed: boolean; status: number }> = {};
      for (const path of paths) {
        try {
          const response = await fetch(path, { credentials: 'same-origin' });
          result[path] = { failed: false, status: response.status };
        } catch {
          result[path] = { failed: true, status: 0 };
        }
      }
      return result;
    }, NON_BLANK_Q_PATHS);
    for (const path of NON_BLANK_Q_PATHS) {
      expect(offlineQ[path].failed, `offline non-blank q must fail: ${path}`).toBe(true);
      expect(await cacheHit(page, path), `offline non-blank q had a cache replay: ${path}`).toBeNull();
    }

    const offlineDetail = await fetchFromPage(page, DETAIL_PATH);
    expect(offlineDetail.ok, 'offline detail fetch must fail (network-only)').toBe(false);
    expect(await cacheHit(page, DETAIL_PATH), 'offline detail must have no cache entry').toBeNull();

    // 地址栏离线直达 detail：新标签页真实导航必须失败，且仍无缓存回放。
    const probe = await context.newPage();
    const navFailure = await probe.goto(DETAIL_PATH, { waitUntil: 'domcontentloaded' })
      .then(() => null)
      .catch((error: unknown) => error);
    expect(navFailure, 'offline address-bar detail navigation must fail').toBeTruthy();
    await probe.close();

    const offlineSnapshot = await recordCacheSnapshot(page, 'offline-after-address-bar');
    const offlineText = allCacheText(offlineSnapshot);
    expect(entriesWithPath(offlineSnapshot, DETAIL_PATH), 'offline cache contains detail URL').toEqual([]);
    expect(offlineText, 'offline cache contains admin detail marker').not.toContain(marker.value);
    expect(
      snapshotHasCacheWithBody(offlineSnapshot, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'offline traversal must not delete another app cache',
    ).toBe(true);
  });

  test('login and account switch await slow cleanup, logout clears bodies, offline /learn stays public-only', async ({ page, context }) => {
    await page.goto('/favicon.svg');
    await seedOtherAppCache(page);
    await installSlowDeleteHarness(page);
    await page.goto('/login');
    await waitForServiceWorker(page);
    await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });

    // 登录页当前有 active registration，但模拟「无 controller」时刻：清理必须
    // 回退到 registration.active，仍然在导航前完成。
    const controllerNull = await page.evaluate(() => {
      Object.defineProperty(navigator.serviceWorker, 'controller', {
        configurable: true,
        get: () => null,
      });
      return navigator.serviceWorker.controller === null;
    });
    expect(controllerNull, 'no-controller harness did not apply').toBe(true);

    await seedOldUserCacheFixture(page);
    const beforeLogin = await recordCacheSnapshot(page, 'before-login-fixture');
    expect(
      snapshotHasCacheWithBody(beforeLogin, OLD_USER_CACHE, OLD_USER_MARKER),
      'pre-login stale fixture was not seeded',
    ).toBe(true);

    await loginViaUi(page);
    const afterLogin = await recordCacheSnapshot(page, 'after-slow-login');
    expect(
      snapshotHasCacheWithBody(afterLogin, OLD_USER_CACHE, OLD_USER_MARKER),
      'slow cleanup did not finish before login navigation',
    ).toBe(false);
    expect(allCacheText(afterLogin), 'stale user marker survived slow login').not.toContain(OLD_USER_MARKER);
    const loginHarness = {
      navStarted: await readHarnessFlag(page, HARNESS_NAV_KEY),
      deleteAfterNavigation: await readHarnessFlag(page, HARNESS_DELETE_AFTER_NAV_KEY),
    };
    await attachEvidence('slow-login-cleanup-order', loginHarness);
    expect(loginHarness.navStarted, 'login navigation was not observed by history harness').toBe('1');
    expect(loginHarness.deleteAfterNavigation,
      'page cache delete completed after login navigation (signIn was not awaited)').toBeNull();

    // 真实 API 详情/练习反馈用于记录本题真实敏感标记。
    const detail = await fetchFromPage(page, DETAIL_PATH);
    expect(detail.ok && detail.status === 200, 'authenticated detail fetch failed').toBe(true);
    const marker = await getAdminSensitiveMarker(page, detail.body, JSON.parse(detail.body));
    await attachEvidence('switch-account-sensitive-marker', marker);

    // 公开目录预热，并写入旧会话私有 fixture（随后由真实账号切换清理）。
    await warmPublicTaxonomy(page);
    await seedOldUserCacheFixture(page);
    const taxCache = await taxonomyCacheName(page);
    if (taxCache) {
      await seedMarkerIntoCache(page, taxCache, `${OLD_USER_MARKER}|${marker.fragment}`);
    }
    const beforeSwitch = await recordCacheSnapshot(page, 'before-account-switch');
    expect(allCacheText(beforeSwitch), 'switch fixture was not seeded').toContain(OLD_USER_MARKER);

    // 从另一端替换 cookie 为 Jamie，再真实重载触发 AuthContext 的 user-id 变化清理。
    const users = JSON.parse((await fetchFromPage(page, '/api/users')).body) as Array<{ id: number; name: string }>;
    const jamie = users.find((user) => user.name === 'Jamie');
    expect(jamie, 'seeded Jamie profile was not found').toBeTruthy();
    const switchLogin = await fetchFromPage(page, '/api/auth/login', {
      method: 'POST',
      body: JSON.stringify({ user_id: jamie!.id, pin: SEEDED_PINS[2] }),
    });
    expect(switchLogin.ok, 'real API account switch login failed').toBe(true);
    expect(switchLogin.status, 'real API account switch login status').toBe(200);
    await page.goto('/?linked=1');
    await expect(page.getByRole('button', { name: meButtonLabel('Jamie') })).toBeVisible({ timeout: 45_000 });
    const afterSwitch = await recordCacheSnapshot(page, 'after-slow-account-switch');
    expect(entriesWithPath(afterSwitch, DETAIL_PATH), 'different-user detail URL survived account switch').toEqual([]);
    expect(entriesWithPath(afterSwitch, '/api/taxonomy/practice'),
      'different-user practice feedback survived account switch').toEqual([]);
    expect(allCacheText(afterSwitch), 'old-user sensitive marker survived account switch')
      .not.toContain(OLD_USER_MARKER);
    expect(allCacheText(afterSwitch), 'old-user detail marker survived account switch')
      .not.toContain(marker.value);
    expect(
      snapshotHasCacheWithBody(afterSwitch, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'account switch deleted another app cache',
    ).toBe(true);

    // 登出前再种一次旧用户/敏感 fixture，验证登出清理枚举全部 cache 正文。
    await seedOldUserCacheFixture(page);
    await seedMarkerIntoCache(page, OLD_USER_CACHE, `${OLD_USER_MARKER}|${marker.fragment}`);
    const beforeLogout = await recordCacheSnapshot(page, 'before-logout');
    expect(allCacheText(beforeLogout), 'logout fixture was not seeded').toContain(OLD_USER_MARKER);

    await page.getByRole('button', { name: meButtonLabel('Jamie') }).first().click();
    await page.getByRole('button', { name: SIGN_OUT_LABEL }).first().click();
    await page.waitForURL((url) => url.pathname === '/login', { timeout: 30_000 });
    const afterLogout = await recordCacheSnapshot(page, 'after-logout');
    expect(entriesWithPath(afterLogout, DETAIL_PATH), 'detail URL survived logout').toEqual([]);
    expect(allCacheText(afterLogout), 'old-user marker survived logout').not.toContain(OLD_USER_MARKER);
    expect(allCacheText(afterLogout), 'admin detail marker survived logout').not.toContain(marker.value);
    expect(
      snapshotHasCacheWithBody(afterLogout, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'logout deleted another app cache',
    ).toBe(true);

    // 清 cookie（不读取/输出任何 cookie 值），并显式确认会话 cookie 名称已消失。
    await context.clearCookies();
    const cookieNames = (await context.cookies()).map((cookie) => cookie.name);
    expect(cookieNames, 'session cookie survived explicit cookie clear').not.toContain('openchore_session');

    // 在线重新导航一次，恢复公开壳/静态资源；随后离线 /learn 只能看到公开内容。
    await page.goto('/login');
    await expect.poll(() => cachedShellReady(page), { timeout: 20_000, message: 'shell cache was not restored online' })
      .toBe(true);
    await expect.poll(() => hasCachedAssets(page), { timeout: 20_000, message: 'static assets were not cached online' })
      .toBe(true);

    await context.setOffline(true);
    const learnNavigation = await page.goto('/learn', { waitUntil: 'domcontentloaded' }).catch((error: unknown) => error);
    expect(learnNavigation, 'offline /learn navigation should be served by the public shell').not.toBeInstanceOf(Error);
    await expect(page.locator('#root')).not.toBeEmpty({ timeout: 30_000 });
    await expect(page.locator('[data-test="offline-learn-notice"]')).toBeVisible({ timeout: 30_000 });

    const offlineBodyText = await page.locator('body').innerText();
    expect(offlineBodyText, 'offline /learn rendered old-user data').not.toContain(OLD_USER_MARKER);
    expect(offlineBodyText, 'offline /learn rendered admin detail answer').not.toContain(marker.value);

    const offlineLearnCaches = await recordCacheSnapshot(page, 'offline-learn-caches');
    expect(entriesWithPath(offlineLearnCaches, DETAIL_PATH), 'offline /learn cache has detail URL').toEqual([]);
    expect(allCacheText(offlineLearnCaches), 'offline /learn cache has old-user marker').not.toContain(OLD_USER_MARKER);
    expect(allCacheText(offlineLearnCaches), 'offline /learn cache has admin detail marker').not.toContain(marker.value);
    expect(await cacheHit(page, DETAIL_PATH), 'offline /learn had a detail cache match').toBeNull();
    expect(
      snapshotHasCacheWithBody(offlineLearnCaches, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'offline /learn traversal deleted another app cache',
    ).toBe(true);

    const offlineDetail = await fetchFromPage(page, DETAIL_PATH);
    expect(offlineDetail.ok, 'offline /learn detail fetch must fail').toBe(false);
  });

  test('a slow stale /auth/me response cannot resurrect the signed-out user', async ({ page, context }) => {
    await goToLoginAndWaitForWorker(page);
    await loginViaUi(page);

    let slowMeStarted = false;
    await page.route('**/api/auth/me', async (route) => {
      // 只做真实响应的延迟转发（pass-through），不伪造任何 API 响应。
      slowMeStarted = true;
      await new Promise((resolve) => setTimeout(resolve, 2_000));
      await route.continue();
    });

    // 用真实浏览器网络状态翻转触发 App 的 offline→online refresh（不是合成事件）：
    // 先等 offline 真正渲染，再恢复在线，让 300ms 防抖后的 /auth/me 被延迟。
    await context.setOffline(true);
    await expect.poll(() => page.evaluate(() => navigator.onLine), {
      timeout: 10_000,
      message: 'browser never reported offline',
    }).toBe(false);
    await page.waitForTimeout(150);
    await context.setOffline(false);
    await expect.poll(() => page.evaluate(() => navigator.onLine), {
      timeout: 10_000,
      message: 'browser never reported online again',
    }).toBe(true);
    await expect.poll(() => slowMeStarted, {
      timeout: 10_000,
      message: 'offline→online refresh never issued /auth/me',
    }).toBe(true);

    // 旧 refresh 在途时登出；旧响应返回后不得把 Alex 重新写回会话/页面。
    await page.getByRole('button', { name: meButtonLabel('Alex') }).first().click();
    await page.getByRole('button', { name: SIGN_OUT_LABEL }).first().click();
    await page.waitForURL((url) => url.pathname === '/login', { timeout: 30_000 });
    await page.waitForTimeout(2_800);
    await expect(page).toHaveURL((url) => url.pathname === '/login');
    await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });
    await expect(page.getByRole('button', { name: meButtonLabel('Alex') })).toHaveCount(0);
    await attachEvidence('stale-auth-after-logout', {
      url: page.url(),
      slowMeStarted,
    });
    await page.unroute('**/api/auth/me');
  });

  test('CacheStorage.delete failures are bounded and do not block online login', async ({ page }) => {
    const pageErrors: string[] = [];
    page.on('pageerror', (error) => pageErrors.push(String(error)));

    await page.goto('/favicon.svg');
    await installDeleteFailureHarness(page);
    await page.goto('/login');
    await waitForServiceWorker(page);
    await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });
    await seedOldUserCacheFixture(page);

    await loginViaUi(page);
    const failures = Number(await readHarnessFlag(page, HARNESS_DELETE_FAILURES_KEY) || '0');
    await attachEvidence('cache-delete-failure', {
      injectedDeleteFailures: failures,
      pageErrors,
    });
    expect(failures, 'CacheStorage.delete failure injection never hit an openchore cache').toBeGreaterThan(0);
    // 失败被 withBound/deleteCacheQuietly 吞掉：认证照常完成，不产生 pageerror。
    await page.waitForTimeout(1_000);
    expect(pageErrors, 'cache delete failure produced an unhandled page error').toEqual([]);
    await expect(page.getByRole('button', { name: meButtonLabel('Alex') })).toBeVisible({ timeout: 30_000 });
  });

  test('CacheStorage unavailable still falls back to online login', async ({ page }) => {
    await page.goto('/favicon.svg');
    await page.addInitScript(() => {
      try {
        Object.defineProperty(window, 'caches', { configurable: true, get: () => undefined });
      } catch {
        // 无法移除时由下方探测显式 skip，不伪称覆盖。
      }
    });
    await page.goto('/login');
    const cachesUnavailable = await page.evaluate(() => typeof caches === 'undefined');
    if (!cachesUnavailable) {
      test.skip(true, '技术阻塞：Chromium 不允许把 window.caches 置为 undefined，无法真实驱动该降级路径');
      return;
    }
    await waitForServiceWorker(page);
    await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });
    await loginViaUi(page);
    await expect(page.getByRole('button', { name: meButtonLabel('Alex') })).toBeVisible({ timeout: 30_000 });
  });

  test('a superseded signIn must not navigate away from /login', async ({ page }) => {
    await page.goto('/favicon.svg');
    await installSlowDeleteHarness(page);
    await page.goto('/login');
    await waitForServiceWorker(page);
    await expect(page.getByRole('button', { name: doorLabel('Alex') })).toBeVisible({ timeout: 30_000 });

    const mainFrameNavigations: string[] = [];
    page.on('framenavigated', (frame) => {
      if (frame === page.mainFrame()) mainFrameNavigations.push(frame.url());
    });

    await page.getByRole('button', { name: doorLabel('Alex') }).click();
    await enterPin(page, SEEDED_PINS[1]);

    // 登录响应回来后 signIn 先 await 慢清理；清理一开始就触发 SESSION_EXPIRED，
    // 让随后完成的 signIn 变成被取代的旧认证。
    await expect.poll(() => readHarnessFlag(page, HARNESS_CLEAR_STARTED_KEY), {
      timeout: 15_000,
      message: 'login cleanup never started',
    }).toBe('1');
    await page.evaluate(() => window.dispatchEvent(new Event('openchore:session-expired')));

    // 覆盖慢清理两轮 + 总 deadline 的最坏路径，避免在 navigate 尚未发生前
    // 就断言“未导航”而假绿。
    await page.waitForTimeout(8_500);
    const navStarted = await readHarnessFlag(page, HARNESS_NAV_KEY);
    await attachEvidence('superseded-sign-in', { navStarted, mainFrameNavigations, url: page.url() });
    expect(navStarted, 'superseded signIn still called navigate("/")').toBeNull();
    const rootNavigations = mainFrameNavigations.filter((url) => {
      try {
        return new URL(url).pathname === '/';
      } catch {
        return false;
      }
    });
    expect(rootNavigations, 'superseded signIn navigated the main frame to /').toEqual([]);
    await expect(page).toHaveURL((url) => url.pathname === '/login');
    await expect(page.getByRole('button', { name: meButtonLabel('Alex') })).toHaveCount(0);
  });

  test('two direct SW CLEAR messages remove app caches without touching another app cache', async ({ page }) => {
    await goToLoginAndWaitForWorker(page);
    await loginViaUi(page);
    await warmPublicTaxonomy(page);
    const taxCache = await taxonomyCacheName(page);
    expect(taxCache, 'taxonomy cache was not created by the public warm-up').not.toBeNull();
    await seedMarkerIntoCache(page, taxCache!, OLD_USER_MARKER);
    const beforeClear = await recordCacheSnapshot(page, 'before-double-clear');
    expect(snapshotHasCacheWithBody(beforeClear, taxCache!, OLD_USER_MARKER),
      'double-clear fixture was not seeded').toBe(true);
    expect(await hasCachedAssets(page), 'static cache was not present before double clear').toBe(true);

    // 两次真实 CLEAR，每次都 await SW 的 DONE ack；验证双轮删除与其它应用隔离。
    await sendClearToServiceWorker(page);
    await sendClearToServiceWorker(page);
    await expect.poll(() => taxonomyCacheName(page), {
      timeout: 20_000,
      message: 'taxonomy cache survived double CLEAR',
    }).toBeNull();
    await expect.poll(() => hasCachedAssets(page), {
      timeout: 20_000,
      message: 'static cache survived double CLEAR',
    }).toBe(false);
    const afterClear = await recordCacheSnapshot(page, 'after-double-clear');
    expect(allCacheText(afterClear), 'double CLEAR left the seeded marker').not.toContain(OLD_USER_MARKER);
    expect(
      snapshotHasCacheWithBody(afterClear, OTHER_APP_CACHE, OTHER_APP_MARKER),
      'double CLEAR deleted another app cache',
    ).toBe(true);
  });

  test('a response already in flight during double CLEAR cannot repopulate the taxonomy cache', async ({ page, context }) => {
    await goToLoginAndWaitForWorker(page);
    await loginViaUi(page);
    // 统一从空白目录开始：目标直接匹配 cacheHit，不能依赖 UI 是否请求过。
    await page.goto('/');
    const target = '/api/taxonomy/topics?subject=math&limit=1';

    // 真实删除失败/在途写入不可观测的兜底：先确认目标没有预置缓存。
    expect(await cacheHit(page, target), 'target cache existed before in-flight test').toBeNull();

    const cdp = await context.newCDPSession(page);
    await cdp.send('Network.enable');
    await cdp.send('Network.emulateNetworkConditions', {
      offline: false,
      latency: 1_500,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });

    let fetchDoneAt = 0;
    const startedAt = Date.now();
    const pendingFetch = page.evaluate(async (url) => {
      const response = await fetch(url, { credentials: 'same-origin' });
      return { status: response.status, length: (await response.text()).length };
    }, target).then((result) => {
      fetchDoneAt = Date.now();
      return result;
    });

    // 等 SW 发起真实网络请求，再发两轮 CLEAR；CLEAR 必须早于慢响应返回。
    await page.waitForTimeout(250);
    await sendClearToServiceWorker(page);
    await sendClearToServiceWorker(page);
    const clearDoneAt = Date.now();
    const onlineFetch = await pendingFetch;

    const evidence = {
      target,
      startedAt,
      clearDoneAt,
      fetchDoneAt,
      onlineFetch,
      inFlightWindowMs: fetchDoneAt - clearDoneAt,
    };
    await attachEvidence('clear-in-flight-response', evidence);
    if (fetchDoneAt - clearDoneAt < 200) {
      // 延迟没有真正覆盖请求窗口：不伪称覆盖，显式 pending。
      test.skip(true, '技术阻塞：CDP 网络延迟未能让 SW 的 taxonomy fetch 在双 CLEAR 完成时仍在途');
      return;
    }

    await page.waitForTimeout(500);
    expect(await cacheHit(page, target), 'in-flight network response resurrected the taxonomy cache').toBeNull();
    const snapshot = await cacheSnapshot(page);
    expect(entriesOf(snapshot).filter((entry) => entry.url.includes('/api/taxonomy/topics') && entry.url.includes('limit=1')),
      'in-flight response left a forbidden topics cache entry').toEqual([]);
    await cdp.send('Network.emulateNetworkConditions', {
      offline: false,
      latency: 0,
      downloadThroughput: -1,
      uploadThroughput: -1,
    });
    await cdp.detach();
  });

  test('install shell redirect/CORS rejection cannot be driven in the frozen preview (pending)', async () => {
    test.info().annotations.push({
      type: 'pending',
      description: 'install 的 / 响应闸门需要让真实 preview 的 / 返回 3xx 或 CORS；冻结环境不允许改 web/vite.config.ts 或生产 SW，三文件所有权内无法可靠构造，保留给 production coder/reviewer 的定向 harness。',
    });
    test.skip(true, '技术阻塞：无法在冻结 preview 上真实制造 install 的 redirect/CORS 壳响应');
  });
});
