// 学科列表共享 hook（卡 DEF-DYNAMIC-SUBJECTS-R2-CODER）。
//
// 为什么运行时不得出现十学科硬编码清单：学科导航数据一律以服务端
// GET /api/taxonomy/subjects（subjects 表驱动）返回为准。失败时不回退
// 十学科清单冒充成功——服务端只返 math/history 时消费者就只能看到这两项；
// 空数组成功响应也按缓存规则走，让 UI 呈现真实的服务端空态（空数组合法，
// 各消费方都有 refresh/retry 兜底）。
//
// 共享订阅（R2，修复 reviewer P1「多消费者状态不同步」）：状态收进单一
// 模块级 store，经 useSyncExternalStore 一致广播——
//  - subjects/loading/error 同源：所有已挂载消费者读同一份 snapshot，
//    任一消费者的 retry 成功/失败，所有消费者同一轮同步更新，不再有
//    每组件各自 useState、互相看不见的副本；
//  - inflight：在途 Promise 去重——并发挂载、retry 撞上在途请求都复用
//    同一个 Promise（StrictMode 的双跑 effect 也被它吸收）；
//  - cached/cachedAt：成功快照 + 写入时间戳，新鲜期 TTL_MS 内的后续挂载
//    零请求、不闪 loading；过期后（含空数组快照过期）重新请求。TTL 描述
//    的是这份缓存数据的新鲜期，不硬编码任何学科语义；
//  - 失败：不写新缓存，旧快照保留作 stale 数据（subjects 不动，消费者仍
//    拿旧清单用），error 必须可见；retry 强制绕过新鲜缓存重新请求。
//
// 契约：taxonomyApi.subjects() 返回 { subjects: SubjectRow[] | null }，
// null 与缺键都统一落成空数组，返回值 subjects 永远 non-nil。
//
// useSyncExternalStore 稳定性要求：getSnapshot 返回模块级 state 单例，
// 只在 setState 里整体替换为新对象（绝不原地 mutate），未通知期间引用
// 不变，不会造成无限重渲染。

import { useCallback, useEffect, useSyncExternalStore } from 'react';
import { taxonomyApi } from '../taxonomy/client';
import type { SubjectRow } from '../taxonomy/types';

export interface UseSubjectsResult {
  /** 服务端学科行；尚无成功快照时为空数组，永不为 null/undefined。 */
  subjects: SubjectRow[];
  loading: boolean;
  /** 最近一次失败（TaxonomyAPIError 或网络错误）；成功后为 null。保留 stale 快照时仍非 null。 */
  error: unknown;
  /** 失败后手动重试：强制绕过新鲜缓存重新请求（已有在途请求仍去重复用）。 */
  retry: () => void;
}

/** 成功缓存的新鲜期：期内挂载零请求，过后由新消费者重新拉取。 */
const TTL_MS = 60_000;

/** 模块级共享 snapshot 形状：整体替换（不 mutate），保证 getSnapshot 引用稳定。 */
interface SubjectsState {
  subjects: SubjectRow[];
  loading: boolean;
  error: unknown;
}

// 模块级单例：共享状态、成功缓存（快照 + 时间戳）与在途请求全 app 一份。
const EMPTY: SubjectRow[] = [];
let state: SubjectsState = { subjects: EMPTY, loading: false, error: null };
let cached: SubjectRow[] | null = null;
let cachedAt = 0;
let inflight: Promise<SubjectRow[]> | null = null;
const listeners = new Set<() => void>();

function isFresh(): boolean {
  return cached != null && Date.now() - cachedAt < TTL_MS;
}

/** 替换共享 snapshot 并广播所有已挂载消费者。 */
function setState(next: Partial<SubjectsState>): void {
  state = { ...state, ...next };
  for (const notify of listeners) notify();
}

function load(force: boolean): Promise<SubjectRow[]> {
  if (!force && isFresh()) return Promise.resolve(cached!);
  if (!inflight) {
    // 发出请求即 loading、清 error；成功写缓存，失败保留旧快照只置 error。
    setState({ loading: true, error: null });
    inflight = taxonomyApi
      .subjects()
      .then((res) => {
        const rows = res.subjects ?? [];
        cached = rows;
        cachedAt = Date.now();
        inflight = null;
        setState({ subjects: rows, loading: false, error: null });
        return rows;
      })
      .catch((err: unknown) => {
        // 失败不写新缓存：旧快照留作 stale 数据，清在途标记让 retry 真正重发。
        inflight = null;
        setState({ loading: false, error: err });
        throw err;
      });
  }
  return inflight;
}

function subscribe(notify: () => void): () => void {
  listeners.add(notify);
  return () => {
    listeners.delete(notify);
  };
}

/** useSyncExternalStore 的 snapshot：模块级单例，仅在 setState 时整体替换。 */
function getSnapshot(): SubjectsState {
  return state;
}

/** 挂载时的缓存判定：新鲜（含空数组快照）零请求，否则真实拉取。 */
function ensureLoaded(): void {
  if (isFresh()) {
    // 缓存被后来者更新过（如 retry 拿到新数据）而对齐共享状态；同引用时零开销。
    if (state.subjects !== cached) setState({ subjects: cached!, loading: false, error: null });
    return;
  }
  // 失败经 store 广播；这里吞掉未处理 rejection（订阅者经 snapshot 感知 error）。
  void load(false).catch(() => {});
}

export function useSubjects(): UseSubjectsResult {
  const snap = useSyncExternalStore(subscribe, getSnapshot);

  // 挂载按需拉取；ensureLoaded 读模块级缓存判定，幂等（StrictMode 双跑被
  // inflight/isFresh 吸收），无需依赖数组。
  useEffect(ensureLoaded, []);

  const retry = useCallback(() => {
    // 强制绕过新鲜缓存；在途请求复用（load 内去重）。失败同样只进 store。
    void load(true).catch(() => {});
  }, []);

  return { subjects: snap.subjects, loading: snap.loading, error: snap.error, retry };
}
