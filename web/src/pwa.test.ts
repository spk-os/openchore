// @vitest-environment jsdom
// PWA 注册器单测（卡 TX-PWA-FIX / TX-SW-ONLY）：
// - 生产环境才注册 SW，开发环境跳过；
// - clearTaxonomyCaches() 两轮向 SW postMessage（每轮等待 DONE 回执或
//   有界超时）再逐轮兜底删除所有 openchore-* cache；
// - 无 serviceWorker 支持时安全降级、无 secure context 仍照常删除；
// - SW 的 fetch 路由规则：subjects、q 全部空白的 topics 列表可缓存；
//   带任一非空/重复/编码 q 的 topics、topics/{ref} 详情与诊断/导出等一律
//   network-only（不读不写缓存、离线不回放）；
// - SW 的 CLEAR 先自增 epoch、有界等待在途 put settle、删缓存再 ack，
//   在途 put 落地不复活缓存；HTML fallback 保留安全头、过滤逐跳/非白名单头；
// - 静态缓存先排除 /api/、/uploads/、/tts/ 等私有前缀与可疑路径再按
//   显式 allowlist 匹配：/api/private.pdf、/uploads/private.png 不缓存；
// - no-store / private 响应在 networkFirst 与 stale-while-revalidate
//   两条路径下都不进缓存；
// - CLEAR_TAXONOMY_CACHES 同时清 taxonomy 与 static 缓存；
// - 所有 cache.put 是 fire-and-forget 且 catch：put reject 不产生
//   unhandled rejection；
// - 导航离线回退用 cache.open().match() 而不是非法的 caches.match(url,
//   {cacheName})；
// - 导航离线回退响应规范化为非空 text/html：空壳条目不回放成空白 200
//   （继续走外层 catch 返回 Response.error()）；
// - 壳引用的构建 JS/CSS 离线时从静态缓存回放（stale-while-revalidate
//   命中，后台刷新失败被吞）。

import { afterEach, describe, expect, it, vi } from 'vitest';
import { clearTaxonomyCaches, registerServiceWorker } from './pwa';

/** 直接改写 import.meta.env.PROD（vi.stubEnv 会把 'false' 当真值，见 vitest#issue）。 */
function stubProd(value: boolean): void {
  (import.meta.env as Record<string, unknown>).PROD = value;
}

/** 可控 MessagePort：port2.postMessage 直接同步触发 port1.onmessage。 */
interface FakePort {
  onmessage: ((event: { data: unknown }) => void) | null;
  onmessageerror: ((event: unknown) => void) | null;
  close: ReturnType<typeof vi.fn>;
  start: ReturnType<typeof vi.fn>;
  postMessage: ReturnType<typeof vi.fn>;
}
interface FakePortPair {
  port1: FakePort;
  port2: MessagePort;
}

/**
 * 可控 MessageChannel 替身：pwa.ts 内部 new 出来的两条通道都留在 pairs 里，
 * 测试可在 postMessage 调用里直接经转移过来的 port2 回 DONE（同步、无 4s
 * 真实定时器等待），也可触发非 DONE / onmessageerror / 超时降级。
 */
function stubControllableMessageChannel(pairs: FakePortPair[]): void {
  class ControllableMessageChannel {
    port1: FakePort;
    port2: MessagePort;
    constructor() {
      const port1: FakePort = {
        onmessage: null,
        onmessageerror: null,
        close: vi.fn(),
        start: vi.fn(),
        postMessage: vi.fn(),
      };
      const port2 = {
        postMessage: vi.fn((data: unknown) => {
          port1.onmessage?.({ data });
        }),
      } as unknown as MessagePort;
      this.port1 = port1;
      this.port2 = port2;
      pairs.push({ port1, port2 });
    }
  }
  vi.stubGlobal('MessageChannel', ControllableMessageChannel);
}

/** 两轮 CLEAR 都经转移端口同步回 DONE 的假 SW。 */
class AckingSW {
  ack: Record<string, unknown>;
  constructor(ack: Record<string, unknown> = { epoch: 1, deleted: [], failed: [] }) {
    this.ack = ack;
  }
  postMessage = vi.fn((_msg: unknown, transfer: unknown[]) => {
    const port = (transfer as MessagePort[])[0];
    port.postMessage({ type: 'CLEAR_TAXONOMY_CACHES_DONE', ...this.ack });
  });
}

class FakeSW {
  postMessage = vi.fn();
}

function stubServiceWorker(active: FakeSW | null) {
  vi.stubGlobal('navigator', {
    serviceWorker: {
      register: vi.fn(async () => ({ scope: '/' })),
      controller: active,
      getRegistration: vi.fn(async () => (active ? { active } : undefined)),
    },
  });
}

/** 页面侧 caches 替身，返回被删除的 cache 名清单以便断言两轮兜底。 */
function stubPageCaches(names: string[]): string[] {
  const deleted: string[] = [];
  vi.stubGlobal('caches', {
    keys: vi.fn(async () => names),
    delete: vi.fn(async (n: string) => {
      deleted.push(n);
      return true;
    }),
  });
  return deleted;
}

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.resetModules();
});

describe('registerServiceWorker', () => {
  it('生产环境注册 /openchore-sw.js', async () => {
    // vitest 默认 PROD=false，改成 true 验证注册分支。
    stubProd(true);
    stubServiceWorker(null);
    const reg = await registerServiceWorker();
    expect(reg).not.toBeNull();
    expect(navigator.serviceWorker.register).toHaveBeenCalledWith('/openchore-sw.js');
    stubProd(false);
  });

  it('开发环境（PROD=false）跳过注册', async () => {
    stubProd(false);
    const register = vi.fn(async () => ({ scope: '/' }));
    vi.stubGlobal('navigator', { serviceWorker: { register } });
    expect(await registerServiceWorker()).toBeNull();
    expect(register).not.toHaveBeenCalled();
  });

  it('注册失败返回 null 而不是抛错', async () => {
    vi.stubGlobal('navigator', {
      serviceWorker: { register: vi.fn(async () => { throw new Error('nope'); }) },
    });
    expect(await registerServiceWorker()).toBeNull();
  });

  it('无 serviceWorker 支持时返回 null', async () => {
    vi.stubGlobal('navigator', {});
    expect(await registerServiceWorker()).toBeNull();
  });
});

describe('clearTaxonomyCaches', () => {
  it('两轮 CLEAR 均带正确 type 与转移 port，DONE 回执后逐轮删缓存（不等 4 秒）', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    const sw = new AckingSW();
    stubServiceWorker(sw);
    const deleted = stubPageCaches(['openchore-taxonomy-v4', 'openchore-static-v4']);

    await clearTaxonomyCaches();

    // DEF-PWA-CLEAR-REVIVAL：两轮复用同一 SW 实例，各自 postMessage 都带
    // MessageChannel 的 port2；每轮经 DONE 同步完成，不触发 2s 真实超时。
    expect(sw.postMessage).toHaveBeenCalledTimes(2);
    expect(pairs).toHaveLength(2);
    sw.postMessage.mock.calls.forEach((call, i) => {
      const [msg, transfer] = call as [unknown, unknown];
      expect(msg).toEqual({ type: 'CLEAR_TAXONOMY_CACHES' });
      expect(Array.isArray(transfer)).toBe(true);
      expect((transfer as unknown[])).toHaveLength(1);
      expect((transfer as MessagePort[])[0]).toBe(pairs[i].port2);
      expect(pairs[i].port2.postMessage).toHaveBeenCalledWith({
        type: 'CLEAR_TAXONOMY_CACHES_DONE',
        epoch: 1,
        deleted: [],
        failed: [],
      });
      expect(pairs[i].port1.close).toHaveBeenCalledTimes(1);
    });
    // 每轮 ack 之后各删一遍：两轮共四次 openchore-* 删除。
    expect(deleted).toEqual([
      'openchore-taxonomy-v4', 'openchore-static-v4',
      'openchore-taxonomy-v4', 'openchore-static-v4',
    ]);
    // 两个 port1/timer 都收尾干净，无遗留定时器。
    expect(pairs.every((p) => p.port1.close.mock.calls.length === 1)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('非 DONE 消息不算 ack：等待 DONE 才完成第一轮并进入第二轮', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    const sw = new FakeSW();
    stubServiceWorker(sw);
    stubPageCaches([]);

    const cleared = clearTaxonomyCaches();
    await vi.advanceTimersByTimeAsync(0);
    expect(sw.postMessage).toHaveBeenCalledTimes(1);
    // 非 DONE、null data、无 type：一律不推进，也不关端口。
    pairs[0].port2.postMessage({ type: 'SKIP_WAITING' });
    pairs[0].port2.postMessage({ type: 'CLEAR_TAXONOMY_CACHES_PROGRESS' });
    pairs[0].port2.postMessage(null);
    pairs[0].port2.postMessage({ epoch: 1 });
    await vi.advanceTimersByTimeAsync(0);
    expect(sw.postMessage).toHaveBeenCalledTimes(1);
    expect(pairs[0].port1.close).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(1);
    // 正确的 DONE 才推进到第二轮；第二轮由 messageerror 有界降级收尾。
    pairs[0].port2.postMessage({ type: 'CLEAR_TAXONOMY_CACHES_DONE', epoch: 1, deleted: [], failed: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(sw.postMessage).toHaveBeenCalledTimes(2);
    pairs[1].port2.postMessage({ type: 'SKIP_WAITING' });
    pairs[1].port1.onmessageerror?.({});
    await cleared;
    expect(pairs[1].port1.close).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('messageerror 立即降级；两轮均无 DONE 时各有界 2s 超时且清理 port/timer', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    const sw = new FakeSW();
    stubServiceWorker(sw);
    const deleted = stubPageCaches(['openchore-taxonomy-v4']);

    const cleared = clearTaxonomyCaches();
    await vi.advanceTimersByTimeAsync(0);
    expect(sw.postMessage).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1999);
    expect(sw.postMessage).toHaveBeenCalledTimes(1);
    expect(pairs[0].port1.close).not.toHaveBeenCalled();
    // 第一轮 2s 超时：立即进入第二轮，不永久阻塞。
    await vi.advanceTimersByTimeAsync(1);
    expect(sw.postMessage).toHaveBeenCalledTimes(2);
    expect(pairs[0].port1.close).toHaveBeenCalledTimes(1);
    // 第二轮用 messageerror 降级（不依赖真实 2s 等待）。
    pairs[1].port1.onmessageerror?.({});
    await cleared;
    expect(deleted).toEqual(['openchore-taxonomy-v4', 'openchore-taxonomy-v4']);
    expect(pairs.every((p) => p.port1.close.mock.calls.length === 1)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('postMessage 抛错（SW 已终止）也立即降级：两轮兜底、port/timer 清理干净', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    const sw = new FakeSW();
    sw.postMessage.mockImplementation(() => {
      throw new DOMException('SW terminated', 'InvalidStateError');
    });
    stubServiceWorker(sw);
    const deleted = stubPageCaches(['openchore-taxonomy-v4']);

    await clearTaxonomyCaches();

    expect(sw.postMessage).toHaveBeenCalledTimes(2);
    expect(deleted).toEqual(['openchore-taxonomy-v4', 'openchore-taxonomy-v4']);
    expect(pairs).toHaveLength(2);
    expect(pairs.every((p) => p.port1.close.mock.calls.length === 1)).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('无 SW 支持时清理如实报告失败（CacheStorage 不可用无从核实，后续可重试）', async () => {
    vi.stubGlobal('navigator', {});
    vi.stubGlobal('caches', undefined);
    await expect(clearTaxonomyCaches()).resolves.toBe(false);
  });

  it('页面侧兜底删除所有 openchore-* cache（含旧 taxonomy 私有详情残留）', async () => {
    stubServiceWorker(null);
    const del = vi.fn(async () => true);
    vi.stubGlobal('caches', {
      keys: vi.fn(async () => [
        'openchore-taxonomy-v1', // 旧版本 SW：可能残留带 answer 的 topics/{ref} 详情
        'openchore-taxonomy-v0',
        'openchore-static-v1', // 旧版本 SW：可能按后缀误存私有响应
        'some-other-app-cache', // 非本应用：不得删除
      ]),
      delete: del,
    });
    await clearTaxonomyCaches();
    expect(del).toHaveBeenCalledWith('openchore-taxonomy-v1');
    expect(del).toHaveBeenCalledWith('openchore-taxonomy-v0');
    expect(del).toHaveBeenCalledWith('openchore-static-v1');
    expect(del).not.toHaveBeenCalledWith('some-other-app-cache');
  });

  it('非 secure context 仍照常删除页面缓存（隐私清理不受 isSecureContext 约束）', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    stubServiceWorker(new AckingSW());
    const deleted = stubPageCaches(['openchore-taxonomy-v4', 'openchore-static-v4', 'other-app']);
    const original = Object.getOwnPropertyDescriptor(window, 'isSecureContext');
    Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });

    await clearTaxonomyCaches();

    expect(window.isSecureContext).toBe(false);
    expect(deleted).toEqual([
      'openchore-taxonomy-v4', 'openchore-static-v4',
      'openchore-taxonomy-v4', 'openchore-static-v4',
    ]);
    expect(vi.getTimerCount()).toBe(0);
    if (original) Object.defineProperty(window, 'isSecureContext', original);
  });
});


// ---------------------------------------------------------------------------
// Service Worker 脚本本体（web/public/openchore-sw.js）的路由规则测试。
// 文件是自执行脚本，用 eval 在隔离环境里装载，捕获 fetch/message 事件处理器。
// 契约版本：DEF-PWA-PRODUCTION-R2 第二轮（SW v4：install 完整壳闸门、
// activate/CLEAR 真实删除结果、在线降级、缓存最终 URL/type/redirect 闸门）。
// ---------------------------------------------------------------------------

const V4 = 'v4';
const SHELL_CACHE_V4 = `openchore-shell-${V4}`;
const STATIC_CACHE_V4 = `openchore-static-${V4}`;
const TAXONOMY_CACHE_V4 = `openchore-taxonomy-${V4}`;
const ORIGIN = 'https://openchore.local';

interface SWHarness {
  fetchHandler: ((event: { request: RequestLike; respondWith: (p: Promise<Response>) => void }) => void) | null;
  messageHandler: ((event: {
    data: unknown;
    waitUntil: (p: Promise<unknown>) => void;
    ports?: MessagePort[];
  }) => void) | null;
  installHandler: ((event: { waitUntil: (p: Promise<unknown>) => void }) => void) | null;
  activateHandler: ((event: { waitUntil: (p: Promise<unknown>) => void }) => void) | null;
  selfRef: Record<string, unknown>;
  listenersAdded: Record<string, number>;
  /** 每次装载 SW 时刷新的假 caches 实现，测试可注入行为。 */
  fakeCaches: {
    open: ReturnType<typeof vi.fn>;
    keys: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
    match: ReturnType<typeof vi.fn>;
  };
  /** 每次装载 SW 时刷新的假 fetch，测试可注入离线/响应行为。 */
  fakeFetch: ReturnType<typeof vi.fn>;
  /**
   * 装载时从 SW 顶层闭包捕获的内部状态/纯判定函数，用于断言 CLEAR 代数、
   * 在途 put 集合与空白控制字符正则等无法从 respondWith 直接观察的契约。
   */
  internals: {
    inFlightPuts: Set<unknown>;
    getEpoch: () => number;
    hasBlankOrControlChars: (path: string) => boolean;
    topicsQueryIsBlank: (url: URL) => boolean;
  };
}

/** fetch 事件里的 request：真 Request 或导航 stub（mode: 'navigate' 无法手动构造）。 */
type RequestLike = Request | NavRequest;

interface FakeCache {
  match: ReturnType<typeof vi.fn>;
  add: ReturnType<typeof vi.fn>;
  put: ReturnType<typeof vi.fn>;
}

/**
 * unhandledRejection spy。Node process 事件用 node:process 拿（测试环境跑在
 * node worker 上），避免给 DOM lib 加 @types/node。
 */
function nodeProcess(): {
  on: (ev: string, fn: (reason: unknown) => void) => unknown;
  off: (ev: string, fn: (reason: unknown) => void) => unknown;
} {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const procAny = (globalThis as any).process;
  return procAny as ReturnType<typeof nodeProcess>;
}

function registerUnhandledRejectionSpy(fn: (reason: unknown) => void): void {
  nodeProcess().on('unhandledRejection', fn);
}

function unregisterUnhandledRejectionSpy(fn: (reason: unknown) => void): void {
  nodeProcess().off('unhandledRejection', fn);
}

/** 构造一个 put/match 可注入的 FakeCache。 */
function makeCache(
  impl: { match?: (req: unknown) => Promise<Response | undefined>; put?: ReturnType<typeof vi.fn> } = {},
): FakeCache {
  return {
    match: vi.fn(impl.match ?? (async () => undefined)),
    add: vi.fn(async () => undefined),
    put: impl.put ?? vi.fn(async () => undefined),
  };
}

/** 只对指定 cache 名返回注入的 FakeCache；其余返回空缓存。 */
function routeCache(sw: SWHarness, name: string, fc: FakeCache): void {
  sw.fakeCaches.open.mockImplementation(async (n: string) => {
    if (n === name) return fc;
    return makeCache();
  });
}

function jsonResponse(body = '{}', headers: Record<string, string> = {}): Response {
  return new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

function htmlResponse(body = '<html>net</html>', headers: Record<string, string> = {}): Response {
  return new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'text/html; charset=utf-8', ...headers },
  });
}

async function loadSW(): Promise<SWHarness> {
  // vitest 下 import.meta.url 指向 dev server（http://localhost:…），fetch public/
  // 会拿到 "Not found."。改为直接用 node:fs 读源文件（jsdom 环境跑在 node 里，
  // 模块可用），动态 import 规避 TS 的 node types 依赖。
  const pathMod: { resolve: (...p: string[]) => string; dirname: (p: string) => string } =
    (await import(/* @vite-ignore */ ('node:path' as string))) as never;
  const fsMod: { readFileSync: (p: string, enc: string) => string } =
    (await import(/* @vite-ignore */ ('node:fs' as string))) as never;
  const here = import.meta.url.replace(/^file:\/\//, '');
  const swPath = pathMod.resolve(pathMod.dirname(here), '../public/openchore-sw.js');
  const source = fsMod.readFileSync(swPath, 'utf-8');
  const listeners: Record<string, number> = {};
  const listenersAdded: Record<string, number> = {};
  const handlers: Record<string, Function> = {};
  const selfRef: Record<string, unknown> = {
    location: new URL(`${ORIGIN}/`),
    skipWaiting: () => Promise.resolve(),
    clients: { claim: () => Promise.resolve() },
    addEventListener: (type: string, fn: Function) => {
      listeners[type] = (listeners[type] ?? 0) + 1;
      listenersAdded[type] = (listenersAdded[type] ?? 0) + 1;
      handlers[type] = fn;
    },
  };
  const fakeCaches = {
    open: vi.fn(async () => makeCache()),
    keys: vi.fn(async () => [] as string[]),
    delete: vi.fn(async () => true),
    match: vi.fn(async () => undefined),
  };
  const fakeFetch = vi.fn(async () => jsonResponse());
  const scope = {
    self: selfRef,
    caches: fakeCaches,
    fetch: fakeFetch,
    Request,
    Response,
  };
  (globalThis as Record<string, unknown>).__sw_scope = scope;
  const internalsKey = `__sw_internals_${Math.random().toString(36).slice(2)}`;
  // 脚本本体不含任何测试钩子；eval 时在 with(this) 的同一词法作用域末尾追加
  // 一行，把顶层 const/函数句柄暴露给断言（仅测试可见，不改实现）。
  const probe = `globalThis[${JSON.stringify(internalsKey)}] = {
    inFlightPuts,
    getEpoch: () => cacheEpoch,
    hasBlankOrControlChars,
    topicsQueryIsBlank,
  };`;
  const proc = `(function(){ with(this){ ${source}\n; ${probe}\n } }).call(globalThis.__sw_scope)`;
  // eslint-disable-next-line no-eval
  (0, eval)(proc);
  const internals = (globalThis as Record<string, unknown>)[internalsKey] as SWHarness['internals'];
  delete (globalThis as Record<string, unknown>)[internalsKey];
  delete (globalThis as Record<string, unknown>).__sw_scope;
  return {
    fetchHandler: (handlers.fetch as SWHarness['fetchHandler']) ?? null,
    messageHandler: (handlers.message as SWHarness['messageHandler']) ?? null,
    installHandler: (handlers.install as SWHarness['installHandler']) ?? null,
    activateHandler: (handlers.activate as SWHarness['activateHandler']) ?? null,
    selfRef,
    listenersAdded,
    fakeCaches,
    fakeFetch,
    internals,
  };
}

/** 触发一次 fetch 事件并等到 respondWith 的 promise settle。 */
async function dispatchFetch(
  sw: SWHarness,
  request: RequestLike,
): Promise<{ respondWith: ReturnType<typeof vi.fn>; response: unknown; error: unknown }> {
  const respondWith = vi.fn();
  const fetchHandler = sw.fetchHandler as NonNullable<SWHarness['fetchHandler']>;
  fetchHandler({ request, respondWith });
  expect(respondWith).toHaveBeenCalledTimes(1);
  const p = (respondWith.mock.calls[0] as [Promise<Response>])[0];
  let response: unknown;
  let error: unknown;
  try {
    response = await p;
  } catch (e) {
    error = e;
  }
  return { response, respondWith, error };
}

/**
 * 触发一次 fetch 事件，断言没有 respondWith（直连原生网络：不读不写缓存）。
 * 同时断言 SW 既没碰 caches 也没调 fetch——fail closed 是彻底的。
 */
function expectPassthrough(sw: SWHarness, request: RequestLike): void {
  const respondWith = vi.fn();
  (sw.fetchHandler as NonNullable<SWHarness['fetchHandler']>)({ request, respondWith });
  expect(respondWith).not.toHaveBeenCalled();
  expect(sw.fakeCaches.open).not.toHaveBeenCalled();
  expect(sw.fakeFetch).not.toHaveBeenCalled();
}

/** 触发一次导航 fetch 事件，断言没有 respondWith。 */
function navPassthrough(sw: SWHarness, path: string): void {
  expectPassthrough(sw, navRequest(path));
}

function sendClear(
  sw: SWHarness,
  port?: { postMessage: ReturnType<typeof vi.fn> },
): Promise<unknown> {
  const waitUntil = vi.fn();
  (sw.messageHandler as NonNullable<SWHarness['messageHandler']>)({
    data: { type: 'CLEAR_TAXONOMY_CACHES' },
    waitUntil,
    ports: port ? [port as unknown as MessagePort] : [],
  });
  expect(waitUntil).toHaveBeenCalledTimes(1);
  return (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
}

/**
 * 模拟导航请求。Node/undici 的 Request 构造器不允许手动构造
 * mode: 'navigate'（浏览器专属），导航分支只读 method/mode/url，
 * 用最小 stub 即可。
 */
interface NavRequest {
  method: string;
  mode: string;
  url: string;
  destination: string;
}
function navRequest(path: string): NavRequest {
  return { method: 'GET', mode: 'navigate', url: `${ORIGIN}${path}`, destination: 'document' };
}

describe('openchore-sw.js 四个监听器', () => {
  it('注册 install/activate/fetch/message 四个监听器', async () => {
    const sw = await loadSW();
    expect(sw.installHandler).toBeTruthy();
    expect(sw.activateHandler).toBeTruthy();
    expect(sw.fetchHandler).toBeTruthy();
    expect(sw.messageHandler).toBeTruthy();
  });

  it('VERSION=v4：安装写 openchore-shell-v4', async () => {
    const sw = await loadSW();
    sw.fakeFetch.mockImplementation(async () => htmlResponse('<html>install</html>'));
    const skipWaiting = vi.fn(() => Promise.resolve());
    (sw.selfRef as { skipWaiting: unknown }).skipWaiting = skipWaiting;
    const waitUntil = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(sw.fakeCaches.open).toHaveBeenCalledWith(SHELL_CACHE_V4);
    expect(skipWaiting).toHaveBeenCalled();
  });

  it('install 只有 HTML 响应才写壳缓存', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    // 默认 fakeFetch 返回 JSON（模拟 API/服务端异常响应落到 '/'）。
    const waitUntil = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(sw.fakeFetch).toHaveBeenCalledTimes(1);
    expect(shell.put).not.toHaveBeenCalled();
  });

  it('install HTML 响应写壳缓存（键 "/"），且不用 cache.add', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => htmlResponse('<html>install</html>'));
    const waitUntil = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(shell.put).toHaveBeenCalledTimes(1);
    const key = (shell.put.mock.calls[0] as unknown[])[0];
    expect(keyUrlOf(key)).toBe(`${ORIGIN}/`);
    expect(shell.add).not.toHaveBeenCalled();
  });

  it('install 期间发生 CLEAR：在途响应不把已删壳缓存复活', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    let resolveFetch: (r: Response) => void = () => {};
    sw.fakeFetch.mockImplementation(
      () => new Promise<Response>((resolve) => { resolveFetch = resolve; }),
    );
    const waitUntilInstall = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil: waitUntilInstall });
    const installDone = (waitUntilInstall.mock.calls[0] as [Promise<unknown>])[0];
    await sendClear(sw);
    resolveFetch(htmlResponse('<html>late</html>'));
    await installDone;
    expect(shell.put).not.toHaveBeenCalled();
  });

  it('activate 删除所有 openchore-* 旧代（含 v3/v2/v1）但不动其它 app cache 与 v4', async () => {
    const sw = await loadSW();
    const names = [
      'openchore-shell-v4',
      'openchore-static-v4',
      'openchore-taxonomy-v4',
      // v2 的 static 缓存可能按后缀误存过私有响应，v2 shell 可能被写入过
      // 非 HTML——整体废弃。
      'openchore-shell-v2',
      'openchore-static-v2',
      'openchore-taxonomy-v2',
      // v1 残留：taxonomy 里可能有带 answer 的 topics/{ref} 详情。
      'openchore-shell-v1',
      'openchore-static-v1',
      'openchore-taxonomy-v1',
      'openchore-shell-v0-old',
      // 其它应用：绝不动。
      'some-other-app-cache',
      'openchorex-cache',
      'MyApp-openchore-v1',
      'workbox-precache-v2',
    ];
    sw.fakeCaches.keys.mockImplementation(async () => names);
    const claim = vi.fn(() => Promise.resolve());
    (sw.selfRef as { clients: unknown }).clients = { claim };
    const waitUntil = vi.fn();
    (sw.activateHandler as NonNullable<SWHarness['activateHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    const deleted = sw.fakeCaches.delete.mock.calls.map((c) => (c as unknown[])[0]);
    for (const old of [
      'openchore-shell-v2',
      'openchore-static-v2',
      'openchore-taxonomy-v2',
      'openchore-shell-v1',
      'openchore-static-v1',
      'openchore-taxonomy-v1',
      'openchore-shell-v0-old',
    ]) {
      expect(deleted).toContain(old);
    }
    for (const keep of [
      'openchore-shell-v4',
      'openchore-static-v4',
      'openchore-taxonomy-v4',
      'some-other-app-cache',
      'openchorex-cache',
      'MyApp-openchore-v1',
      'workbox-precache-v2',
    ]) {
      expect(deleted).not.toContain(keep);
    }
    expect(claim).toHaveBeenCalled();
  });

  it('message CLEAR_TAXONOMY_CACHES 清 v4 taxonomy 与 static，不清 shell', async () => {
    const sw = await loadSW();
    await sendClear(sw);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(STATIC_CACHE_V4);
    expect(sw.fakeCaches.delete).not.toHaveBeenCalledWith(SHELL_CACHE_V4);
  });

  it('CLEAR 回执在 SW 自身删除完成之后发出，且 epoch 递增', async () => {
    const sw = await loadSW();
    const order: string[] = [];
    sw.fakeCaches.delete.mockImplementation(async (n: string) => {
      order.push(`delete:${n}`);
      return true;
    });
    const port = {
      postMessage: vi.fn((msg: { type: string; epoch: number }) => {
        order.push(`ack:${msg.type}:${msg.epoch}`);
      }),
    };
    await sendClear(sw, port);
    expect(port.postMessage).toHaveBeenCalledWith({
      type: 'CLEAR_TAXONOMY_CACHES_DONE',
      epoch: 1,
      deleted: [TAXONOMY_CACHE_V4, STATIC_CACHE_V4],
      failed: [],
    });
    const ackIdx = order.findIndex((s) => s.startsWith('ack:'));
    expect(ackIdx).toBe(order.length - 1);
    expect(order.slice(0, ackIdx)).toEqual(
      expect.arrayContaining([`delete:${TAXONOMY_CACHE_V4}`, `delete:${STATIC_CACHE_V4}`]),
    );
  });

  it('连续 CLEAR 时 epoch 单调递增', async () => {
    const sw = await loadSW();
    const ack = vi.fn();
    await sendClear(sw, { postMessage: ack });
    await sendClear(sw, { postMessage: ack });
    await sendClear(sw, { postMessage: ack });
    const epochs = ack.mock.calls.map((c) => (c[0] as { epoch: number }).epoch);
    expect(epochs).toEqual([1, 2, 3]);
  });

  it('无 port 时照常清理、不回执、不抛错', async () => {
    const sw = await loadSW();
    await expect(sendClear(sw)).resolves.toBeUndefined();
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
  });

  it('port 已关闭（postMessage 抛错）时清理仍完成、不抛错', async () => {
    const sw = await loadSW();
    const port = {
      postMessage: vi.fn(() => {
        throw new DOMException('closed', 'InvalidStateError');
      }),
    };
    await expect(sendClear(sw, port)).resolves.toBeUndefined();
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
  });

  it('非 CLEAR 消息被忽略', async () => {
    const sw = await loadSW();
    const waitUntil = vi.fn();
    (sw.messageHandler as NonNullable<SWHarness['messageHandler']>)({
      data: { type: 'SKIP_WAITING' },
      waitUntil,
    });
    expect(waitUntil).not.toHaveBeenCalled();
    expect(sw.fakeCaches.delete).not.toHaveBeenCalled();
  });

  // -------------------------------------------------------------------------
  // TX-PWA-SW-PRIVACY-FIX2：CLEAR epoch / in-flight put 屏障 / 有界 1500ms。
  // -------------------------------------------------------------------------

  it('FIX2: CLEAR 先自增 epoch，再等慢 put settle，之后才 delete + ack，且 put 不复活缓存', async () => {
    const sw = await loadSW();
    const order: string[] = [];
    let resolvePut: () => void = () => {};
    const put = vi.fn(() => new Promise<void>((resolve) => {
      order.push('put:start');
      resolvePut = () => { order.push('put:settle'); resolve(); };
    }));
    routeCache(sw, TAXONOMY_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"subjects":[]}'));
    // 网络响应已回来、put 已发起但尚未 settle；发起时 epoch=0。
    const pendingFetch = dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(sw.internals.inFlightPuts.size).toBe(1);

    sw.fakeCaches.delete.mockImplementation(async (n: string) => {
      order.push(`delete:${n}`);
      return true;
    });
    const port = {
      postMessage: vi.fn((msg: { type: string; epoch: number }) => {
        order.push(`ack:${msg.type}:${msg.epoch}`);
      }),
    };
    const work = sendClear(sw, port);
    // 同步处理消息：epoch 立即自增（旧写入立即作废），不等待慢 put。
    expect(sw.internals.getEpoch()).toBe(1);
    expect(sw.fakeCaches.delete).not.toHaveBeenCalled();
    expect(port.postMessage).not.toHaveBeenCalled();
    // 不推进真实时间：慢 put 未 settle 时 CLEAR 必须保持挂起（有界等待的
    // 起点），delete/ack 都不得提前发生。
    await Promise.resolve();
    expect(sw.fakeCaches.delete).not.toHaveBeenCalled();
    expect(port.postMessage).not.toHaveBeenCalled();

    // put 落地：登记集合随即摘除，删除与 ack 才继续。
    resolvePut();
    await work;
    expect(sw.internals.inFlightPuts.size).toBe(0);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(STATIC_CACHE_V4);
    expect(port.postMessage).toHaveBeenCalledWith({
      type: 'CLEAR_TAXONOMY_CACHES_DONE',
      epoch: 1,
      deleted: [TAXONOMY_CACHE_V4, STATIC_CACHE_V4],
      failed: [],
    });
    expect(order.indexOf('put:settle')).toBeLessThan(
      Math.min(...[`delete:${TAXONOMY_CACHE_V4}`, `delete:${STATIC_CACHE_V4}`].map((s) => order.indexOf(s))),
    );
    expect(order.at(-1)).toContain('ack:');
    // 旧 epoch 的写入只有一次（clear 前发起的），clear 后不得复活缓存。
    expect(put).toHaveBeenCalledTimes(1);
    const { error } = await pendingFetch;
    expect(error).toBeUndefined();
    // CLEAR 之后新请求仍可写缓存（epoch 防护不误伤后续会话）。
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(put).toHaveBeenCalledTimes(2);
  });

  it('FIX2: 挂起 put 最多等 1500ms 即降级删除 + ack（有界、不永久阻塞）', async () => {
    const sw = await loadSW();
    // 超时窗口内不 settle 的 put：模拟存储后端挂死；超时后可手动落地以验证
    // 集合摘除与"不复活"。
    let resolvePut: () => void = () => {};
    const put = vi.fn(() => new Promise<void>((resolve) => { resolvePut = resolve; }));
    routeCache(sw, TAXONOMY_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{}'));
    const pendingFetch = dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    await vi.waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(sw.internals.inFlightPuts.size).toBe(1);

    const port = { postMessage: vi.fn() };
    vi.useFakeTimers();
    const work = sendClear(sw, port);
    expect(sw.internals.getEpoch()).toBe(1);
    await vi.advanceTimersByTimeAsync(1499);
    expect(sw.fakeCaches.delete).not.toHaveBeenCalled();
    expect(port.postMessage).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await work;
    vi.useRealTimers();
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(STATIC_CACHE_V4);
    expect(port.postMessage).toHaveBeenCalledWith({
      type: 'CLEAR_TAXONOMY_CACHES_DONE',
      epoch: 1,
      deleted: [TAXONOMY_CACHE_V4, STATIC_CACHE_V4],
      failed: [],
    });
    // 挂起的 put 尚未 settle，集合仍登记它；清理已完成、epoch 已递增，
    // 其后续落地也不会写缓存（超时降级不复活）。
    expect(sw.internals.inFlightPuts.size).toBe(1);
    expect(put).toHaveBeenCalledTimes(1);
    const { error } = await pendingFetch;
    expect(error).toBeUndefined();
    // 超时后 put 最终落地：settle 必摘除，集合不复活、不无界增长。
    resolvePut();
    await vi.waitFor(() => expect(sw.internals.inFlightPuts.size).toBe(0));
    expect(sw.fakeCaches.delete).toHaveBeenCalledTimes(2);
  });

  it('FIX2: 正常 put settle 后 inFlightPuts 摘除，CLEAR 不再等待、集合不复活', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, TAXONOMY_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{}'));
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    await vi.waitFor(() => expect(sw.internals.inFlightPuts.size).toBe(0));

    const port = { postMessage: vi.fn() };
    vi.useFakeTimers();
    const work = sendClear(sw, port);
    // 无在途 put：第一个 timer tick 都不用等，microtask 即完成删除与 ack。
    await work;
    vi.useRealTimers();
    expect(port.postMessage).toHaveBeenCalledTimes(1);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
    expect(sw.internals.inFlightPuts.size).toBe(0);
    expect(sw.internals.getEpoch()).toBe(1);
    // CLEAR 后的新请求写入会在其 settle 时再次摘除，集合不增长。
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    await vi.waitFor(() => expect(sw.internals.inFlightPuts.size).toBe(0));
  });
});

describe('openchore-sw.js taxonomy 白名单', () => {
  const ALLOW_PATHS = [
    '/api/taxonomy/subjects',
    '/api/taxonomy/topics',
    '/api/taxonomy/topics?subject=math',
    // TX-PWA-SW-PRIVACY-FIX2：q 缺失/全部空白（含 +、%20、制表符编码、
    // 重复 q 全部空白）仍按无 q 目录端点缓存，不剥离 query 统一 key。
    '/api/taxonomy/topics?q=',
    '/api/taxonomy/topics?q=%20',
    '/api/taxonomy/topics?q=+',
    '/api/taxonomy/topics?q=%09%0A',
    '/api/taxonomy/topics?q=&q=%20&q=+',
    '/api/taxonomy/topics?subject=math&q=%20',
  ];
  it.each(ALLOW_PATHS)('白名单 GET %s 走 respondWith（可缓存）', async (path) => {
    const sw = await loadSW();
    const respondWith = vi.fn();
    (sw.fetchHandler as NonNullable<SWHarness['fetchHandler']>)({
      request: new Request(`${ORIGIN}${path}`),
      respondWith,
    });
    expect(respondWith).toHaveBeenCalledTimes(1);
  });

  // network-only：一律不 respondWith（不缓存、离线不回放旧缓存）。
  const DENY_PATHS = [
    // 个人导出与分享（含敏感数据）
    '/api/taxonomy/export',
    '/api/taxonomy/export?user_id=1',
    '/api/taxonomy/shared/abc123',
    // 诊断面
    '/api/taxonomy/diagnostics',
    '/api/taxonomy/diagnostics?user_id=1',
    '/api/taxonomy/diagnostics/3',
    '/api/taxonomy/diagnostics/3/import-learning',
    // 盲区
    '/api/taxonomy/blindspots',
    // 管理面
    '/api/admin/taxonomy/courses',
    '/api/admin/taxonomy/import',
    // 白名单之外的其它 taxonomy 路径
    '/api/taxonomy/practice',
    '/api/taxonomy/subjects/extra',
    '/api/taxonomy',
    '/api/taxonomy/',
    '/api/taxonomy/topics/',
    // topics 大小写变体不在白名单
    '/api/taxonomy/Subjects',
    '/api/taxonomy/TOPICS',
    // DEF-PWA-PRIVACY：topics 详情含角色差异化数据（admin 注入
    // answer/accept/explanation），共享缓存即越权面，一律 network-only。
    '/api/taxonomy/topics/cnm_p001',
    '/api/taxonomy/topics/cnm_p001/practice',
    // TX-PWA-SW-PRIVACY-FIX2：q 任一非空（含重复 q 中任一非空、百分号
    // 编码后解析为非空）一律 network-only，绝不剥离 query 统一 key。
    '/api/taxonomy/topics?q=math',
    '/api/taxonomy/topics?q=%E4%B8%80',
    '/api/taxonomy/topics?q=%20x',
    '/api/taxonomy/topics?q=+x',
    '/api/taxonomy/topics?q=&q=math',
    '/api/taxonomy/topics?q=math&q=',
  ];
  it.each(DENY_PATHS)('network-only 路径 %s 不缓存（不读不写缓存、不走网络代理）', async (path) => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
  });

  it('FIX2: 大写 Q 不是 q 参数，仍按目录端点缓存（URLSearchParams 语义）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, TAXONOMY_CACHE_V4, makeCache({ put }));
    const { response, error } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/topics?Q=math`),
    );
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).toHaveBeenCalledTimes(1);
  });

  const ENCODED_DENY_PATHS = [
    // %2F 编码绕过：解码后是 /api/taxonomy/diagnostics/3/import-learning
    '/api/taxonomy%2Fdiagnostics%2F3%2Fimport-learning',
    // export 用 %65 编码：解码后 pathname 变化 → 不匹配白名单
    '/api/taxonomy/%65xport',
    // shared 用 %73 编码
    '/api/taxonomy/%73hared/abc123',
    // topics 用 %74 编码：解码后仍不匹配白名单（详情本就不在白名单）
    '/api/taxonomy/%74opics/cnm_p001',
    // subjects 用 %73 编码变体绕过尝试（解码后是 /api/taxonomy/subjects/extra）
    '/api/taxonomy/%73ubjects/extra',
    // 畸形编码：%zz 不可解码
    '/api/taxonomy/topics/%zz',
    // 畸形编码：裸 %
    '/api/taxonomy/topics/%',
    // %2F 作为 topics 详情 ref：解码后含 / → 不安全
    '/api/taxonomy/topics/foo%2Fbar',
    // %2e%2e 路径穿越尝试
    '/api/taxonomy/topics/%2e%2e%2fadmin',
    // safeSegment 类相对引用（详情本就不缓存，这里防编码变体混入）
    '/api/taxonomy/topics/.',
    '/api/taxonomy/topics/..',
    // 详情 ref 的编码变体：%63nm_p001 解码后即 cnm_p001，同样不缓存
    '/api/taxonomy/topics/%63nm_p001',
    // 双重编码：%2563nm_p001 一次 decode 后仍是 %63nm_p001（含 %），不匹配白名单
    '/api/taxonomy/topics/%2563nm_p001',
  ];
  it.each(ENCODED_DENY_PATHS)('编码/变体路径 %s 不缓存', async (path) => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
  });

  it('非 GET 请求一律直连', async () => {
    const sw = await loadSW();
    expectPassthrough(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`, { method: 'POST' }),
    );
    expectPassthrough(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.js`, { method: 'PUT' }),
    );
  });

  it('跨域请求直连', async () => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request('https://evil.example.com/assets/a.js'));
    expectPassthrough(sw, new Request('https://evil.example.com/api/taxonomy/subjects'));
  });
});

describe('openchore-sw.js 缓存写入与离线回放', () => {
  /** 配置 taxonomy v4 缓存。 */
  function primeCache(
    sw: SWHarness,
    impl: { match?: (req: unknown) => Promise<Response | undefined>; put?: ReturnType<typeof vi.fn> },
  ): FakeCache {
    const cache = makeCache(impl);
    routeCache(sw, TAXONOMY_CACHE_V4, cache);
    return cache;
  }

  // -------------------------------------------------------------------------
  // TX-PWA-SW-PRIVACY-FIX2 正式用例：缓存/不缓存都要求"不读不写"与键保持
  // 原始 URL（绝不剥离非空 q 统一 key）。
  // -------------------------------------------------------------------------

  it('FIX2: subjects 在线写缓存，离线读同一 URL 的缓存回放', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    const cached = jsonResponse('{"subjects":[1]}');
    primeCache(sw, { put, match: async () => cached });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"subjects":[]}'));
    const online = await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(online.error).toBeUndefined();
    expect(put).toHaveBeenCalledTimes(1);
    expect((put.mock.calls[0] as unknown[])[0]).toBeInstanceOf(Request);
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('offline'); });
    const offline = await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(offline.response).toBe(cached);
  });

  /** 空白 q 的可缓存 topics：既写缓存、离线回放，put/match 键保留原始 query。 */
  const BLANK_Q_PATHS = [
    '/api/taxonomy/topics',
    '/api/taxonomy/topics?q=',
    '/api/taxonomy/topics?q=%20',
    '/api/taxonomy/topics?q=+',
    '/api/taxonomy/topics?q=%09%0A',
    '/api/taxonomy/topics?q=&q=%20&q=+',
    '/api/taxonomy/topics?subject=math&q=%20',
  ];
  it.each(BLANK_Q_PATHS)('FIX2: 空白 q %s 可缓存（写缓存/离线回放，键保留原 URL）', async (path) => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    const cached = jsonResponse('{"topics":[1]}');
    const match = vi.fn(async () => cached);
    primeCache(sw, { put, match });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"topics":[]}'));
    const online = await dispatchFetch(sw, new Request(`${ORIGIN}${path}`));
    expect(online.error).toBeUndefined();
    expect(put).toHaveBeenCalledTimes(1);
    expect(keyUrlOf((put.mock.calls[0] as unknown[])[0])).toBe(`${ORIGIN}${path}`);
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('offline'); });
    const offline = await dispatchFetch(sw, new Request(`${ORIGIN}${path}`));
    expect(offline.response).toBe(cached);
    expect(keyUrlOf((match.mock.calls.at(-1) as unknown[])[0])).toBe(`${ORIGIN}${path}`);
  });

  /** 任一非空 q（重复/编码）与详情：彻底不读不写缓存，网络也不被代理。 */
  const NON_CACHEABLE_TAXONOMY_PATHS = [
    '/api/taxonomy/topics?q=math',
    '/api/taxonomy/topics?q=%E4%B8%80',
    '/api/taxonomy/topics?q=%20x',
    '/api/taxonomy/topics?q=+x',
    '/api/taxonomy/topics?q=&q=math',
    '/api/taxonomy/topics?q=math&q=',
    '/api/taxonomy/topics?subject=math&q=1',
    '/api/taxonomy/topics?q=math&q=%20',
    '/api/taxonomy/topics/cnm_p001',
    '/api/taxonomy/topics/%63nm_p001',
    '/api/taxonomy/topics/cnm_p001?q=',
  ];
  it.each(NON_CACHEABLE_TAXONOMY_PATHS)(
    'FIX2: %s network-only（不 respondWith、不读不写缓存、不代理网络）',
    async (path) => {
      const sw = await loadSW();
      const put = vi.fn(async () => undefined);
      const match = vi.fn(async () => jsonResponse('{"stale":true}'));
      routeCache(sw, TAXONOMY_CACHE_V4, makeCache({ put, match }));
      sw.fakeFetch.mockImplementation(async () => jsonResponse('{"answer":"42"}'));
      expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
      expect(sw.fakeCaches.open).not.toHaveBeenCalled();
      expect(put).not.toHaveBeenCalled();
      expect(match).not.toHaveBeenCalled();
    },
  );

  it('FIX2: topicsQueryIsBlank 按 URLSearchParams 语义判定（重复/编码/空白/大小写）', async () => {
    const sw = await loadSW();
    const blank = (q: string) => sw.internals.topicsQueryIsBlank(new URL(`${ORIGIN}/api/taxonomy/topics?${q}`));
    expect(blank('')).toBe(true);
    expect(blank('q=')).toBe(true);
    expect(blank('q=%20&q=+&q=%09')).toBe(true);
    expect(blank('q=%E4%B8%80')).toBe(false);
    expect(blank('q=&q=x')).toBe(false);
    expect(blank('q=x&q=')).toBe(false);
    expect(blank('Q=x')).toBe(true); // 大写 Q 不是 q 参数
    expect(blank('subject=math')).toBe(true); // 其它参数不影响闸门
  });

  it('FIX2: 带非空 q 的响应绝不剥离 query 写进无 q 的 topics key', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"private":true}'));
    expectPassthrough(sw, new Request(`${ORIGIN}/api/taxonomy/topics?q=math`));
    expect(put).not.toHaveBeenCalled();
    // 独立的无 q 请求仍可写缓存，但键是自己的 URL，不含被剥离的搜索请求。
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/topics`));
    expect(put).toHaveBeenCalledTimes(1);
    expect(keyUrlOf((put.mock.calls[0] as unknown[])[0])).toBe(`${ORIGIN}/api/taxonomy/topics`);
  });

  it('FIX2: 带非空 q 的 topics 离线不回放无 q 目录缓存', async () => {
    const sw = await loadSW();
    const cached = jsonResponse('{"topics":["public"]}');
    const match = vi.fn(async () => cached);
    primeCache(sw, { match });
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('offline'); });
    expectPassthrough(sw, new Request(`${ORIGIN}/api/taxonomy/topics?q=math`));
    expect(match).not.toHaveBeenCalled();
  });

  it('FIX2: admin detail（含答案）在线返回也不写缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"answer":"42"}'));
    expectPassthrough(sw, new Request(`${ORIGIN}/api/taxonomy/topics/cnm_p001`));
    expect(put).not.toHaveBeenCalled();
  });

  it('白名单 subjects 在线时写 taxonomy v4 缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"subjects":[]}'));
    const { response, error } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).toHaveBeenCalledTimes(1);
  });

  it('白名单 subjects 离线时回放缓存', async () => {
    const sw = await loadSW();
    const cached = jsonResponse('{"subjects":[]}');
    primeCache(sw, { match: async () => cached });
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response, error } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(error).toBeUndefined();
    expect(response).toBe(cached);
  });

  it('白名单 topics 列表离线且无缓存时返回 Response.error()', async () => {
    const sw = await loadSW();
    primeCache(sw, {});
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/topics`),
    );
    expect(response).toBeInstanceOf(Response);
    expect((response as Response).status).toBe(0);
    expect((response as Response).type).toBe('error');
  });

  it('no-store 响应不进缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{}', { 'Cache-Control': 'no-store' }));
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('private 响应不进缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () =>
      jsonResponse('{}', { 'Cache-Control': 'private, max-age=0' }));
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('CLEAR 后已在途的网络响应不得把已删缓存复活（epoch 防护）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    // fetch 挂起：响应尚未回来（发起时 epoch=0）。
    let resolveFetch: (r: Response) => void = () => {};
    sw.fakeFetch.mockImplementation(
      () => new Promise<Response>((resolve) => { resolveFetch = resolve; }),
    );
    const pending = dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    // 期间发生登出清理：CLEAR 先自增 epoch 再删缓存。
    await sendClear(sw);
    expect(sw.fakeCaches.delete).toHaveBeenCalledWith(TAXONOMY_CACHE_V4);
    // 在途响应这时才回来：epoch 已变，绝不重新 put。
    resolveFetch(jsonResponse('{}'));
    const { response, error } = await pending;
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('CLEAR 后已在途的静态响应也不复活 static 缓存（epoch 防护）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    let resolveFetch: (r: Response) => void = () => {};
    sw.fakeFetch.mockImplementation(
      () => new Promise<Response>((resolve) => { resolveFetch = resolve; }),
    );
    const pending = dispatchFetch(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.js`),
    );
    await sendClear(sw);
    resolveFetch(new Response('js', { status: 200, headers: { 'Content-Type': 'application/javascript' } }));
    const { response, error } = await pending;
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('CLEAR 后已在途的导航 HTML 也不复活壳缓存（epoch 防护）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, SHELL_CACHE_V4, makeCache({ put }));
    let resolveFetch: (r: Response) => void = () => {};
    sw.fakeFetch.mockImplementation(
      () => new Promise<Response>((resolve) => { resolveFetch = resolve; }),
    );
    const pending = dispatchFetch(sw, navRequest('/learn'));
    await sendClear(sw);
    resolveFetch(htmlResponse('<html>late</html>'));
    const { response, error } = await pending;
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('CLEAR 之后的全新请求正常写缓存（epoch 防护不误伤后续会话）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    await sendClear(sw);
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(response).toBeInstanceOf(Response);
    expect(put).toHaveBeenCalledTimes(1);
  });

  it('诊断路径即使曾被缓存（旧版本 SW 写入）也不再 respondWith：离线不回放', async () => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}/api/taxonomy/diagnostics/3`));
  });

  it('非 ok 响应不进缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeCache(sw, { put });
    sw.fakeFetch.mockImplementation(async () => new Response('{"error":"x"}', {
      status: 500,
      headers: { 'Content-Type': 'application/json' },
    }));
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/api/taxonomy/subjects`),
    );
    expect(response).toBeInstanceOf(Response);
    expect((response as Response).status).toBe(500);
    expect(put).not.toHaveBeenCalled();
  });

  it('cache.put reject 不产生 unhandled rejection，响应照常返回', async () => {
    const sw = await loadSW();
    // 模拟登出清缓存与在途写入竞争（cache 已被 delete 后 put reject）。
    const put = vi.fn(async () => {
      throw new DOMException('The cache was deleted', 'InvalidStateError');
    });
    primeCache(sw, { put });
    const onUnhandled = vi.fn();
    registerUnhandledRejectionSpy(onUnhandled);
    try {
      const { response, error } = await dispatchFetch(
        sw,
        new Request(`${ORIGIN}/api/taxonomy/subjects`),
      );
      expect(error).toBeUndefined();
      expect(response).toBeInstanceOf(Response);
      expect(put).toHaveBeenCalledTimes(1);
      // 给 microtask 队列留时间：若 put 的 promise 未被 catch，
      // unhandledRejection 会在这里被捕获。
      await new Promise((r) => setTimeout(r, 20));
      expect(onUnhandled).not.toHaveBeenCalled();
    } finally {
      unregisterUnhandledRejectionSpy(onUnhandled);
    }
  });
});

describe('openchore-sw.js 静态缓存白名单（显式 allowlist，不再按后缀）', () => {
  const PRIVATE_ASSET_PATHS = [
    // API 下带文件后缀：旧逻辑按后缀误存鉴权响应，必须排除
    '/api/private.pdf',
    '/api/report.svg',
    '/api/doc.pdf',
    '/api/taxonomy/subjects.json',
    // 上传照片（用户内容）
    '/uploads/private.png',
    '/uploads/photo.jpg',
    // 朗读音频（家庭内容）
    '/tts/chore-1.mp3',
  ];
  it.each(PRIVATE_ASSET_PATHS)('私有路径 %s 不进静态缓存（不读不写缓存）', async (path) => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
  });

  // 后缀不再足够：同源未知路径即使带静态后缀也不缓存。
  const SUFFIX_ONLY_PATHS = [
    '/foo.js',
    '/foo.css',
    '/foo.png',
    '/foo.woff2',
    '/index.html',
    '/data.json',
    '/openchore-sw.js',
    '/sw.js',
    '/favicon.ico',
    // /assets/ 之外的伪前缀
    '/assets2/index.js',
    '/xassets/index.js',
  ];
  it.each(SUFFIX_ONLY_PATHS)('后缀/后缀伪装路径 %s 不缓存', async (path) => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
  });

  // 私有前缀与 /assets/ 的编码/双编码/大小写/双斜杠/点段/反斜杠变体。
  // 注意 new URL 会规范化 /./、/../ 与反斜杠，所以裸写路径构造出来的
  // pathname 可能已经变成合法 /assets/...——这里只保留真正能保持可疑形态
  // 的变体（编码、大小写、双斜杠）。
  const SUSPICIOUS_ASSET_PATHS = [
    '/API/x.js',
    '/Api/x.css',
    '/Uploads/x.png',
    '/UPLOADS/x.png',
    '/Tts/x.mp3',
    '/TTs/x.mp3',
    '/%61pi/x.js',
    '/api%2Fx.js',
    '/uploads%2Fx.png',
    '/assets%2Findex.js',
    '/%75ploads/x.png',
    '/%75ploads%2Fx.png',
    '/uploads/../uploads/x.png',
    '/api/%2e%2e/uploads/x.png',
    '/assets/../../api/x.png',
    '//assets/index.js',
    '/assets//index.js',
    '/assets/%69ndex.js',
    '/assets/index%2Ejs',
    '/assets/%2e%2e/api/x.png',
    '/assets/%252e%252e/api/x.png',
    '/assets/%zz.js',
    '/assets/x%',
    // 固定壳文件的私有伪装
    '/api/favicon.svg',
    '/uploads/manifest.json',
    '/favicon.svg/../api/x.svg',
    '/%66avicon.svg',
  ];
  it.each(SUSPICIOUS_ASSET_PATHS)('可疑静态路径 %s fail closed（不缓存）', async (path) => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}${path}`));
  });

  const ALLOWED_ASSET_PATHS = [
    '/assets/index-abc123.js',
    '/assets/index-abc123.css',
    '/assets/vendor/chunk-9f8e.woff2',
    '/assets/logo-1a2b.svg',
    '/favicon.svg',
    '/manifest.json',
    '/vite.svg',
  ];
  it.each(ALLOWED_ASSET_PATHS)('合法静态资源 %s 走 stale-while-revalidate（行为不回退）', async (path) => {
    const sw = await loadSW();
    routeCache(sw, STATIC_CACHE_V4, makeCache());
    sw.fakeFetch.mockImplementation(async () =>
      new Response('body', { status: 200, headers: { 'Content-Type': 'application/octet-stream' } }));
    const { response } = await dispatchFetch(sw, new Request(`${ORIGIN}${path}`));
    expect(response).toBeInstanceOf(Response);
    expect(sw.fakeCaches.open).toHaveBeenCalledWith(STATIC_CACHE_V4);
  });

  it('SWR no-store 响应不写静态缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => new Response('body', {
      status: 200,
      headers: { 'Content-Type': 'application/javascript', 'Cache-Control': 'no-store' },
    }));
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.js`),
    );
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('SWR 正常响应写静态缓存（v4）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => new Response('body', {
      status: 200,
      headers: { 'Content-Type': 'application/javascript' },
    }));
    const { response } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.js`),
    );
    expect(response).toBeInstanceOf(Response);
    expect(put).toHaveBeenCalledTimes(1);
  });

  it('SWR put reject 不产生 unhandled rejection', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => {
      throw new DOMException('The cache was deleted', 'InvalidStateError');
    });
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => new Response('body', {
      status: 200,
      headers: { 'Content-Type': 'application/javascript' },
    }));
    const onUnhandled = vi.fn();
    registerUnhandledRejectionSpy(onUnhandled);
    try {
      const { response, error } = await dispatchFetch(
        sw,
        new Request(`${ORIGIN}/assets/index-abc123.js`),
      );
      expect(error).toBeUndefined();
      expect(response).toBeInstanceOf(Response);
      expect(put).toHaveBeenCalledTimes(1);
      await new Promise((r) => setTimeout(r, 20));
      expect(onUnhandled).not.toHaveBeenCalled();
    } finally {
      unregisterUnhandledRejectionSpy(onUnhandled);
    }
  });
});

describe('openchore-sw.js 导航（精确 SPA 白名单）', () => {
  it('未知路径导航不接管（fail closed，含旧的 catch-all 路径）', async () => {
    const sw = await loadSW();
    navPassthrough(sw, '/somewhere');
    navPassthrough(sw, '/learn/cnm_p001');
    navPassthrough(sw, '/design');
  });

  const NAV_DENY_PATHS = [
    // 私有面（导航方式发起的 API，如 OIDC 跳转）
    '/api/auth/oidc/start',
    '/api/report',
    '/uploads/x.png',
    '/tts/a.mp3',
    // 大小写变体
    '/API/auth/me',
    '/Api/x',
    '/Uploads/x',
    '/TTS/x',
    // 编码/双编码
    '/%61pi/x',
    '/%2561pi/x',
    '/learn%2F',
    '/%6Cearn',
    '/learn%zz',
    '/learn%',
    '/learn/%09',
    '/learn/%0A',
    '/learn/%0D',
    '/learn/%0C',
    '/learn/%C2%A0',
    '/learn/%E2%80%A8',
    '/learn/%E2%80%A9',
    '/learn/%E2%80%AF',
    '/learn/%E2%81%9F',
    '/learn/%E3%80%80',
    '/learn/%EF%BB%BF',
    '/learn/%20',
    '/%09learn',
    '/learn%00',
    // 双斜杠
    '//api/auth/me',
    '//learn',
    '/learn//x',
    // 点段：裸 ./.. 与 %2e 点段在构造 Request 时已被 WHATWG URL 规范化为
    // 真实路径（落到白名单即真实的该路由，不属于绕过）；这里只保留解析后
    // 仍保留可疑形态的变体。
    '/learn/..%2fadmin',
    '/learn/%2e%2e%2fadmin',
    // 反斜杠（保留反斜杠形态，不被 URL 规范化掉）
    '/%5Clearn',
    // 带参数路由的非法参数
    '/boost/short',
    '/boost/ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789', // 大写 hex
    '/boost/0123456789abcdef0123456789abcdef0123456789abcde', // 47 位
    '/boost/0123456789abcdef0123456789abcdef0123456789abcdef0', // 49 位
    '/boost/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz',
    '/boost/0123456789abcdef0123456789abcdef0123456789abcde%30',
    '/share/taxonomy/abc123', // 前端测试夹具的短 token 不是真实格式
    '/share/taxonomy/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde',
    '/share/taxonomy/0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF',
    '/share/taxonomy/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/../x',
    '/share/other/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
    '/course/abc',
    '/course/',
    '/course',
    '/course/1/', // 尾斜杠不在精确形状里
    '/course/1.5',
    '/course/-1',
    '/course/1/practice',
  ];
  it.each(NAV_DENY_PATHS)('导航 %s 不接管（不 respondWith、不碰缓存）', async (path) => {
    const sw = await loadSW();
    navPassthrough(sw, path);
  });

  it('FIX2: hasBlankOrControlChars 覆盖 ASCII 控制/空格与 Unicode 空白，合法路由不受影响', async () => {
    const sw = await loadSW();
    const has = sw.internals.hasBlankOrControlChars;
    for (const ch of ['\u0000', '\u0009', '\u000a', '\u000c', '\u000d', ' ', '\u001f']) {
      expect(has(`/learn${ch}`)).toBe(true);
    }
    for (const ch of [
      ' ', ' ', ' ', ' ', ' ', ' ',
      ' ', ' ', '⁠', '　', '﻿',
    ]) {
      expect(has(`/learn${ch}`)).toBe(true);
    }
    for (const path of ['/learn', '/admin/dashboard', '/course/1', `/boost/${'0'.repeat(48)}`]) {
      expect(has(path)).toBe(false);
    }
  });

  it('FIX2: 空白/控制字符导航 fail closed（不 respondWith、不读不写缓存）', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, SHELL_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => htmlResponse('<html>net</html>'));
    for (const path of ['/learn%09', '/learn%C2%A0', '/learn%E2%80%A8', '/%0Dlearn', '/learn%00']) {
      navPassthrough(sw, path);
    }
    expect(sw.fakeCaches.open).not.toHaveBeenCalled();
    expect(sw.fakeFetch).not.toHaveBeenCalled();
    expect(put).not.toHaveBeenCalled();
  });

  const NAV_ALLOW_PATHS = [
    '/',
    '/login',
    '/setup',
    '/upload',
    '/admin',
    '/admin/dashboard',
    '/admin/reports',
    '/ambient',
    '/week',
    '/history',
    '/errorbook',
    '/review',
    '/learn',
    '/rewards',
    `/boost/${'0123456789abcdef'.repeat(3)}`,
    `/share/taxonomy/${'0123456789abcdef'.repeat(4)}`,
    '/course/1',
    '/course/900719925474099199',
  ];
  it.each(NAV_ALLOW_PATHS)('精确 SPA 导航 %s 接管（respondWith）', async (path) => {
    const sw = await loadSW();
    routeCache(sw, SHELL_CACHE_V4, makeCache());
    sw.fakeFetch.mockImplementation(async () => htmlResponse('<html>net</html>'));
    const { respondWith } = await dispatchFetch(sw, navRequest(path));
    expect(respondWith).toHaveBeenCalledTimes(1);
  });

  it('导航请求本身不因 mode=navigate 获得缓存资格（POST 导航直连）', async () => {
    const sw = await loadSW();
    const req: NavRequest = { ...navRequest('/learn'), method: 'POST' };
    expectPassthrough(sw, req);
  });

  it('导航离线时回退到缓存的壳（规范化 text/html）', async () => {
    const sw = await loadSW();
    const shell = new Response('<html>shell</html>', {
      status: 200,
      headers: { 'Content-Type': 'text/html' },
    });
    const matchCalls: unknown[] = [];
    routeCache(sw, SHELL_CACHE_V4, makeCache({
      match: async (req: unknown) => {
        matchCalls.push(req);
        return shell;
      },
    }));
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response, error } = await dispatchFetch(sw, navRequest('/learn'));
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect((response as Response).status).toBe(200);
    expect((response as Response).headers.get('Content-Type')).toContain('text/html');
    await expect((response as Response).text()).resolves.toBe('<html>shell</html>');
    // 离线回退读的是壳缓存里的 '/'（SHELL_URL），而不是导航请求原始 URL。
    expect(matchCalls).toHaveLength(1);
    expect(keyUrlOf(matchCalls[0])).toBe(`${ORIGIN}/`);
  });

  it('FIX2: HTML fallback 保留安全头、过滤逐跳/长度头与 Set-Cookie/非白名单头', async () => {
    const sw = await loadSW();
    // 用原始 Headers 构造缓存条目：测试环境允许逐跳头与 set-cookie 注入，
    // 正好验证重建响应时的白名单过滤（浏览器缓存条目可能来自旧版本 SW）。
    const staleHeaders = new Headers();
    const injected: Record<string, string> = {
      'Content-Type': 'text/html',
      'Cache-Control': 'public, max-age=60',
      'Content-Language': 'zh',
      'Content-Security-Policy': "default-src 'self'",
      'Content-Security-Policy-Report-Only': 'script-src none',
      'Cross-Origin-Opener-Policy': 'same-origin',
      'Cross-Origin-Embedder-Policy': 'require-corp',
      'Cross-Origin-Resource-Policy': 'same-origin',
      'Permissions-Policy': 'camera=()',
      'Referrer-Policy': 'no-referrer',
      'Strict-Transport-Security': 'max-age=31536000',
      'Vary': 'Accept-Encoding',
      'X-Content-Type-Options': 'nosniff',
      'X-Frame-Options': 'DENY',
      // 逐跳/体长度：body 已读成文本，原值必然失配。
      'Content-Encoding': 'gzip',
      'Content-Length': '999',
      'Transfer-Encoding': 'chunked',
      // 白名单外：绝不从缓存条目带回导航。
      'Set-Cookie': 'session=stale; HttpOnly',
      'X-Debug-Private': 'leak',
      'X-Powered-By': 'legacy',
    };
    for (const [k, v] of Object.entries(injected)) staleHeaders.set(k, v);
    const cached = new Response('<html>shell</html>', {
      status: 200,
      statusText: 'OK',
      headers: staleHeaders,
    });
    routeCache(sw, SHELL_CACHE_V4, makeCache({ match: async () => cached }));
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('offline'); });

    const { response, error } = await dispatchFetch(sw, navRequest('/learn'));
    expect(error).toBeUndefined();
    const fallback = response as Response;
    expect(fallback.status).toBe(200);
    expect(fallback.headers.get('Content-Type')).toBe('text/html; charset=utf-8');
    expect(await fallback.text()).toBe('<html>shell</html>');
    // 安全头全部保留（含大小写无关的原值）。
    for (const [name, value] of Object.entries(injected)) {
      if (['Content-Type', 'Content-Encoding', 'Content-Length', 'Transfer-Encoding',
        'Set-Cookie', 'X-Debug-Private', 'X-Powered-By'].includes(name)) continue;
      expect(fallback.headers.get(name)).toBe(value);
    }
    // 逐跳/长度、Set-Cookie、任意非白名单头全部被过滤。
    for (const name of [
      'Content-Encoding', 'Content-Length', 'Transfer-Encoding',
      'Set-Cookie', 'X-Debug-Private', 'X-Powered-By',
    ]) {
      expect(fallback.headers.get(name)).toBeNull();
    }
    // 缓存条目的 Content-Length 与实际 body 长度不同，重建响应不得沿用。
    expect(fallback.headers.get('Content-Length')).not.toBe('999');
  });

  it('FIX2: 外层兜底 fallback 同样过滤逐跳头与 Set-Cookie 并保留安全头', async () => {
    const sw = await loadSW();
    const staleHeaders = new Headers();
    for (const [k, v] of Object.entries({
      'Content-Type': 'text/html',
      'Content-Length': '999',
      'Content-Encoding': 'gzip',
      'Transfer-Encoding': 'chunked',
      'Set-Cookie': 'session=stale',
      'X-Frame-Options': 'DENY',
      'Content-Security-Policy': "default-src 'self'",
    })) staleHeaders.set(k, v);
    const cached = new Response('<html>shell</html>', { status: 200, headers: staleHeaders });
    // 第一次 match 返回空壳（触发 networkFirst 的 throw err），外层兜底
    // catch 再次 match 返回带毒头的缓存条目，走第二条重建路径。
    let calls = 0;
    routeCache(sw, SHELL_CACHE_V4, makeCache({
      match: async () => {
        calls += 1;
        return calls === 1 ? new Response('', { status: 200, headers: { 'Content-Type': 'text/html' } }) : cached;
      },
    }));
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('offline'); });
    const { response } = await dispatchFetch(sw, navRequest('/learn'));
    const fallback = response as Response;
    expect(fallback.headers.get('Content-Type')).toBe('text/html; charset=utf-8');
    expect(fallback.headers.get('X-Frame-Options')).toBe('DENY');
    expect(fallback.headers.get('Content-Security-Policy')).toBe("default-src 'self'");
    for (const name of ['Content-Length', 'Content-Encoding', 'Transfer-Encoding', 'Set-Cookie']) {
      expect(fallback.headers.get(name)).toBeNull();
    }
    expect(await fallback.text()).toBe('<html>shell</html>');
  });

  it('导航在线时走网络并以 SHELL_URL("/") 为键写壳缓存（与 install 预缓存同键）', async () => {
    const sw = await loadSW();
    const net = htmlResponse('<html>net</html>');
    const put = vi.fn(async () => undefined);
    routeCache(sw, SHELL_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => net);
    const { response } = await dispatchFetch(sw, navRequest('/learn'));
    expect(response).toBe(net);
    expect(put).toHaveBeenCalledTimes(1);
    expect(keyUrlOf((put.mock.calls[0] as unknown[])[0])).toBe(`${ORIGIN}/`);
  });

  it('导航在线的 JSON 响应（异常/API 响应）绝不写入壳缓存 "/" 键', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, SHELL_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => jsonResponse('{"error":"boom"}'));
    const { response, error } = await dispatchFetch(sw, navRequest('/learn'));
    expect(error).toBeUndefined();
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('导航在线的 text/plain 响应也不写入壳缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, SHELL_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () =>
      new Response('Not found.', { status: 200, headers: { 'Content-Type': 'text/plain' } }));
    await dispatchFetch(sw, navRequest('/learn'));
    expect(put).not.toHaveBeenCalled();
  });

  it('导航离线且壳也没缓存时 Response.error()', async () => {
    const sw = await loadSW();
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response } = await dispatchFetch(sw, navRequest('/learn'));
    expect(response).toBeInstanceOf(Response);
    expect((response as Response).type).toBe('error');
  });

  it('导航离线且壳缓存条目为空时不回放空白 200（走 Response.error()）', async () => {
    const sw = await loadSW();
    // 壳缓存命中但 body 为空：回放它等于给导航一个空白成功响应。
    routeCache(sw, SHELL_CACHE_V4, makeCache({
      match: async () => new Response('', { status: 200, headers: { 'Content-Type': 'text/html' } }),
    }));
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response } = await dispatchFetch(sw, navRequest('/learn'));
    // 外层兜底 catch：空壳继续回退，最终是明确的错误响应而不是空白 200。
    expect(response).toBeInstanceOf(Response);
    expect((response as Response).type).toBe('error');
  });
});

describe('openchore-sw.js 壳资源离线回放', () => {
  it('壳引用的构建 JS 离线时从静态缓存回放（SWR 命中）', async () => {
    const sw = await loadSW();
    const cachedJs = new Response('console.log("chunk")', {
      status: 200,
      headers: { 'Content-Type': 'application/javascript' },
    });
    routeCache(sw, STATIC_CACHE_V4, makeCache({ match: async () => cachedJs }));
    // 离线：后台刷新失败被吞，SWR 直接回放缓存条目。
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response, error } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.js`),
    );
    expect(error).toBeUndefined();
    expect(response).toBe(cachedJs);
    await expect((response as Response).text()).resolves.toBe('console.log("chunk")');
  });

  it('壳引用的构建 CSS 离线时从静态缓存回放（SWR 命中）', async () => {
    const sw = await loadSW();
    const cachedCss = new Response('body{}', {
      status: 200,
      headers: { 'Content-Type': 'text/css' },
    });
    routeCache(sw, STATIC_CACHE_V4, makeCache({ match: async () => cachedCss }));
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response, error } = await dispatchFetch(
      sw,
      new Request(`${ORIGIN}/assets/index-abc123.css`),
    );
    expect(error).toBeUndefined();
    expect(response).toBe(cachedCss);
    await expect((response as Response).text()).resolves.toBe('body{}');
  });

  it('manifest / favicon 离线时从静态缓存回放', async () => {
    const sw = await loadSW();
    const cached = jsonResponse('{"name":"OpenChore"}');
    routeCache(sw, STATIC_CACHE_V4, makeCache({ match: async () => cached }));
    sw.fakeFetch.mockImplementation(async () => {
      throw new TypeError('Failed to fetch');
    });
    const { response } = await dispatchFetch(sw, new Request(`${ORIGIN}/manifest.json`));
    expect(response).toBe(cached);
  });
});

describe('openchore-sw.js 直连', () => {
  it('未知同源 GET（如 /api/auth/me）直连不缓存', async () => {
    const sw = await loadSW();
    expectPassthrough(sw, new Request(`${ORIGIN}/api/auth/me`));
  });
});

// ---------------------------------------------------------------------------
// DEF-PWA-PRODUCTION-R2 第二轮：生产修复后的真实契约（页面侧布尔结果、
// 无 controller 用 active、SW 侧写入闸门与在线降级、activate 单删失败）。
// ---------------------------------------------------------------------------

/**
 * 伪造一条"网络响应"的 type / redirected / url（真 Response 的 type 恒为
 * default 且无法重定向，只能用 defineProperty 模拟 SW 里 fetch 得到的
 * basic/cors/opaque/redirected 响应）。
 */
function asNetworkResponse(
  resp: Response,
  opts: { type?: string; redirected?: boolean; url?: string },
): Response {
  if (opts.type !== undefined) Object.defineProperty(resp, 'type', { value: opts.type, configurable: true });
  if (opts.redirected !== undefined) {
    Object.defineProperty(resp, 'redirected', { value: opts.redirected, configurable: true });
  }
  Object.defineProperty(resp, 'url', { value: opts.url ?? '', configurable: true });
  return resp;
}

describe('clearTaxonomyCaches：清理结果布尔与退化路径（DEF-PWA-PRODUCTION-R2 第二轮）', () => {
  it('无 controller 但 registration.active 存在：对 active 做两轮 CLEAR 再删页面缓存', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    const active = new AckingSW();
    vi.stubGlobal('navigator', {
      serviceWorker: {
        register: vi.fn(async () => ({ scope: '/' })),
        controller: null,
        getRegistration: vi.fn(async () => ({ active })),
      },
    });
    const deleted = stubPageCaches(['openchore-taxonomy-v4']);

    await expect(clearTaxonomyCaches()).resolves.toBe(true);

    // 不能只做页面删除：active SW 必须收到两轮 CLEAR（其代数与在途 put 要收敛）。
    expect(active.postMessage).toHaveBeenCalledTimes(2);
    expect(pairs).toHaveLength(2);
    pairs.forEach((p) => expect(p.port1.close).toHaveBeenCalledTimes(1));
    expect(deleted).toEqual(['openchore-taxonomy-v4', 'openchore-taxonomy-v4']);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('getRegistration reject：降级为页面删除，结果按页面删除是否成功如实返回', async () => {
    vi.stubGlobal('navigator', {
      serviceWorker: {
        register: vi.fn(async () => ({ scope: '/' })),
        controller: null,
        getRegistration: vi.fn(async () => { throw new Error('registration unavailable'); }),
      },
    });
    const deleted = stubPageCaches(['openchore-taxonomy-v4']);
    await expect(clearTaxonomyCaches()).resolves.toBe(true);
    // 降级为页面删除；两轮次数由「两轮 CLEAR 均正常」用例覆盖。
    expect(deleted.length).toBeGreaterThanOrEqual(1);
    expect(deleted.every((n) => n === 'openchore-taxonomy-v4')).toBe(true);
  });

  it('SW ack 的 failed 非空：返回 false（SW 侧删除失败不能算清理成功）', async () => {
    vi.useFakeTimers();
    stubControllableMessageChannel([]);
    stubServiceWorker(new AckingSW({ epoch: 1, deleted: [], failed: ['openchore-taxonomy-v4'] }));
    const deleted = stubPageCaches([]);
    await expect(clearTaxonomyCaches()).resolves.toBe(false);
    // 结果不可信但页面兜底删除照常跑两轮。
    expect(deleted).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('旧版本 SW 回执缺 deleted/failed：按结果未知处理，返回 false', async () => {
    vi.useFakeTimers();
    const pairs: FakePortPair[] = [];
    stubControllableMessageChannel(pairs);
    // 旧 SW 只回 type/epoch，没有真实删除结果。
    stubServiceWorker(new AckingSW({ epoch: 1 } as Record<string, unknown>));
    stubPageCaches([]);
    await expect(clearTaxonomyCaches()).resolves.toBe(false);
    expect(pairs).toHaveLength(2);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('页面侧 caches.delete 返回 false：本轮结果 false，整体返回 false', async () => {
    vi.useFakeTimers();
    stubControllableMessageChannel([]);
    stubServiceWorker(new AckingSW());
    vi.stubGlobal('caches', {
      keys: vi.fn(async () => ['openchore-taxonomy-v4']),
      delete: vi.fn(async () => false),
    });
    await expect(clearTaxonomyCaches()).resolves.toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('页面侧 caches.keys reject：无从核实是否删净，返回 false 且不抛错', async () => {
    vi.useFakeTimers();
    stubControllableMessageChannel([]);
    stubServiceWorker(new AckingSW());
    vi.stubGlobal('caches', {
      keys: vi.fn(async () => { throw new Error('storage broken'); }),
      delete: vi.fn(async () => true),
    });
    await expect(clearTaxonomyCaches()).resolves.toBe(false);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('无 SW 支持但 CacheStorage 可用：页面删除成功即返回 true', async () => {
    vi.stubGlobal('navigator', {});
    const deleted = stubPageCaches(['openchore-static-v4', 'openchore-taxonomy-v4']);
    await expect(clearTaxonomyCaches()).resolves.toBe(true);
    // 无 SW 路径只做页面删除（两轮）；顺序与次数由下面的两轮断言覆盖。
    expect(deleted.slice(0, 2)).toEqual(['openchore-static-v4', 'openchore-taxonomy-v4']);
  });

  it('两轮 CLEAR 都正常 ack 且删除成功：返回 true', async () => {
    vi.useFakeTimers();
    stubControllableMessageChannel([]);
    stubServiceWorker(new AckingSW());
    stubPageCaches(['openchore-taxonomy-v4']);
    await expect(clearTaxonomyCaches()).resolves.toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe('openchore-sw.js 写入闸门（DEF-PWA-PRODUCTION-R2 第二轮）', () => {
  function primeTaxonomy(sw: SWHarness, put: ReturnType<typeof vi.fn>): FakeCache {
    const cache = makeCache({ put });
    routeCache(sw, TAXONOMY_CACHE_V4, cache);
    return cache;
  }

  it.each([
    ['redirected', { type: 'basic', redirected: true, url: `${ORIGIN}/api/taxonomy/subjects` }],
    ['opaque', { type: 'opaque', redirected: false, url: `${ORIGIN}/api/taxonomy/subjects` }],
    ['cors', { type: 'cors', redirected: false, url: `${ORIGIN}/api/taxonomy/subjects` }],
    ['error', { type: 'error', redirected: false, url: `${ORIGIN}/api/taxonomy/subjects` }],
    ['跨源最终 URL', { type: 'basic', redirected: false, url: 'https://evil.example/api/taxonomy/subjects' }],
    ['畸形最终 URL', { type: 'basic', redirected: false, url: 'not a url' }],
  ])('taxonomy 响应被 %s：在线照常返回响应，但绝不写缓存', async (_label, shape) => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeTaxonomy(sw, put);
    const sealed = jsonResponse('{"subjects":[1]}');
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(sealed.clone(), shape));
    const { response } = await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('taxonomy 响应被重定向离开白名单（最终 URL 在 /api/taxonomy/topics/abc）：不写缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeTaxonomy(sw, put);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      jsonResponse('{"answer":"secret"}'),
      { type: 'basic', redirected: true, url: `${ORIGIN}/api/taxonomy/topics/abc` },
    ));
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(put).not.toHaveBeenCalled();
  });

  it('taxonomy 正常 basic 响应且最终 URL 同源白名单：照常写缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    primeTaxonomy(sw, put);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      jsonResponse('{"subjects":[1]}'),
      { type: 'basic', redirected: false, url: `${ORIGIN}/api/taxonomy/subjects` },
    ));
    await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(put).toHaveBeenCalledTimes(1);
  });

  it('静态资源被重定向到跨源：在线返回该响应，但不写静态缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      new Response('body', { status: 200, headers: { 'Content-Type': 'application/javascript' } }),
      { type: 'basic', redirected: true, url: 'https://cdn.example/assets/index-abc123.js' },
    ));
    const { response } = await dispatchFetch(sw, new Request(`${ORIGIN}/assets/index-abc123.js`));
    expect(response).toBeInstanceOf(Response);
    expect(put).not.toHaveBeenCalled();
  });

  it('静态资源响应为 opaque：不写静态缓存', async () => {
    const sw = await loadSW();
    const put = vi.fn(async () => undefined);
    routeCache(sw, STATIC_CACHE_V4, makeCache({ put }));
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      new Response('body', { status: 200, headers: { 'Content-Type': 'application/javascript' } }),
      { type: 'opaque', redirected: false, url: `${ORIGIN}/assets/index-abc123.js` },
    ));
    await dispatchFetch(sw, new Request(`${ORIGIN}/assets/index-abc123.js`));
    expect(put).not.toHaveBeenCalled();
  });

  it('导航壳响应 redirected：在线返回重定向终点内容，但绝不写 "/" 壳缓存', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      htmlResponse('<html>redirect target</html>'),
      { type: 'basic', redirected: true, url: `${ORIGIN}/login` },
    ));
    const { response } = await dispatchFetch(sw, navRequest('/'));
    expect(response).toBeInstanceOf(Response);
    expect(shell.put).not.toHaveBeenCalled();
  });

  it('导航壳响应最终 URL 跨源：不写 "/" 壳缓存', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      htmlResponse('<html>evil</html>'),
      { type: 'basic', redirected: false, url: 'https://evil.example/' },
    ));
    await dispatchFetch(sw, navRequest('/'));
    expect(shell.put).not.toHaveBeenCalled();
  });

  it('导航壳响应最终 URL 在私有 API 路径（/api/x，HTML）：不写 "/" 壳缓存', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      htmlResponse('<html>api</html>'),
      { type: 'basic', redirected: false, url: `${ORIGIN}/api/x` },
    ));
    await dispatchFetch(sw, navRequest('/'));
    expect(shell.put).not.toHaveBeenCalled();
  });

  it('install 预缓存被重定向到跨源：不写壳缓存，仍完成 skipWaiting', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      htmlResponse('<html>cdn</html>'),
      { type: 'basic', redirected: true, url: 'https://cdn.example/index.html' },
    ));
    const skipWaiting = vi.fn(() => Promise.resolve());
    (sw.selfRef as { skipWaiting: unknown }).skipWaiting = skipWaiting;
    const waitUntil = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(shell.put).not.toHaveBeenCalled();
    expect(skipWaiting).toHaveBeenCalled();
  });

  it('install 预缓存为 opaque：不写壳缓存', async () => {
    const sw = await loadSW();
    const shell: FakeCache = makeCache();
    routeCache(sw, SHELL_CACHE_V4, shell);
    sw.fakeFetch.mockImplementation(async () => asNetworkResponse(
      htmlResponse('<html>opaque</html>'),
      { type: 'opaque', redirected: false, url: `${ORIGIN}/` },
    ));
    const waitUntil = vi.fn();
    (sw.installHandler as NonNullable<SWHarness['installHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(shell.put).not.toHaveBeenCalled();
  });
});

describe('openchore-sw.js 在线降级：CacheStorage 不可用时纯网络（R2 第二轮）', () => {
  it('networkFirst：caches.open reject 时在线请求仍返回网络响应，不 reject', async () => {
    const sw = await loadSW();
    sw.fakeCaches.open.mockImplementation(async () => { throw new Error('storage broken'); });
    const net = htmlResponse('<html>online</html>');
    sw.fakeFetch.mockImplementation(async () => net);
    const { response, error } = await dispatchFetch(sw, navRequest('/'));
    expect(error).toBeUndefined();
    expect(response).toBe(net);
  });

  it('networkFirst：caches.open reject 且离线时导航不卡死（外层兜底以同一错误收敛）', async () => {
    const sw = await loadSW();
    sw.fakeCaches.open.mockImplementation(async () => { throw new Error('storage broken'); });
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('Failed to fetch'); });
    // 生产契约：caches.open 失败时 networkFirst 直接走 fetch（网络优先），
    // 离线把网络错误原样抛出，由导航 handler 外层 catch 兜底。
    const { error } = await dispatchFetch(sw, navRequest('/'));
    expect(error).toBeInstanceOf(Error);
    // 不因 CacheStorage 故障而反复重开缓存（open 只可能尝试入口一次）。
    expect(sw.fakeCaches.open.mock.calls.length).toBeGreaterThanOrEqual(1);
  });

  it('networkFirst：caches.open reject 时 taxonomy 请求在线仍返回网络响应', async () => {
    const sw = await loadSW();
    sw.fakeCaches.open.mockImplementation(async () => { throw new Error('storage broken'); });
    const net = jsonResponse('{"subjects":[1]}');
    sw.fakeFetch.mockImplementation(async () => net);
    const { response, error } = await dispatchFetch(sw, new Request(`${ORIGIN}/api/taxonomy/subjects`));
    expect(error).toBeUndefined();
    expect(response).toBe(net);
  });

  it('staleWhileRevalidate：caches.open reject 时在线请求仍返回网络响应（走网络刷新）', async () => {
    const sw = await loadSW();
    sw.fakeCaches.open.mockImplementation(async () => { throw new Error('storage broken'); });
    const net = new Response('body', { status: 200, headers: { 'Content-Type': 'application/javascript' } });
    sw.fakeFetch.mockImplementation(async () => net);
    const { response, error } = await dispatchFetch(sw, new Request(`${ORIGIN}/assets/index-abc123.js`));
    expect(error).toBeUndefined();
    expect(await (response as Response).text()).toBe('body');
  });

  it('staleWhileRevalidate：caches.open reject 且离线返回 Response.error()，不 reject', async () => {
    const sw = await loadSW();
    sw.fakeCaches.open.mockImplementation(async () => { throw new Error('storage broken'); });
    sw.fakeFetch.mockImplementation(async () => { throw new TypeError('Failed to fetch'); });
    const { response, error } = await dispatchFetch(sw, new Request(`${ORIGIN}/assets/index-abc123.js`));
    expect(error).toBeUndefined();
    expect((response as Response).type).toBe('error');
  });
});

describe('openchore-sw.js activate / CLEAR 真实删除结果（R2 第二轮）', () => {
  it('activate：单个旧代 delete reject 不阻止其余删除，也不阻止 clients.claim', async () => {
    const sw = await loadSW();
    sw.fakeCaches.keys.mockImplementation(async () => [
      'openchore-taxonomy-v3',
      'openchore-static-v3',
      SHELL_CACHE_V4,
      'other-app',
    ]);
    sw.fakeCaches.delete.mockImplementation(async (n: string) => {
      if (n === 'openchore-taxonomy-v3') throw new Error('delete failed');
      return true;
    });
    const claim = vi.fn(() => Promise.resolve());
    (sw.selfRef as { clients: unknown }).clients = { claim };
    const waitUntil = vi.fn();
    (sw.activateHandler as NonNullable<SWHarness['activateHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    const deletedNames = (sw.fakeCaches.delete.mock.calls as unknown[][]).map((c) => c[0]);
    expect(deletedNames).toContain('openchore-taxonomy-v3');
    expect(deletedNames).toContain('openchore-static-v3');
    expect(deletedNames).not.toContain(SHELL_CACHE_V4);
    expect(deletedNames).not.toContain('other-app');
    expect(claim).toHaveBeenCalledTimes(1);
  });

  it('activate：caches.keys reject 时仍 clients.claim', async () => {
    const sw = await loadSW();
    sw.fakeCaches.keys.mockImplementation(async () => { throw new Error('keys broken'); });
    const claim = vi.fn(() => Promise.resolve());
    (sw.selfRef as { clients: unknown }).clients = { claim };
    const waitUntil = vi.fn();
    (sw.activateHandler as NonNullable<SWHarness['activateHandler']>)({ waitUntil });
    await (waitUntil.mock.calls[0] as [Promise<unknown>])[0];
    expect(claim).toHaveBeenCalledTimes(1);
  });

  it('CLEAR：taxonomy 删除失败时 ack 的 failed 如实列出，deleted 只含成功项', async () => {
    const sw = await loadSW();
    sw.fakeCaches.delete.mockImplementation(async (n: string) => {
      if (n === TAXONOMY_CACHE_V4) throw new Error('delete failed');
      return true;
    });
    const port = { postMessage: vi.fn() };
    await sendClear(sw, port);
    expect(port.postMessage).toHaveBeenCalledWith({
      type: 'CLEAR_TAXONOMY_CACHES_DONE',
      epoch: 1,
      deleted: [STATIC_CACHE_V4],
      failed: [TAXONOMY_CACHE_V4],
    });
  });

  it('CLEAR：delete 返回 false（未删除）同样记入 failed', async () => {
    const sw = await loadSW();
    sw.fakeCaches.delete.mockImplementation(async (n: string) => n !== STATIC_CACHE_V4);
    const port = { postMessage: vi.fn() };
    await sendClear(sw, port);
    expect(port.postMessage).toHaveBeenCalledWith({
      type: 'CLEAR_TAXONOMY_CACHES_DONE',
      epoch: 1,
      deleted: [TAXONOMY_CACHE_V4],
      failed: [STATIC_CACHE_V4],
    });
  });
});

/** put/match 键（string 或 Request）转 URL 字符串。 */
function keyUrlOf(key: unknown): string {
  if (typeof key === 'string') {
    return key.startsWith('http') ? key : `${ORIGIN}${key}`;
  }
  return (key as { url: string }).url;
}
