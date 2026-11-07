// PWA Service Worker 真实浏览器 E2E helper（TX-PWA-REAL-E2E-V2）。
// 只使用真实浏览器：不 mock、不拦截 API、不伪造响应。这些 helper 仅用于
// 观察 CacheStorage、写入隐私 fixture、等待生产 SW 接管。
import { expect, test, type Page } from '@playwright/test';

/** 缓存全量快照：每个 cache 名、每个 request key 与响应正文。 */
export interface CacheSnapshot {
  caches: Array<{
    name: string;
    entries: Array<{ url: string; status: number; body: string }>;
  }>;
}

/** 页面当前可见的全部 cache 名（已排序）。 */
export function cacheKeys(page: Page): Promise<string[]> {
  return page.evaluate(async () => {
    if (typeof caches === 'undefined') return [];
    return (await caches.keys()).sort();
  });
}

/** 属于其它应用的 cache 与 marker；activate 不得删除它们。 */
export const OTHER_APP_CACHE = 'other-app-private-cache-e2e';
export const OTHER_APP_MARKER = 'other-app-owned';

/**
 * 写入一个名字不以 `openchore-` 开头的 cache，使 activate 的清理可被观察，
 * 同时不削弱本应用自身 cache 的断言。
 */
export async function seedOtherAppCache(page: Page): Promise<void> {
  await page.evaluate(async ({ name, marker }) => {
    const cache = await caches.open(name);
    await cache.put(new Request('/other-app/marker'), new Response(marker, {
      headers: { 'Content-Type': 'text/plain' },
    }));
  }, { name: OTHER_APP_CACHE, marker: OTHER_APP_MARKER });
}

/**
 * 代表上一用户/上一会话残留私有数据的 cache fixture。
 * 它是本应用自有的 Cache Storage 条目，绝不是被拦截的 API 响应。
 */
export const OLD_USER_CACHE = 'openchore-legacy-private-e2e';
export const OLD_USER_MARKER = 'TX-PWA-OLD-USER-PRIVATE-MARKER-6d9e27c4';
const OLD_USER_FIXTURE_URL = '/e2e-cache-fixture/old-user';

export async function seedOldUserCacheFixture(
  page: Page,
  marker: string = OLD_USER_MARKER,
): Promise<void> {
  await page.evaluate(async ({ name, body, url }) => {
    const cache = await caches.open(name);
    await cache.put(new Request(url), new Response(body, {
      headers: { 'Content-Type': 'text/plain' },
    }));
  }, { name: OLD_USER_CACHE, body: marker, url: OLD_USER_FIXTURE_URL });
}

/** 枚举 caches.keys()、每个 cache.keys() 以及每个响应正文。 */
export function cacheSnapshot(page: Page): Promise<CacheSnapshot> {
  return page.evaluate(async () => {
    if (typeof caches === 'undefined') return { caches: [] };
    const names = (await caches.keys()).sort();
    const result: Array<{ name: string; entries: Array<{ url: string; status: number; body: string }> }> = [];
    for (const name of names) {
      const cache = await caches.open(name);
      const requests = await cache.keys();
      const entries = await Promise.all(requests.map(async (request) => {
        const response = await cache.match(request);
        return {
          url: request.url,
          status: response?.status ?? -1,
          body: response ? await response.clone().text() : '',
        };
      }));
      result.push({ name, entries });
    }
    return { caches: result };
  });
}

/** 向 Playwright 报告附加精简、可核查的证据。 */
export async function attachEvidence(label: string, value: unknown): Promise<void> {
  await test.info().attach(`pwa-evidence:${label}`, {
    body: JSON.stringify(value, null, 2),
    contentType: 'application/json',
  });
}

/** 附加快照摘要；正文只留在内存中用于隐私断言，不落盘。 */
export async function recordCacheSnapshot(page: Page, label: string): Promise<CacheSnapshot> {
  const snapshot = await cacheSnapshot(page);
  const summary = {
    cacheCount: snapshot.caches.length,
    entryCount: snapshot.caches.reduce((sum, cache) => sum + cache.entries.length, 0),
    caches: snapshot.caches.map((cache) => ({
      name: cache.name,
      keys: cache.entries.map((entry) => entry.url),
      bodySizes: cache.entries.map((entry) => entry.body.length),
    })),
  };
  await attachEvidence(`cache-snapshot:${label}`, summary);
  return snapshot;
}

/** 展平全部 URL 与缓存响应正文，供隐私断言检索。 */
export function allCacheText(snapshot: CacheSnapshot): string {
  return snapshot.caches.flatMap((cache) => [
    cache.name,
    ...cache.entries.flatMap((entry) => [entry.url, entry.body]),
  ]).join('\n');
}

/** 指定 cache 中是否仍有正文包含 marker 的条目。 */
export function snapshotHasCacheWithBody(
  snapshot: CacheSnapshot,
  cacheName: string,
  marker: string,
): boolean {
  return snapshot.caches.some(
    (cache) => cache.name === cacheName && cache.entries.some((entry) => entry.body.includes(marker)),
  );
}

/**
 * 仅返回公开目录响应正文（subjects 与无 q 的 topics）。
 * 注意：q 用 String.trim() 判空，U+FEFF/零宽字符不算空白，因此本 helper
 * 只用于公开目录正文断言，不能替代非空 q 的 network-only 断言。
 */
export function publicTaxonomyBodies(snapshot: CacheSnapshot): Array<{
  cache: string;
  url: string;
  body: string;
}> {
  const result: Array<{ cache: string; url: string; body: string }> = [];
  for (const cache of snapshot.caches) {
    for (const entry of cache.entries) {
      let url: URL;
      try {
        url = new URL(entry.url);
      } catch {
        continue;
      }
      const qIsBlank = url.searchParams.getAll('q').every((value) => value.trim() === '');
      const isPublicDirectory = url.pathname === '/api/taxonomy/subjects'
        || (url.pathname === '/api/taxonomy/topics' && qIsBlank);
      if (isPublicDirectory) result.push({ cache: cache.name, url: entry.url, body: entry.body });
    }
  }
  return result;
}

/** 等待生产 SW 注册、activated 并接管页面（reload 一次拿到 controller）。 */
export async function waitForServiceWorker(page: Page, timeout = 20_000): Promise<void> {
  await expect
    .poll(
      async () => page.evaluate(async () => {
        if (!('serviceWorker' in navigator)) return null;
        const registration = await navigator.serviceWorker.getRegistration();
        return registration?.active?.state ?? null;
      }),
      { timeout, message: 'service worker never reached activated state' },
    )
    .toBe('activated');
  await page.reload();
  await expect
    .poll(
      async () => page.evaluate(() => navigator.serviceWorker.controller !== null),
      { timeout, message: 'service worker never took control' },
    )
    .toBeTruthy();
}

/** 生产 taxonomy cache 名（版本号变化不影响断言），不存在返回 null。 */
export async function taxonomyCacheName(page: Page): Promise<string | null> {
  return (await cacheKeys(page)).find((name) => name.startsWith('openchore-taxonomy-')) ?? null;
}

/** 在全部 cache 中查找某个 URL 的命中条目，未命中返回 null。 */
export function cacheHit(page: Page, url: string): Promise<{ status: number; body: string } | null> {
  return page.evaluate(async (u) => {
    if (typeof caches === 'undefined') return null;
    const hit = await caches.match(u);
    return hit === undefined ? null : { status: hit.status, body: await hit.text() };
  }, url);
}

/** 等待 SW 的 cache.put 通过 CacheStorage.match 可观测。 */
export async function waitForCacheHit(page: Page, url: string, timeout = 20_000): Promise<void> {
  await expect
    .poll(() => cacheHit(page, url), { timeout, message: `cache entry never landed: ${url}` })
    .not.toBeNull();
}

/** 任一本应用静态 cache 中是否已有真实 Vite /assets/... 条目。 */
export function hasCachedAssets(page: Page): Promise<boolean> {
  return page.evaluate(async () => {
    if (typeof caches === 'undefined') return false;
    const names = (await caches.keys()).filter((name) => name.startsWith('openchore-static-'));
    for (const name of names) {
      const cache = await caches.open(name);
      const keys = await cache.keys();
      if (keys.some((request) => request.url.includes('/assets/'))) return true;
    }
    return false;
  });
}

/** 往已有本应用 cache 写入 marker fixture，不拦截任何 API。 */
export async function seedMarkerIntoCache(
  page: Page,
  cacheName: string,
  marker: string,
): Promise<void> {
  await page.evaluate(async ({ name, body, url }) => {
    const cache = await caches.open(name);
    await cache.put(new Request(url), new Response(body, {
      headers: { 'Content-Type': 'text/plain' },
    }));
  }, { name: cacheName, body: marker, url: '/e2e-cache-fixture/double-clear' });
}

/** 直接向 SW 发一条 CLEAR，并等待 DONE ack（或 2.5s 超时）。 */
export function sendClearToServiceWorker(page: Page): Promise<void> {
  return page.evaluate(async () => {
    const sw = navigator.serviceWorker.controller;
    if (!sw) throw new Error('no controlling service worker');
    await new Promise<void>((resolve) => {
      const channel = new MessageChannel();
      const timer = setTimeout(resolve, 2_500);
      channel.port1.onmessage = (event) => {
        if (event.data?.type === 'CLEAR_TAXONOMY_CACHES_DONE') {
          clearTimeout(timer);
          resolve();
        }
      };
      sw.postMessage({ type: 'CLEAR_TAXONOMY_CACHES' }, [channel.port2]);
    });
  });
}
