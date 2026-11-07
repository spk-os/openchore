// OpenChore Service Worker（卡 TX-PWA-FIX / TX-SW-ONLY / DEF-PWA-PRIVACY /
// TX-PWA-SW-FIX1 / TX-PWA-SW-PRIVACY-FIX2）。
//
// 缓存策略（刻意保守，只覆盖明确安全的面）：
//   1. SPA 导航（精确路由白名单）：网络优先，离线回退缓存的 index.html
//      （在线成功时统一以 SHELL_URL('/') 为键写入壳缓存，与 install 预
//      缓存同键）。仅 App.tsx / KidApp.tsx 里真实存在的 SPA 路由进入该
//      路径；/api、/uploads、/tts 及其一切变体（编码/双编码/大小写/双
//      斜杠）与未知路径的导航一律直接放行原生网络——不 respondWith、
//      不读不写任何缓存。写 SHELL_CACHE 前必须确认最终响应
//      Content-Type 是 text/html：任何 JSON（API 响应、服务端异常）
//      绝不写入 '/' 键被当成离线壳回放。
//   2. 静态资源（显式 allowlist）：真实构建产物 /assets/… 与当前既有
//      固定壳文件（favicon.svg / manifest.json / vite.svg），
//      stale-while-revalidate。不再按后缀放行任意 .js/.css/图片——
//      同源上任何带静态后缀的私有/未知响应（/api/report.pdf、
//      /foo.js、/openchore-sw.js）都不进缓存。含 %、反斜杠、双斜杠、
//      点段、非规范大小写私有前缀等可疑特征一律 fail closed。
//   3. taxonomy 白名单 GET（见 isCacheableTaxonomy）：网络优先，离线回
//      退缓存。只允许 /api/taxonomy/subjects 与"q 参数全部为空/空白"的
//      /api/taxonomy/topics 两个只读目录端点。带任何非空 q（含重复 q
//      里任一非空、百分号编码后解析为非空）的 topics 一律 network-only
//      ——不读缓存回放、不写缓存、不剥离 query 统一 key。
//      topics/{ref} 详情继续 network-only。
//   其余请求一律直接放行（不缓存、离线不回放）。
//
// 安全边界：
//   - 只处理 GET；POST/PUT/DELETE 等写请求永远直接走网络。
//   - taxonomy 可缓存面是**精确白名单**，不是前缀放行：只有
//     /api/taxonomy/subjects、以及 q 参数全部为空/空白的
//     /api/taxonomy/topics 两个只读目录端点可进缓存。
//     topics/{ref} 详情**绝不缓存**：服务端对同一详情按角色差异化
//     响应（admin 会拿到 answer/accept/explanation，孩子端没有），进共
//     享缓存就是越权面；任何 URL 编码变体同样 network-only（离线不回放）。
//     diagnostics、blindspots、export、shared、admin、import 及其它一切
//     taxonomy 路径都是 network-only。
//   - 清理代数（cacheEpoch）+ 在途 put 追踪（inFlightPuts）：收到
//     CLEAR_TAXONOMY_CACHES 先 cacheEpoch++，再等所有登记在途的 put
//     settle（有界：最多 PUT_SETTLE_TIMEOUT_MS，Promise.allSettled/等效），
//     然后删 taxonomy 与 static 缓存。install / networkFirst / SWR 都在
//     请求发起时记下当时代数、写入前核对，期间发生过清理的在途响应放弃
//     写入——清理前已在途的网络响应不得在缓存删除后重新 put，把（可能含
//     上一会话私有数据的）缓存复活出来。所有旧 epoch 的写入一律放弃。
//     install 因此不用 cache.add（其内部 fetch+put 无代数保护），改为
//     显式 fetch + epoch 检查。
//   - activate 只删除 `openchore-` 前缀且不在当前 KNOWN_CACHES 的缓存；
//     同 origin 下其它应用的 cache 绝不动。
//   - CLEAR 完成后经 event.ports[0] 回执
//     {type:'CLEAR_TAXONOMY_CACHES_DONE', epoch}（AUTH-FIX1 契约）：回执
//     在 cacheEpoch 已递增、已在途 put 已 settle（有界超时）、SW 自身删
//     除完成之后发出，页面等这个 ack 再删 openchore-*。无 port（旧页面）
//     时照常清理、不回执。在途 put 的 settle 等待只承诺**有界降级**（超
//     时即继续删除，绝不无限阻塞清理），不承诺对挂死的 put 绝对等待。
//   - SPA 导航路径任何空白/控制字符一律 fail closed（不接管）；HTML
//     fallback 重建响应时保留原安全响应头、过滤逐跳头
//     （content-encoding / content-length / transfer-encoding）、补正确
//     的 Content-Type；body 为空的壳条目不回放（不写空壳）。
//   - 尊重响应 Cache-Control：no-store / private 的响应不进任何缓存
//     （networkFirst 与 stale-while-revalidate 同一准入）。
//   - 所有 cache.put 都是 fire-and-forget 且带 catch：登出清缓存与在途
//     写入竞争时 reject 必须被吞掉，不能产生 unhandled rejection。
//   - taxonomy 缓存属于"当前会话"数据：登出时由页面调用
//     clearTaxonomyCaches()（经 postMessage）清空 taxonomy 与 static
//     缓存——static 一并清是为了兜底清掉旧版本 SW 按后缀误存的私有响应，
//     避免跨用户读到旧数据。
//
// 版本升级：改动缓存逻辑时递增 VERSION，activate 阶段删除所有旧代
// openchore-* 缓存（v2 的 static 缓存可能按后缀误存过私有响应，v2 的
// shell 缓存可能被旧导航逻辑写入过非 HTML 响应，必须整体废弃）。

// TX-PWA-SW-PRIVACY-FIX2 在 v3 契约上收紧三点，不递增 VERSION（v3 缓存
// 里可缓存条目本就只有 subjects / 无 q 的 topics / 静态 allowlist / 壳，
// 收紧的是准入与清理时序，不是缓存形态）：
//   1) topics 可缓存判定加 q 参数闸门：URLSearchParams 语义解析 query，
//      所有 q 值 trim 后全空才可缓存；任一非空（含重复 q 中任一非空、
//      百分号编码后解析为非空）network-only，不剥离 query 统一 key。
//   2) SPA 导航路径加空白/控制字符检查（URL 层剥不掉的 %09/%0A/%0C/%0D、
//      %C2%A0 等），HTML fallback 重建保留安全头、剥逐跳头。
//   3) putQuietly 登记在途 put；CLEAR 先 epoch++、等在途 put settle
//      （有界 1500ms）、再删缓存、再 ack；settle 后从集合摘除，无界增长
//      不存在。旧 epoch 的写入全部放弃。
//
// DEF-PWA-PRODUCTION-R2（reviewer P0/P1）：
//   1) topics 的 q 判空与 Go strings.TrimSpace 的空白集合对齐：显式用
//      Unicode White_Space 属性正则（/^\p{White_Space}*$/u）而不是
//      String.trim（ECMAScript trim 会把 U+FEFF 当空白裁掉，与服务端
//      不一致）。U+FEFF、零宽/格式字符、lone surrogate、任何可见字符
//      一律非空 → network-only；重复 q 任一非空仍 network-only。
//   2) install 的 shell cache.put 改走 putQuietly（登记 inFlightPuts），
//      caches.open 之后、put 之前重查 epoch（open 挂起期间可能已发生
//      CLEAR）；CLEAR 等已登记 put settle 后再删除，caches.delete 失败
//      / 同步抛错收敛（逐个兜底），清理仍 best-effort ack，不产生未处理
//      rejection。
//   3) 导航在线写 shell（'/' 键）前的最终响应闸门 isSafeShellResponse：
//      拒绝 redirected、非 basic（opaque/cors/error）、最终 URL 不同源或
//      不在精确 SPA 白名单的响应。生产网络响应的 resp.url 恒非空；同步
//      构造的 Response 没有 url（仅测试替身出现），此时跳过最终 URL 项、
//      其余闸门照常生效。
//   4) putQuietly 兜 cache.put 同步抛错：登记的 tracked promise 立即
//      settle 并摘除，inFlightPuts 不残留、CLEAR 不空等超时。
//
// DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1/P2，VERSION 递增 v4）：v3 及
// 更早的 SW 曾把"只查 Content-Type 的 HTML"写进 shell 缓存（重定向/非
// basic/跨源终点都可能混入），activate 只删旧代缓存无法覆盖"同代内已
// 投毒"的条目，整体升 v4 让旧代缓存全部作废：
//   1) install 写 shell 同时走 isCacheableResponse + isSafeShellResponse
//      （不再只做 HTML 检查：redirected/非 basic/最终 URL 不同源或不在
//      SPA 白名单的响应一律不预缓存）。
//   2) activate 逐 cache deleteQuietly 兜底，单个 delete 失败不阻止
//      clients.claim。
//   3) CLEAR ack 携带 deleted/failed 真实删除结果（页面侧据此判定清理
//      是否成功）。
//   4) networkFirst / staleWhileRevalidate 的 caches.open 失败回退纯网
//      络，不让在线请求 reject；taxonomy/static 写入同样拒绝 redirected、
//      非 basic 及最终 URL 不同源/不在各自白名单的响应（公共闸门
//      hasSafeResponseType + finalUrlIsCacheable）。
//   5) settle 等待语义如实化：只承诺**已登记且窗口内 settle** 的 put；
//      超时后仍在途的 put 无法取消（cache.put 不可取消），超时是有界
//      best-effort 降级，不是绝对写入屏障——绝对性由 epoch 检查兜底
//      （旧 epoch 的写入一律放弃）。

const VERSION = 'v4';
const SHELL_CACHE = `openchore-shell-${VERSION}`;
const STATIC_CACHE = `openchore-static-${VERSION}`;
const TAXONOMY_CACHE = `openchore-taxonomy-${VERSION}`;
const KNOWN_CACHES = [SHELL_CACHE, STATIC_CACHE, TAXONOMY_CACHE];

const SHELL_URL = '/';

// 清理代数：每次收到 CLEAR_TAXONOMY_CACHES 自增。请求发起时记下的代数
// 与写入时的代数不一致，说明期间发生过清理——该响应属于"清理前已在
// 途"的请求，不得写入（防止缓存被删后 put 把它复活）。
let cacheEpoch = 0;

// 在途 cache.put 追踪（TX-PWA-SW-PRIVACY-FIX2）：putQuietly 每次写缓存
// 都把 put promise 登记进来、settle 后摘除。CLEAR 收到时先 epoch++，再
// 有界等待集合里已在途的 put settle（超时继续，不无限阻塞清理），之后
// 才删缓存——删除与旧 put 落地的交错窗口由此收窄。集合只增不减的风险
// 由"settle 必摘除"杜绝：正常路径摘除发生在 put resolve/reject 的
// microtask，永不无界增长。
const inFlightPuts = new Set();

// CLEAR 等待在途 put settle 的有界超时：超时说明某个 put 挂死（存储后端
// 卡死等），放弃等待继续删除——语义只是"有界降级"，不是对坏 put 的
// 绝对等待；剩余窗口由 epoch 检查兜底（旧 epoch 写入全部放弃）。put 一旦
// 发起不可取消（cache.put 无取消原语）：本超时不假装能撤销仍在途的 put，
// 只承诺**已登记且窗口内 settle** 的写入被纳入删除屏障，超时后落地的
// 写入靠其调用侧的 epoch 检查放弃。
const PUT_SETTLE_TIMEOUT_MS = 1500;

// DEF-PWA-PRODUCTION-R2：与 Go strings.TrimSpace 对齐的空白判空正则。
// 只认 Unicode White_Space 属性为空白；U+FEFF/零宽格式字符是 Go 语义下
// 的非空字符（见 topicsQueryIsBlank 注释），不得用 String.trim（会多裁
// U+FEFF）判定。u 标志必需：\p{...} 属性转义依赖 Unicode 模式（目标
// 浏览器 SW 环境均支持）。
const UNICODE_WHITESPACE_ONLY = /^\p{White_Space}*$/u;

// 逐跳/体长度头：HTML fallback 重建响应时必须过滤——body 已被读成文本，
// 原始 content-encoding/content-length/transfer-encoding 与重建后的
// Response 不再匹配，回传浏览器会拒绝解码或截断。
const HOP_BY_HOP_RESPONSE_HEADERS = [
  'content-encoding',
  'content-length',
  'transfer-encoding',
];

// HTML fallback 重建响应必须保留的安全响应头（除 Content-Type 单独补正
// 外）：缓存条目里对壳回放有意义的安全相关头原样带过，其余头不透传。
const SAFE_FALLBACK_RESPONSE_HEADERS = [
  'cache-control',
  'content-language',
  'content-security-policy',
  'content-security-policy-report-only',
  'cross-origin-opener-policy',
  'cross-origin-embedder-policy',
  'cross-origin-resource-policy',
  'permissions-policy',
  'referrer-policy',
  'strict-transport-security',
  'vary',
  'x-content-type-options',
  'x-frame-options',
];

// 私有/动态路径前缀：任何场景（导航、静态、taxonomy）都不允许进入缓存
// 路径。大小写规范化后比对，/API/、/Uploads/、/TTS/ 等变体一并拒绝。
//   - '/api/'：任何 API 响应，含 /api/report.svg、/api/doc.pdf 这类带文件
//     后缀的鉴权/个人数据端点，以及 OIDC 跳转这类以导航方式发起的 API。
//   - '/uploads/'：上传照片（用户内容，含他人的完成照片）。
//   - '/tts/'：按家务生成的朗读音频（家庭内容）。
const PRIVATE_PATH_PREFIXES = ['/api/', '/uploads/', '/tts/'];

// SPA 导航白名单：App.tsx 与 pages/kid/KidApp.tsx 路由定义的镜像（无参数
// 的精确路径；带参数的路由在 isSpaNavigationPath 里按严格字符集单独匹
// 配）。开发专用路由（/design）不注册 SW，不列入。App 的 /* 与 KidApp
// 的 * catch-all 兜底路由**故意不在列**：未知路径的导航直接走原生网络，
// 离线失败就失败——宁可少缓存，不把非白名单响应引入壳缓存。
const SPA_EXACT_PATHS = new Set([
  '/',
  '/login',
  '/setup',
  '/upload',
  '/admin',
  '/admin/dashboard',
  '/admin/reports',
  '/ambient',
  // KidApp（挂在 App 的 /* 下）的子路由
  '/week',
  '/history',
  '/errorbook',
  '/review',
  '/learn',
  '/rewards',
]);

// 当前既有固定壳文件（web/public/，构建时原样拷贝到 dist 根）。
// openchore-sw.js 故意不在列：SW 脚本由浏览器按自身的更新策略拉取，
// 缓存它只会让更新检查读到旧脚本。
const SHELL_ASSET_PATHS = new Set(['/favicon.svg', '/manifest.json', '/vite.svg']);

// 显式构建产物前缀：/assets/ 下真实存在的 Vite 指纹文件。
const ASSETS_PREFIX = '/assets/';

self.addEventListener('install', (event) => {
  event.waitUntil(
    (async () => {
      try {
        // 不用 cache.add：它内部 fetch+put，没有清理代数保护——install
        // 与登出清理（CLEAR_TAXONOMY_CACHES）竞态时会把壳条目写进刚被
        // 删除的缓存。改为显式 fetch + 请求起始 epoch + 写入前 epoch
        // 检查，与 networkFirst / stale-while-revalidate 同一准入。
        const epochAtInstall = cacheEpoch;
        // 用绝对 URL 构造请求（SW 全局 self.location 提供基址；相对 '/'
        // 在部分嵌入环境里无法解析）。
        const resp = await fetch(new Request(new URL(SHELL_URL, self.location.href), { cache: 'reload' }));
        // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：install 写 shell 同时
        // 走 isCacheableResponse + isSafeShellResponse，不再只做 HTML 检查——
        // 重定向（redirected）、opaque/cors/error、最终 URL 不同源或不在
        // SPA 白名单的响应一律不预缓存（isSafeShellResponse 内已含
        // text/html 检查）。任何 JSON（API 响应、服务端异常）绝不写入 '/'
        // 键被当成离线壳回放。
        if (epochAtInstall === cacheEpoch && isCacheableResponse(resp) && isSafeShellResponse(resp)) {
          const cache = await caches.open(SHELL_CACHE);
          // DEF-PWA-PRODUCTION-R2：caches.open 之后、put 之前重查 epoch——
          // open 挂起期间可能已发生 CLEAR（epoch 已变），此时放弃写入；
          // 写入改走 putQuietly（登记 inFlightPuts）：CLEAR 会等已登记的
          // put settle 之后再删除，install 的壳写入不再处于登记盲区。
          if (epochAtInstall === cacheEpoch) {
            putQuietly(cache, SHELL_URL, resp);
          }
        }
      } catch {
        // 预缓存失败不阻塞安装：导航请求在线成功时会补写 SHELL_URL。
      }
      await self.skipWaiting();
    })(),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    (async () => {
      let names = [];
      try {
        names = await caches.keys();
      } catch {
        // keys 失败：跳过旧代清理，不阻塞 claim。
      }
      // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：逐 cache deleteQuietly
      // 兜底（单个 delete reject/同步抛错都收敛），一个失败不阻止其余删除、
      // 更不阻止下面的 clients.claim——旧代清理是 best-effort 屏障，页面侧
      // 认证边界的两轮清理还有兜底。只清理本应用的旧代缓存：openchore- 前
      // 缀且不在当前 KNOWN_CACHES。同 origin 下其它应用的 cache 绝不动。
      await Promise.all(
        names
          .filter((n) => n.startsWith('openchore-') && !KNOWN_CACHES.includes(n))
          .map((n) => deleteQuietly(n)),
      );
      await self.clients.claim();
    })(),
  );
});

// 页面侧 clearTaxonomyCaches() 通过 postMessage 触发：登出即清空 taxonomy
// 缓存；static 一并清（兜底清掉旧版本 SW 按后缀误存的私有响应）。顺序
// （TX-PWA-SW-PRIVACY-FIX2）：先 cacheEpoch++（旧 epoch 写入立即全部作
// 废）→ 有界等待登记在途的 put settle（最多 PUT_SETTLE_TIMEOUT_MS，
// Promise.allSettled 语义：reject 也算 settle，不因单个坏 put 卡住）→
// 删 taxonomy 与 static 缓存 → ack。
self.addEventListener('message', (event) => {
  if (!event.data || event.data.type !== 'CLEAR_TAXONOMY_CACHES') return;
  const port = event.ports && event.ports[0];
  cacheEpoch += 1;
  const epochAtClear = cacheEpoch;
  const done = (async () => {
    // 在途 put settle 等待：快照一份当前集合（等待期间新登记的 put 属于
    // epoch 已变之后发起的写入，其调用侧 epoch 检查会放弃，不在此等待）。
    const pending = Array.from(inFlightPuts);
    await new Promise((resolve) => {
      const timer = setTimeout(resolve, PUT_SETTLE_TIMEOUT_MS);
      Promise.allSettled(pending).then(() => {
        clearTimeout(timer);
        resolve();
      });
    });
    // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：每个 caches.delete 单独
    // 兜底（reject/同步抛错都收敛）并**记录真实结果**——ack 携带
    // deleted/failed 数组（页面侧 clearTaxonomyCaches 据此判定本次清理是否
    // 成功：任一 delete 失败即不算成功，认证层不得推进 lastClearedUserId，
    // 后续 refresh 可重试）。一个失败不拖垮另一个——删除语义是 best-effort
    // 屏障，页面侧还有两轮兜底删除。Promise.all 里的 delete rejection 已被
    // deleteWithResult 吞掉，这里不再产生未处理 rejection。
    const deleted = [];
    const failed = [];
    await Promise.all(
      [TAXONOMY_CACHE, STATIC_CACHE].map((name) =>
        deleteWithResult(name).then((ok) => {
          (ok ? deleted : failed).push(name);
        }),
      ),
    );
    // 回执（AUTH-FIX1 冻结契约）：必须在 cacheEpoch 已递增、已在途 put
    // 已 settle（有界）、SW 自身删除完成之后发出，event.waitUntil 等待
    // 的就是包含本回执的这整段异步工作。页面等这个 ack 再删 openchore-*。
    // DEF-PWA-PRODUCTION-R2 第二轮：ack 携带 deleted/failed 真实删除结果。
    // 无 port（旧页面）时照常清理、不回执；端口已关闭等竞态下 postMessage
    // 可能抛错，吞掉——清理本身已完成，回执丢失可接受。
    if (port) {
      try {
        port.postMessage({
          type: 'CLEAR_TAXONOMY_CACHES_DONE',
          epoch: epochAtClear,
          deleted,
          failed,
        });
      } catch {
        // 端口已关闭：清理已完成，回执丢失可接受。
      }
    }
  })();
  event.waitUntil(done);
});

/** 删除单个 cache 并返回是否删除成功（reject/同步抛错都算失败，不 reject）。 */
function deleteWithResult(name) {
  try {
    return caches
      .delete(name)
      .then((ok) => !!ok)
      .catch(() => false);
  } catch {
    return Promise.resolve(false);
  }
}

/** 删除单个 cache，吞掉 rejection 与同步抛错：清理是 best-effort 屏障。 */
function deleteQuietly(name) {
  try {
    return caches.delete(name).catch(() => undefined);
  } catch {
    return Promise.resolve();
  }
}

/**
 * taxonomy / static 写入共用的最终响应闸门（DEF-PWA-PRODUCTION-R2 第二轮，
 * reviewer P1）。在 isCacheableResponse（ok / no-store / private）与 epoch
 * 检查之外，把 isSafeShellResponse 对壳写入的收紧同样施加到 taxonomy 与
 * 静态写入：
 *   1) 非 basic / default 类型一律拒绝：opaque（no-cors，头部不可读，
 *      Cache-Control/Content-Type 检查形同虚设）、cors、error 不进缓存。
 *   2) redirected 响应拒绝：重定向终点不是白名单端点本身。
 *   3) 最终 URL 与请求同源，且落在该请求的缓存白名单内（allowUrl：
 *      taxonomy 传 isCacheableTaxonomy、静态传 isStaticAsset——与 fetch
 *      handler 的准入完全同一白名单），防止重定向把请求带离白名单后仍
 *      写进共享缓存。
 * 生产网络响应的 resp.url 恒非空；同步构造的 Response（测试替身）没有
 * url，跳过第 3 项、其余闸门照常生效。
 */
function isSafeCachedResponse(resp, request, allowUrl) {
  if (!resp) return false;
  if (resp.type !== 'basic' && resp.type !== 'default') return false;
  if (resp.redirected) return false;
  const finalUrl = resp.url;
  if (!finalUrl) return true; // 同步构造的 Response（测试替身）：跳过最终 URL 检查。
  let url;
  try {
    url = new URL(finalUrl);
  } catch {
    return false; // 畸形最终 URL：拒绝。
  }
  if (url.origin !== self.location.origin) return false;
  return allowUrl(url, request);
}

/**
 * 解码并规范化 pathname。返回 null 表示畸形编码（decode 抛错），
 * 调用方一律按"不可缓存"处理。
 */
function decodedPathname(url) {
  try {
    return decodeURIComponent(url.pathname);
  } catch {
    return null;
  }
}

/** 响应最终 Content-Type 是否为 text/html（写入壳缓存的准入条件）。 */
function isHtmlResponse(resp) {
  const ct = (resp.headers && resp.headers.get('Content-Type')) || '';
  return /^text\/html\b/i.test(ct);
}

/**
 * SPA 导航路径是否含空白/控制字符（TX-PWA-SW-PRIVACY-FIX2）。
 * WHATWG URL 解析保留 %09/%0A/%0C/%0D、NBSP（%C2%A0）等转义原样在
 * pathname 里（服务端可能将其折叠或当成可导航路径），SW 一律不接管。
 * 覆盖：ASCII 0x00-0x20（含空格与全部 C0 控制字符）与 Unicode 空白
 * （NBSP U+00A0、U+1680、U+2000-U+200A、行分隔 U+2028、段分隔 U+2029、
 * U+202F、U+205F、词连接 U+2060、全角空格 U+3000、BOM U+FEFF）。
 * 字符类全部用 \\u 转义书写，U+2028/U+2029 不以字面量出现（旧 JS 引擎
 * 按行终止符解析会断行）。与真实 SPA 路由（纯小写字母/数字/路径分隔）
 * 天然不相交，fail closed 不损失任何合法导航。
 */
function hasBlankOrControlChars(path) {
  // eslint-disable-next-line no-control-regex
  return new RegExp(
    "[\\u0000-\\u0020\\u00a0\\u1680\\u2000-\\u200a\\u2028\\u2029\\u202f\\u205f\\u2060\\u3000\\ufeff]",
  ).test(path);
}

/**
 * 导航请求是否是白名单内的精确 SPA 路由（App.tsx / KidApp.tsx 路由定义
 * 的镜像）。fail closed：真实 SPA 路由全部由 ASCII 小写字母、数字与
 * hex token 组成，从不需要百分号编码——凡含 %（含双编码 %25xx、畸形
 * %zz、裸 %）、反斜杠、双斜杠、点段、空白/控制字符、非规范大小写私有
 * 前缀的路径一律不是 SPA 导航。/api、/uploads、/tts 与它们的一切编码/
 * 双编码/大小写/双斜杠变体、以及未列出的未知路径全部被拒，直接放行原生
 * 网络。
 */
function isSpaNavigationPath(url) {
  const p = url.pathname;
  if (p.includes('%')) return false; // 任何编码痕迹（含双编码、畸形编码）
  if (p.includes('\\')) return false; // 反斜杠
  if (p.includes('//')) return false; // 双斜杠（//api、/a//b）
  if (/(^|\/)\.\.?(\/|$)/.test(p)) return false; // . / .. 点段穿越
  if (hasBlankOrControlChars(p)) return false; // 空白/控制字符（含编码转义）
  // 非规范大小写私有前缀（/API/、/Uploads/、/Tts/…）显式拒绝。
  const lower = p.toLowerCase();
  if (PRIVATE_PATH_PREFIXES.some((prefix) => lower.startsWith(prefix))) return false;
  if (SPA_EXACT_PATHS.has(p)) return true;
  // 带参数的 SPA 路由，参数施加严格字符集（大小写敏感，与服务端的精确
  // 匹配一致）：
  //   /boost/{token}：boost 链接 token = 24 字节小写 hex（48 字符）；
  //   /share/taxonomy/{token}：诊断分享 token = 32 字节小写 hex（64 字符）；
  //   /course/{id}：课程数字 id。
  if (/^\/boost\/[0-9a-f]{48}$/.test(p)) return true;
  if (/^\/share\/taxonomy\/[0-9a-f]{64}$/.test(p)) return true;
  if (/^\/course\/\d+$/.test(p)) return true;
  return false;
}

/**
 * topics 的 q 参数是否全部为空/空白（TX-PWA-SW-PRIVACY-FIX2）。
 * 用 URLSearchParams 按 application/x-www-form-urlencoded 语义解析
 * query（与浏览器/服务端解析一致）：
 *   - 无任何 q：可缓存；
 *   - 所有 q 值按 Unicode White_Space 判空后为空（?q=、?q=%20、?q=+）：
 *     可缓存；
 *   - 任一 q 非空（含重复 q 中任一非空、?q=%E4%B8%80 类百分号编码后
 *     解析为非空）：network-only。
 * 语义与 URLSearchParams 的 + → 空格解码一致；"重复 q 中任一非空"由
 * getAll 遍历覆盖。绝不把带非空 q 的 URL 剥离 query 统一 key——非空 q
 * 的搜索结果是会话内输入，不进共享缓存。
 *
 * DEF-PWA-PRODUCTION-R2（reviewer P0）：判空必须与 Go strings.TrimSpace
 * 的空白集合对齐。原实现用 String.prototype.trim——ECMAScript trim 把
 * U+FEFF（BOM）也当空白裁掉，Go 不裁：?q=%EF%BB%BF 在 Go 侧是非空搜索词
 * （走 LIKE 过滤、结果按会话输入差异化），在 SW 侧却被判"空"进共享缓存，
 * 缓存了一个服务端按非空 q 计算的私有结果。改为显式 Unicode White_Space
 * 属性正则（\p{White_Space}）：
 *   - 只认 White_Space（U+0020、U+0009-U+000D、U+00A0、U+1680、
 *     U+2000-U+200A、U+2028、U+2029、U+202F、U+205F、U+3000）为空白；
 *   - U+FEFF、零宽/格式字符（U+200B-U+200D、U+2060-U+2064、U+FEFF）、
 *     任何可见字符一律非空 → network-only；
 *   - 空字符串（无 q）直接放行，不进正则路径（性能与语义都更直白）。
 * 逐值判空：getAll 覆盖"重复 q 中任一非空仍 network-only"。
 */
function topicsQueryIsBlank(url) {
  const params = new URLSearchParams(url.search);
  for (const value of params.getAll('q')) {
    if (value !== '' && !UNICODE_WHITESPACE_ONLY.test(value)) return false;
  }
  return true;
}

/** 同源 GET 且命中精确白名单的 taxonomy 数据请求。 */
function isCacheableTaxonomy(url) {
  if (url.origin !== self.location.origin) return false;
  // 先对整个 pathname 做 decodeURIComponent 规范化，再按解码结果匹配白名单：
  //   - 畸形编码（%zz、裸 %、孤立代理对）decode 抛错 → 一律不缓存；
  //   - %2F、%74opics 等编码形式解码后按真实路径判定，无法绕过白名单。
  const pathname = decodedPathname(url);
  if (pathname === null) return false;

  // 精确白名单（仅两个只读目录端点）。topics/{ref} 详情不在此列：
  // 服务端按角色差异化响应（admin 注入 answer/accept/explanation），
  // 共享缓存会成为越权面，一律 network-only。
  if (pathname === '/api/taxonomy/subjects') return true;
  // topics 列表：q 参数全部为空/空白才可缓存；任一非空 q（重复 q、
  // 百分号编码后非空、+ 解码出的空格除外——trim 后非空即拒）
  // network-only，不读缓存回放、不 put、不剥离 query 统一 key。
  if (pathname === '/api/taxonomy/topics') return topicsQueryIsBlank(url);
  // 其余全部 network-only：topics/{ref} 详情（含一切编码变体）、
  // diagnostics、blindspots、export、shared、admin、import、以及任何
  // 未列出的 taxonomy 路径。
  return false;
}

/**
 * 静态资源（显式 allowlist，不是后缀匹配）：
 *   - 真实构建产物 /assets/…（Vite 指纹文件，内容不可变）；
 *   - 当前既有固定壳文件（见 SHELL_ASSET_PATHS）。
 * 其余同源路径——含任何带静态后缀的私有/未知响应（/api/report.pdf、
 * /uploads/x.png、/openchore-sw.js、/foo.js）——一律不进缓存。可疑特
 * 征 fail closed：含 %、反斜杠、双斜杠、点段、非规范大小写私有前缀；
 * 编码后落入私有路径的变体（%75ploads、%2e%2e 穿越等）与畸形编码全部
 * 拒绝，不依赖黑名单正则放行。
 */
function isStaticAsset(url) {
  if (url.origin !== self.location.origin) return false;
  const p = url.pathname;
  const decoded = decodedPathname(url);
  // 畸形编码（%zz、裸 %、孤立代理对）decode 抛错 → 一律不缓存。
  if (decoded === null) return false;
  // 原始与解码后的 pathname 双重检查（纵深防御：解码后出现的点段/私有
  // 前缀即使未来放宽 % 一票否决也仍被拦下）。
  for (const path of [p, decoded]) {
    if (path.includes('%')) return false; // 编码残留（含双编码）
    if (path.includes('\\')) return false; // 反斜杠
    if (path.includes('//')) return false; // 双斜杠
    if (/(^|\/)\.\.?(\/|$)/.test(path)) return false; // . / .. 点段
    // 私有前缀大小写规范化后比对：/API/、/Uploads/、/TTS/ 一并拒绝。
    const lower = path.toLowerCase();
    if (PRIVATE_PATH_PREFIXES.some((prefix) => lower.startsWith(prefix))) return false;
  }
  return p.startsWith(ASSETS_PREFIX) || SHELL_ASSET_PATHS.has(p);
}

/** 响应是否被服务端标记为不可缓存（no-store / private）。 */
function isCacheableResponse(resp) {
  if (!resp || !resp.ok) return false;
  const cc = (resp.headers && resp.headers.get('Cache-Control')) || '';
  if (/\bno-store\b/i.test(cc)) return false;
  if (/(^|[,\s])private\b/i.test(cc)) return false;
  return true;
}

/**
 * 重建导航壳回放响应（TX-PWA-SW-PRIVACY-FIX2）。读取缓存 body 后重建
 * Response：
 *   1) Content-Type 显式补正为 text/html（调用方传入），防止缓存条目
 *      Content-Type 丢失时被当 opaque/unknown 类型渲染成下载或空白页；
 *   2) 过滤逐跳/体长度头（HOP_BY_HOP_RESPONSE_HEADERS：content-encoding /
 *      content-length / transfer-encoding）：body 已是读出的文本，原始值
 *      与重建后的 Response 不再匹配，原样回传浏览器会拒绝解码或截断；
 *   3) 只保留原安全响应头（SAFE_FALLBACK_RESPONSE_HEADERS：CSP/HSTS/
 *      X-Content-Type-Options/X-Frame-Options/Referrer-Policy/
 *      Permissions-Policy/COOP/COEP/CORP/Vary/Cache-Control 等），缓存壳
 *      条目带过的安全策略不因重建丢失；
 *   4) 白名单之外的头一律不透传（Set-Cookie 等绝不从缓存条目带回导航）。
 * body 为空的壳条目由调用方拦截（不回放空壳）。
 */
function buildShellFallbackResponse(cached, contentType) {
  const headers = new Headers();
  for (const [name, value] of cached.headers.entries()) {
    const lower = name.toLowerCase();
    // 逐跳/体长度头显式过滤（body 已重建，原值必然失配）。
    if (HOP_BY_HOP_RESPONSE_HEADERS.includes(lower)) continue;
    // 只保留安全响应头：白名单外的头不从缓存条目透传。
    if (!SAFE_FALLBACK_RESPONSE_HEADERS.includes(lower)) continue;
    headers.set(name, value);
  }
  headers.set('Content-Type', contentType);
  return headers;
}

/**
 * fire-and-forget 写缓存：catch 吞掉 reject（登出清缓存与在途写入竞争、
 * 配额超限等），绝不让它变成 unhandled rejection。
 * TX-PWA-SW-PRIVACY-FIX2：put promise 登记进 inFlightPuts，settle（无论
 * resolve/reject）后摘除——CLEAR 会先 epoch++ 再有界等这些在途 put
 * settle，把"删除与旧 put 落地交错"的窗口收窄。摘除发生在 settle 的
 * 同一 microtask，集合不会无界增长。
 * DEF-PWA-PRODUCTION-R2：cache.put **同步抛错**（cache 已被删除/冻结等）
 * 也走 settleAndRemove——tracked 立即 settle 并摘除，CLEAR 不必为一条
 * 根本没登记成功的 put 空等 PUT_SETTLE_TIMEOUT_MS 超时，也不产生
 * unhandled rejection。
 */
function putQuietly(cache, request, response) {
  let settle;
  const tracked = new Promise((resolve) => {
    settle = resolve;
  });
  inFlightPuts.add(tracked);
  const settleAndRemove = () => {
    inFlightPuts.delete(tracked);
    settle();
  };
  try {
    cache
      .put(request, response)
      .then(settleAndRemove, settleAndRemove)
      .catch(() => {
        // 双保险：settleAndRemove 自身抛错（理论不可达）也不产生 unhandled。
      });
  } catch {
    // put 同步抛错：立即摘除登记，不留挂死的 tracked。
    settleAndRemove();
  }
}

/**
 * 导航在线写 shell（'/' 键）前的最终响应闸门（DEF-PWA-PRODUCTION-R2，
 * reviewer P0）。
 *
 * networkFirst 在线成功时会把响应写进 SHELL_CACHE 的 SHELL_URL（'/'）键，
 * 之前只检查 Content-Type 是 text/html——一条被服务端重定向到别处、或代理
 * 回来的 opaque/cors 响应，只要 Content-Type 恰好是 HTML 就会被当作离线壳
 * 缓存，离线时对每个白名单 SPA 路由回放这个可能不同源/不同路由的内容。
 * 本闸门在写壳之前收紧：
 *   1) 非 basic 类型一律拒绝：opaque（no-cors，头部不可读，Content-Type
 *      检查形同虚设）、cors、error 等都不做壳。
 *   2) redirected 响应拒绝：服务端 3xx 之后的最终文档不是我们请求的路由，
 *      缓存它等于把重定向目标的内容固化为所有路由的离线壳。
 *   3) 最终 URL 的 origin 与 SW 一致（防跨源重定向终点被缓存为壳）。
 *   4) 最终 URL 的路径在精确 SPA 白名单里（isSpaNavigationPath 已含
 *      私有前缀、空白/控制字符、编码、点段、双斜杠等 fail-closed 检查）：
 *      重定向到 /api/...、未知路径等的 HTML 也不做壳。
 *
 * 关于 resp.url 为空：浏览器 fetch() 返回的网络响应其 url 恒为最终请求
 * URL（非空）；只有同步构造的 Response（如测试替身）没有 url。此时跳过
 * 第 3/4 项检查（其余闸门照常生效），与既有测试契约一致——生产网络响应
 * 不受此分支影响。
 */
function isSafeShellResponse(resp) {
  if (!resp) return false;
  // 只认 basic / default 响应：basic 是生产 SW 里同源 fetch 的响应类型；
  // default 只出现在程序构造的 Response（生产 fetch 不会返回），无跨源/
  // 不透明风险，一并放行以兼容同步构造的响应对象。真正要拒的是
  // opaque（no-cors，headers 不可读，Content-Type 闸门形同虚设）、
  // cors（跨源）、error。
  if (resp.type !== 'basic' && resp.type !== 'default') return false;
  // 重定向后的最终文档不是被请求路由本身：不做壳。
  if (resp.redirected) return false;
  // 最终响应头必须是 text/html（与 install 预缓存同一准入）。
  if (!isHtmlResponse(resp)) return false;
  const finalUrl = resp.url;
  if (!finalUrl) return true; // 同步构造的 Response（测试替身）：跳过最终 URL 检查。
  let url;
  try {
    url = new URL(finalUrl);
  } catch {
    return false; // 畸形最终 URL：拒绝。
  }
  if (url.origin !== self.location.origin) return false;
  return isSpaNavigationPath(url);
}

/** 网络优先；失败（离线）时回退到缓存。用于 SPA 导航壳与 taxonomy 白名单 GET。 */
async function networkFirst(request, cacheName, fallbackKey) {
  const epochAtRequest = cacheEpoch;
  // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：caches.open 失败（存储后端
  // 异常/被占用）时回退纯网络——在线请求绝不能因缓存基础设施故障而
  // reject；离线回退读取也无从谈起，直接 throw 让 respondWith 的外层
  // catch 兜底（导航路径有壳兜底，taxonomy 返回 Response.error()）。
  let cache;
  try {
    cache = await caches.open(cacheName);
  } catch {
    return fetch(request);
  }
  try {
    const resp = await fetch(request);
    if (isCacheableResponse(resp) && epochAtRequest === cacheEpoch) {
      // 写入键统一：带 fallbackKey（导航壳）时写 SHELL_URL，与 install 的
      // 显式预缓存同键——install 预缓存失败后，运行时导航在线成功也会补
      // 上 '/'，离线回退不至于无壳。代数已变（期间清理过缓存）则放弃写入：
      // 清理前已在途的响应不得把已删缓存复活。
      // DEF-PWA-PRODUCTION-R2：导航壳写入改走 isSafeShellResponse 完整闸门
      // ——basic、非重定向、最终 URL 同源且在精确 SPA 白名单、text/html。
      // 任何 JSON（API 响应误入导航、服务端异常响应）、重定向、opaque、
      // 跨源/非白名单终点绝不写入 '/' 键被当成离线壳回放。
      // DEF-PWA-PRODUCTION-R2 第二轮：taxonomy 写入（无 fallbackKey）同样
      // 走 isSafeCachedResponse 公共闸门——拒绝 redirected、非 basic/default
      // （opaque 的 Cache-Control 检查形同虚设）、最终 URL 不同源或已离开
      // taxonomy 白名单（重定向终点）的响应，见函数头注释。
      const safeToWrite = fallbackKey
        ? isSafeShellResponse(resp)
        : isSafeCachedResponse(resp, request, isCacheableTaxonomy);
      if (safeToWrite) {
        putQuietly(cache, fallbackKey || request, resp.clone());
      }
    }
    return resp;
  } catch (err) {
    // fallbackKey：导航壳回退固定读 SHELL_URL，其它请求回退读自身。
    const cached = await cache.match(fallbackKey || request);
    if (cached) {
      // 导航回退规范化响应（buildShellFallbackResponse）：text/html 显式
      // 补正、逐跳头过滤、安全头保留；空壳条目（body 为空字符串）不回放
      // ——继续走外层 catch 返回 Response.error()，绝不把空白成功响应当
      // 壳交给导航（不写空壳）。
      if (fallbackKey) {
        const body = await cached.text();
        if (!body) throw err;
        const headers = buildShellFallbackResponse(cached, 'text/html; charset=utf-8');
        return new Response(body, {
          status: cached.status,
          statusText: cached.statusText,
          headers,
        });
      }
      return cached;
    }
    throw err;
  }
}

/** stale-while-revalidate：先回缓存，同时后台更新。用于不可变静态资源。 */
async function staleWhileRevalidate(request, cacheName) {
  const epochAtRequest = cacheEpoch;
  // DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：caches.open 失败回退纯网
  // 络——在线请求绝不因缓存基础设施故障而 reject；无缓存可读时
  // await fetch(request)，失败收敛为 Response.error()，绝不 reject。
  let cache;
  try {
    cache = await caches.open(cacheName);
  } catch {
    try {
      return await fetch(request);
    } catch {
      return Response.error();
    }
  }
  let cached;
  try {
    cached = await cache.match(request);
  } catch {
    // match 失败（缓存实现异常）：视同无缓存，纯网络。
    cached = undefined;
  }
  const refresh = fetch(request)
    .then((resp) => {
      // 与 networkFirst 同一准入：no-store / private 的静态响应不进缓存；
      // 清理代数已变则放弃写入（CLEAR 也会删 static 缓存，防复活）。
      // DEF-PWA-PRODUCTION-R2 第二轮：写入同时走 isSafeCachedResponse 公共
      // 闸门——拒绝 redirected、非 basic/default、最终 URL 不同源或不在
      // 静态白名单（isStaticAsset）的响应，见函数头注释。
      if (
        isCacheableResponse(resp) &&
        isSafeCachedResponse(resp, request, isStaticAsset) &&
        epochAtRequest === cacheEpoch
      ) {
        putQuietly(cache, request, resp.clone());
      }
      return resp;
    })
    .catch(() => undefined);
  return cached || (await refresh) || Response.error();
}

self.addEventListener('fetch', (event) => {
  const { request } = event;
  if (request.method !== 'GET') return; // 写请求与不可缓存方法一律直连。

  const url = new URL(request.url);
  if (url.origin !== self.location.origin) return;

  if (request.mode === 'navigate') {
    // 仅精确 SPA 路由白名单进入 networkFirst；其余导航（/api、/uploads、
    // /tts 与其编码/双编码/大小写/双斜杠变体、未知路径）直接 return：
    // 不 respondWith、不读不写任何缓存，在线走原生网络，离线按浏览器
    // 原生行为失败。request.mode==='navigate' 本身不再授予任何缓存资格。
    if (!isSpaNavigationPath(url)) return;
    event.respondWith(
      networkFirst(request, SHELL_CACHE, SHELL_URL).catch(async () => {
        // caches.match(url, {cacheName}) 在部分实现里不合法（CacheStorage.match
        // 只接受 ignoreSearch/ignoreMethod/ignoreVary），改从指定 cache 取。
        // 兜底再查一次壳缓存；空/缺失条目一律返回 Response.error()——
        // 绝不把空白成功响应当导航结果。
        const cache = await caches.open(SHELL_CACHE);
        const cached = await cache.match(SHELL_URL);
        if (cached) {
          const body = await cached.text();
          if (body) {
            const headers = buildShellFallbackResponse(cached, 'text/html; charset=utf-8');
            return new Response(body, {
              status: cached.status,
              statusText: cached.statusText,
              headers,
            });
          }
        }
        return Response.error();
      }),
    );
    return;
  }

  if (isCacheableTaxonomy(url)) {
    // DEF-PWA-PRODUCTION-R2 第二轮：taxonomy 写入的 isSafeCachedResponse
    // 闸门在 networkFirst 内生效（最终 URL 仍在 taxonomy 白名单）。
    event.respondWith(
      networkFirst(request, TAXONOMY_CACHE).catch(() => Response.error()),
    );
    return;
  }

  if (isStaticAsset(url)) {
    event.respondWith(staleWhileRevalidate(request, STATIC_CACHE));
  }
  // 其余（/api/auth、/api/admin/*、topics/{ref} 详情、diagnostics、export、
  // 非 allowlist 的同源静态、跨域……）直接放行，不缓存、离线不回放。
});
