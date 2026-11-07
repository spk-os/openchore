// PWA Service Worker 注册器（卡 TX-PWA-FIX / TX-PWA-AUTH-FIX2）。
//
// main.tsx 在应用挂载前调用 registerServiceWorker()；生产构建（Vite build 产物）
// 才注册，开发服务器（HMR 会与 SW 缓存互相干扰）不注册。
//
// clearTaxonomyCaches() 暴露给认证流程（AuthContext 的登录/切换用户/登出/
// SESSION_EXPIRED 集成）：SW 收到消息后删除 taxonomy 缓存，避免下一用户读到
// 上一会话的数据。返回是否清理可观测成功（DEF-PWA-PRODUCTION-R2 第二轮），
// 调用方失败时不推进 lastClearedUserId（后续 refresh 可重试）。
//
// TX-PWA-AUTH-FIX2（与 SW 侧 CLEAR_TAXONOMY_CACHES_DONE 回执契约配套）：
//   1) 发消息时创建 MessageChannel，port2 随 postMessage 转移给 SW（SW 侧
//      即 event.ports[0]）；SW 在自身 cacheEpoch 自增、taxonomy/static 缓存
//      删除完成之后经该端口回执。
//   2) 页面真正 await 这个 ack（有限超时 SW_ACK_TIMEOUT_MS：SW 被终止、回执
//      丢失、旧版本 SW 不回执时不永久阻塞认证流程）之后，才枚举删除所有
//      'openchore-*' cache。顺序是防"缓存复活"的关键：SW 的代数自增与删除
//      先落地，页面兜底删除才不会与 SW 在途写入交错。
//   3) 超时/无 SW/SW 竞态一律走同一页面兜底清理；timer/port 收尾清理，避免
//      泄漏。函数内部兜住所有 rejection：绝不 unhandled、绝不阻塞认证。
//
// DEF-PWA-CLEAR-REVIVAL（reviewer M1）：一次 ack/timeout 后页面的 delete 仍
// 可能与 CLEAR 之前就已开始的 cache.put 交错——put 在页面 delete 完成后才
// 落地，把刚清掉的缓存复活。因此清理跑两轮：CLEAR(ack) → 页面删除 →
// 第二次 CLEAR(ack) → 再次页面删除。两轮复用同一个 SW 实例，不重新选择；
// 不做第三次 CLEAR，也不引入任何 sleep。
//
// 屏障承诺是**分级**的，注释如实区分：
//   - ack 路径（SW 在超时窗口内回执 CLEAR_TAXONOMY_CACHES_DONE 且删除
//     无失败）：SW 已完成代数自增、已等其登记的在途 put 有界 settle 并
//     完成自身删除，正常 SW 的旧在途写入因代数检查作废，页面删除不与其
//     交错——这是对"已登记且窗口内 settle 的写入"的屏障。
//   - timeout 降级（SW 被终止/卡死/回执丢失/旧版本 SW 不回执）：只有有界
//     best-effort 的页面删除兜底，**不构成**对坏/旧 SW 的绝对写入屏障——
//     旧 SW 没有代数防护，其丢给删除完成后落地的 put 无法从页面侧杜绝
//     （cache.put 不可取消）。剩余窗口由 SW 侧 in-flight settle 追踪与
//     收窄后的缓存白名单（仅公开 subjects / 无参 topics；详情与带参列表
//     不缓存）兜住。
//
// DEF-PWA-PRODUCTION-R2（reviewer P1）：整个清理**永不 reject 且有界**。
// 之前只有 SW ack 有超时，getRegistration、caches.keys、每个 caches.delete
// 与 MessageChannel 构造都可能挂起/抛错——CacheStorage 永不 settle 就会卡死
// 登录/登出。现在：
//   - 每个异步步骤（getRegistration / keys / 单个 delete）各自套
//     withBound（超时 + reject/同步抛错都算 settle，降级继续）；
//   - MessageChannel 构造、onmessage 赋值、postMessage 全部在外层 try 内，
//     任何异常立即降级为页面兜底删除，promise 永不 reject；
//   - clearTaxonomyCaches 整体以 CLEAR_TOTAL_TIMEOUT_MS 为总 deadline：
//     即使各步超时叠加（最坏两轮 ack + 两轮删除 + getRegistration），认证
//     流程最多被阻塞这么多毫秒。deadline 耗尽后剩余步骤跳过（后台仍在跑
//     的删除自身永不 reject，不会产生 unhandled rejection）——完全降级环境
//     下第二轮可能被截断，返回 false，这是有界 best-effort 语义的一部分，
//     不是缺陷：本函数从不声称对坏/旧 SW 有绝对约束。
//
// DEF-PWA-PRODUCTION-R2 第二轮（reviewer P1）：
//   - 可观测成功布尔：clearTaxonomyCaches 返回 Promise<boolean>——两轮
//     CLEAR 的 ack 均在超时内收到、SW ack 的 failed 列表为空、两轮页面
//     删除全部成功才 true；任一 SW ack 超时、任一 delete 失败/超时均
//     false。调用方（AuthContext）失败时不推进 lastClearedUserId（后续
//     refresh 可重试），但清理失败不阻塞认证本身。
//   - 无 controller 但 registration.active 存在时（SW 已激活但尚未控制
//     本页），对该 active SW 同样做两轮 CLEAR+页面删除，不能只做页面
//     删除——active SW 的代数与在途 put 只有经 CLEAR 才能收敛。
//   - timer 语义如实化：清理期间同时存在的 timer 数量**有界**（每个在途
//     有界操作一个：ack 等待或单个 delete 超时），不是也不需要是"全局
//     单 timer"；总时长仍由 deadline 传播硬上限兜住。
//
// DEF-PWA-PRIVACY：页面侧兜底清理不再只删名字含 'taxonomy' 的 cache，改为
// 清理**本应用所有** 'openchore-*' cache——旧版本 SW 的 taxonomy 缓存里可能
// 残留带 answer/explanation 的 topics/{ref} 详情，旧 static 缓存里也可能有按
// 后缀误存的私有响应，登出/切换用户时必须一并清掉。只匹配 'openchore-' 前缀，
// 同 origin 下非本应用的 cache（其它 app 的存储）不受影响；全部 fail-safe，
// 清理失败绝不阻塞认证流程。

export const SW_URL = '/openchore-sw.js';
export const CLEAR_TAXONOMY_MESSAGE = 'CLEAR_TAXONOMY_CACHES';

// SW 完成回执的消息类型（openchore-sw.js 的 message 处理器在代数自增、
// 自身删除完成后经 event.ports[0] 发出）。不导出：页面侧只消费。
// 回执 payload：{type, epoch, deleted: string[], failed: string[]}
// （DEF-PWA-PRODUCTION-R2 第二轮：SW 在 ack 里携带真实删除结果，页面侧
// 据此判定本次清理是否成功）。旧版本 SW 的回执没有 deleted/failed——
// 解析时按缺失处理为"结果未知"，不视为成功。
const SW_ACK_MESSAGE = 'CLEAR_TAXONOMY_CACHES_DONE';

// SW 回执的删除结果形状（缺失按结果未知处理）。
interface ClearAck {
  epoch?: unknown;
  deleted?: unknown;
  failed?: unknown;
}

// 等待 SW 回执的有限超时：超时说明 SW 未回执（已终止、回执丢失、旧版本 SW
// 还不回执），立即走页面兜底清理——清理本身幂等，慢一点可接受，永久阻塞
// 登出/登录流程不可接受。
const SW_ACK_TIMEOUT_MS = 2000;

// 单个 CacheStorage / ServiceWorkerContainer 异步操作的上限（getRegistration、
// caches.keys、单个 caches.delete）：操作挂死（存储后端卡死等）时降级继续，
// 不让它单独卡死整个清理。
const CACHE_OP_TIMEOUT_MS = 2000;

// clearTaxonomyCaches 整体的硬上限（DEF-PWA-PRODUCTION-R2）：以 deadline
// （Date.now() + 此值）向内传播，每一步的超时取 min(剩余 deadline, 步骤
// 上限)，deadline 耗尽后剩余步骤跳过——清理全程最多阻塞认证流程这么
// 多毫秒，且任意时刻最多只有一个活动 timer。正常 ack 路径全部步骤在
// 毫秒级完成，此上限只在完全降级环境（CacheStorage/SW 挂死）生效。
const CLEAR_TOTAL_TIMEOUT_MS = 8000;

let registration: ServiceWorkerRegistration | null = null;

/** 是否处于生产构建环境（import.meta.env 由 Vite 注入）。 */
function isProd(): boolean {
  return import.meta.env.PROD === true;
}

/**
 * 有界等待（DEF-PWA-PRODUCTION-R2）：p settle（resolve 与 reject 都算）或
 * 超时先到，二者取先。永不 reject：p 的 rejection 被当作 settle 降级为
 * undefined；超时后 p 才 settle 的 rejection 也已被本函数的 handler 消化，
 * 不会变成 unhandled rejection。p 抛同步异常由调用方的 try/catch 兜。
 * ms <= 0 时（deadline 已耗尽）立即返回，不注册任何 timer、不等待 p——
 * 调用方以此实现"总 deadline 传播 + 每步有界"。
 */
function withBound<T>(p: Promise<T>, ms: number): Promise<T | undefined> {
  return new Promise<T | undefined>((resolve) => {
    if (ms <= 0) {
      resolve(undefined);
      // p 的后续 rejection 必须被消化，不能变成 unhandled。
      p.then(
        () => undefined,
        () => undefined,
      );
      return;
    }
    const timer = setTimeout(() => resolve(undefined), ms);
    p.then(
      (value) => {
        clearTimeout(timer);
        resolve(value);
      },
      () => {
        clearTimeout(timer);
        resolve(undefined);
      },
    );
  });
}

/** 注册 Service Worker；非生产环境或浏览器不支持时静默跳过。 */
export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null;
  if (!isProd()) return null;
  try {
    registration = await navigator.serviceWorker.register(SW_URL);
    return registration;
  } catch {
    // 注册失败不阻塞应用：退化为纯在线模式。
    return null;
  }
}

/** 静默关闭一个 MessagePort：端口已关闭/已转移（neutered）等竞态吞掉。 */
function closePortQuietly(port: MessagePort | null): void {
  if (!port) return;
  try {
    port.close();
  } catch {
    // 端口关闭竞态：无影响。
  }
}

/**
 * 页面侧清理：枚举并删除所有本应用 'openchore-*' cache（只此前缀）。
 * 在 SW 回执 ack（或超时/无 SW 的兜底路径）之后调用——SW 侧已完成自身
 * epoch 自增与缓存删除，页面这一遍兜住 SW 未接管/旧版本 SW 的残留（旧
 * taxonomy 缓存里可能有私有 topics/{ref} 详情、旧 static 缓存里可能有按
 * 后缀误存的私有响应）；非本应用的 cache 不动。准入条件是 caches API 的
 * 可用性（typeof caches === 'undefined' 即不支持，直接返回）：非 secure
 * context 下若浏览器仍暴露 caches（少见但存在），删除照常执行——清理
 * 本来就是隐私兜底，能删尽删；不能删也不影响认证流程。
 *
 * DEF-PWA-PRODUCTION-R2：keys 与每个 delete 都套 withBound（超时取
 * min(剩余总 deadline, CACHE_OP_TIMEOUT_MS)）——CacheStorage 永不 settle
 * 时本轮最多等这么久即放弃（外层还有第二轮），同步抛错/reject 一律内部
 * 吞掉。本函数永不 reject。
 *
 * DEF-PWA-PRODUCTION-R2 第二轮：返回是否本轮删除**全部成功**——keys
 * 超时/抛错（无法核实是否删净）、任一 delete 超时/失败均 false；一个
 * openchore-* cache 都没有也算成功（无可删即已清）。
 */
async function deleteOpenchoreCaches(deadline: number): Promise<boolean> {
  if (typeof caches === 'undefined') return false;
  let names: string[] | undefined;
  try {
    names = await withBound(caches.keys(), Math.min(deadline - Date.now(), CACHE_OP_TIMEOUT_MS));
  } catch {
    // keys 同步抛错：本轮放弃（结果不可核实），外层还有第二轮。
    return false;
  }
  if (!names) return false; // deadline 耗尽：本轮放弃。
  const targets = names.filter((n) => n.startsWith('openchore-'));
  if (targets.length === 0) return true; // 无可删：视为已清。
  // 每个 delete 独立兜底：单个 delete 挂死/抛错不拖累其余删除的发起。
  const results = await Promise.all(targets.map((n) => deleteCacheQuietly(n, deadline)));
  return results.every((ok) => ok);
}

/**
 * 删除单个 cache（有界 + 异常兜底）：永不 reject、永不阻塞调用方。
 * 返回是否删除成功（超时/reject/同步抛错均 false）。
 */
async function deleteCacheQuietly(name: string, deadline: number): Promise<boolean> {
  try {
    const ok = await withBound(
      caches.delete(name),
      Math.min(deadline - Date.now(), CACHE_OP_TIMEOUT_MS),
    );
    return ok === true;
  } catch {
    // 清理失败不影响认证流程，但结果记为失败。
    return false;
  }
}

/**
 * 向 SW 发送 CLEAR 消息并等待其回执（TX-PWA-AUTH-FIX2 契约）。
 * - 创建 MessageChannel：port2 随消息转移给 SW，SW 在 cacheEpoch 自增、
 *   taxonomy/static 缓存删除完成后经 event.ports[0] 回执（payload 含
 *   deleted/failed 真实删除结果），页面在 port1 收。
 * - 只认 type 为 CLEAR_TAXONOMY_CACHES_DONE 的回执；端口反序列化失败
 *   （onmessageerror）视同回执失败，立即走超时同款降级路径。
 * - 有限超时：SW 卡死/被终止/回执丢失时不永久阻塞，超时即 resolve，由
 *   调用方走页面兜底清理。
 * - settle 后清 timer、关两个 port（DEF-PWA-PRODUCTION-R2 minor：port1 是
 *   页面侧端口；port2 在 postMessage 成功后已被转移（neutered，close 为
 *   no-op），在 postMessage 抛错未转移成功时仍归页面，必须关闭防泄漏）。
 * - DEF-PWA-PRODUCTION-R2：MessageChannel 构造、onmessage 赋值、postMessage
 *   全在外层 try 内——任何一步异常立即 finish() 降级。本函数永不 reject。
 * - DEF-PWA-PRODUCTION-R2 第二轮：返回 SW 侧删除结果——超时/降级/回执
 *   缺失 deleted/failed（旧版本 SW）为 null（结果未知，不算成功）；
 *   正常回执返回 {ok: failed 列表为空}。deleted/failed 校验宽松
 *   （Array.isArray），畸形 payload 按结果未知处理。
 */
function clearViaServiceWorker(sw: ServiceWorker, timeoutMs: number): Promise<{ ok: boolean } | null> {
  return new Promise<{ ok: boolean } | null>((resolve) => {
    let settled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let port1: MessagePort | null = null;
    let port2: MessagePort | null = null;
    const finish = (result: { ok: boolean } | null) => {
      if (settled) return;
      settled = true;
      if (timer !== undefined) clearTimeout(timer);
      closePortQuietly(port1);
      closePortQuietly(port2);
      resolve(result);
    };
    // 总 deadline 已耗尽：不再发 CLEAR、不再建 channel，直接降级为页面兜底
    // 删除（调用方随后照常 deleteOpenchoreCaches），结果未知。
    if (timeoutMs <= 0) {
      resolve(null);
      return;
    }
    // 外层 catch（DEF-PWA-PRODUCTION-R2 minor）：executor 内任何异常
    // （MessageChannel 不可用、端口冻结、postMessage 抛错等）都降级为
    // 立即 resolve(null)，绝不让本 promise reject。
    try {
      const { port1: p1, port2: p2 } = new MessageChannel();
      port1 = p1;
      port2 = p2;
      port1.onmessage = (event: MessageEvent) => {
        // 只认 SW 的完成回执（CLEAR_TAXONOMY_CACHES_DONE）：其自身 epoch 自增
        // 与缓存删除已完成。其它消息不算 ack，继续等（超时兜底）。
        const data = event.data as (ClearAck & { type?: unknown }) | null | undefined;
        if (!data || data.type !== SW_ACK_MESSAGE) {
          return;
        }
        // DEF-PWA-PRODUCTION-R2 第二轮：ack 携带 deleted/failed 真实删除
        // 结果。旧版本 SW 的回执没有这两个字段——按结果未知（null）处理，
        // 不视为成功；正常回执以 failed 列表是否为空判定成功。
        if (!Array.isArray(data.deleted) || !Array.isArray(data.failed)) {
          finish(null);
          return;
        }
        finish({ ok: data.failed.length === 0 });
      };
      port1.onmessageerror = () => {
        // 端口收到无法反序列化的消息：SW 回执不可达，立即降级（结果未知）。
        finish(null);
      };
      timer = setTimeout(() => finish(null), Math.min(timeoutMs, SW_ACK_TIMEOUT_MS));
      sw.postMessage({ type: CLEAR_TAXONOMY_MESSAGE }, [port2]);
    } catch {
      // MessageChannel 构造失败 / SW 已终止等竞态：立即走页面兜底清理。
      finish(null);
    }
  });
}

/**
 * 清理本体（不直接导出）：全部步骤有界且永不抛错，供 clearTaxonomyCaches
 * 套总 deadline 后调用。deadline（Date.now() 毫秒时间戳）向每一步传播，
 * 每步超时取 min(剩余 deadline, 该步上限)；deadline 耗尽后剩余步骤跳过。
 *
 * DEF-PWA-PRODUCTION-R2 第二轮：返回本次清理是否**可观测成功**——两轮
 * SW CLEAR 的 ack 均收到且 failed 为空、两轮页面删除均全部成功才 true；
 * 任一 ack 超时/降级/结果未知、任一 delete 失败或超时均 false。无 SW
 * 路径只要求页面删除成功。控制器缺失但 registration.active 存在时（SW
 * 已激活但尚未控制本页），对该 active SW 同样走两轮 CLEAR+页面删除——
 * active SW 的代数与在途 put 也必须收敛，不能只做页面删除。
 */
async function clearCachesBestEffort(deadline: number): Promise<boolean> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) {
    return deleteOpenchoreCaches(deadline);
  }
  let sw: ServiceWorker | null = null;
  try {
    sw = navigator.serviceWorker.controller ?? null;
  } catch {
    sw = null;
  }
  if (!sw) {
    // DEF-PWA-PRODUCTION-R2 第二轮：无 controller 时不能只做页面删除——
    // registration.active 的 SW 已在跑（只是尚未控制本页），它的缓存写入
    // 同样要经两轮 CLEAR 收敛代数、等在途 put settle。getRegistration 有
    // 界等待 + 异常兜底：个别浏览器状态下它会 reject/throw/永不 settle
    // （刚登出的竞态窗口），一律降级为无 SW 路径。
    try {
      const reg = await withBound(
        navigator.serviceWorker.getRegistration(),
        Math.min(deadline - Date.now(), CACHE_OP_TIMEOUT_MS),
      );
      sw = reg?.active ?? null;
    } catch {
      sw = null;
    }
  }
  if (!sw) return deleteOpenchoreCaches(deadline);
  // 对选定的 SW（controller，或无 controller 时的 registration.active）做
  // 两轮 CLEAR(ack) + 页面删除：
  //   - 第一轮：await SW ack/超时（不是 fire-and-forget postMessage）之后
  //     才做页面删除。SW 侧删除先落地、页面删除在后，是防"已删缓存被在
  //     途写入复活"的关键顺序。
  //   - 第二轮屏障（DEF-PWA-CLEAR-REVIVAL）：第一轮时可能仍有 CLEAR 之前
  //     就已开始、尚未 settle 的 cache.put 在途；页面这一遍 delete 与它们
  //     交错时，put 会在删除完成后落地，把刚清掉的缓存复活。向**同一个**
  //     SW 实例再发一次 CLEAR 并 await ack/有限超时，随后再删一遍页面缓存。
  //     ack 路径：正常 SW 的代数再次自增，其旧在途写入因代数检查作废。
  //     timeout 降级：只剩有界 best-effort 的页面删除，不构成对坏/旧 SW
  //     的绝对写入屏障（旧 SW 无代数防护）。
  //   - 成功判定：两轮 ack 均收到且 failed 为空、两轮页面删除均成功；
  //     任一环节超时/失败即 false（调用方据此不推进 lastClearedUserId）。
  // 不做第三次 CLEAR、无 sleep。
  const ack1 = await clearViaServiceWorker(sw, deadline - Date.now());
  const del1 = await deleteOpenchoreCaches(deadline);
  const ack2 = await clearViaServiceWorker(sw, deadline - Date.now());
  const del2 = await deleteOpenchoreCaches(deadline);
  return ack1?.ok === true && ack2?.ok === true && del1 && del2;
}

/**
 * 清空本应用的缓存（认证边界时调用）。
 * 有 SW（controller，回退 registration.active）时先 await SW 的 ack 或超时，
 * 然后页面枚举删除所有 'openchore-*' cache；无 SW（浏览器不支持、未注册）
 * 时直接页面清理。调用方（AuthContext）await 本函数并再 catch 一层双保险。
 *
 * DEF-PWA-PRODUCTION-R2：整个清理以 CLEAR_TOTAL_TIMEOUT_MS 为总 deadline
 * （Date.now() + 上限）向每一步传播，每步超时取 min(剩余 deadline, 该步
 * 上限)——内部任何一步（含 CacheStorage 永不 settle）最坏也只能阻塞认证
 * 流程 CLEAR_TOTAL_TIMEOUT_MS 毫秒，deadline 耗尽后剩余步骤直接跳过。
 * 仍在后台跑的删除自身永不 reject。本函数永不 reject。
 *
 * DEF-PWA-PRODUCTION-R2 第二轮：返回 Promise<boolean>——本次清理是否可
 * 观测成功（两轮 ack 收到且 SW 侧无失败、两轮页面删除全部成功）。超时、
 * delete 失败、回执缺失（旧 SW）均不算成功；调用方失败时不推进
 * lastClearedUserId（后续 refresh 可重试），清理失败不阻塞认证。
 */
export async function clearTaxonomyCaches(): Promise<boolean> {
  return clearCachesBestEffort(Date.now() + CLEAR_TOTAL_TIMEOUT_MS);
}
