// @vitest-environment jsdom
// 冻结隐私契约（TX-PWA-AUTH-AWAIT-TESTER）：
// - signIn / refresh 身份切换 / SESSION_EXPIRED / signOut 必须 await
//   clearTaxonomyCaches 的整个 promise（模拟 SW 双轮清理）后才改身份；
// - 首次匿名 refresh 失败不清公共缓存；首次 null → 有效 id 必须清；
//   同一 id 重复 refresh 不重复清；清理失败不阻塞登录、恢复或退出；
// - 旧同步 act 全部改为 await act；两段式 gate 在单个 act 内分阶段放行，
//   afterEach 统一排空未完成的 promise，避免异步泄漏与跨测试计数污染。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor, act } from '@testing-library/react';
import { SESSION_EXPIRED_EVENT } from './api';
import { AuthProvider, useAuth } from './AuthContext';
import type { AuthSession, User } from './types';

vi.mock('./api', async () => {
  const actual = await vi.importActual<typeof import('./api')>('./api');
  return {
    ...actual,
    api: {
      auth: {
        me: vi.fn(async () => { throw new Error('signed out'); }),
        options: vi.fn(async () => ({ pin_sign_in: true, start_page: 'picker' })),
        logout: vi.fn(async () => {}),
      },
    },
  };
});

// 双轮真实实现由 pwa 测试覆盖；此处用「两轮都完成才 resolve」的 promise 验证
// AuthContext 等的是整个清理屏障，而不是某一轮 SW postMessage。
const { clearTaxonomyCaches } = vi.hoisted(() => ({
  clearTaxonomyCaches: vi.fn(async (): Promise<boolean> => true),
}));
vi.mock('./pwa', () => ({ clearTaxonomyCaches }));

import { api } from './api';

type UnhandledRejectionListener = (reason: unknown, promise: Promise<unknown>) => void;
interface NodeProcess {
  on?: (event: 'unhandledRejection', listener: UnhandledRejectionListener) => void;
  off?: (event: 'unhandledRejection', listener: UnhandledRejectionListener) => void;
}
interface UnhandledWatcher {
  onUnhandled: ReturnType<typeof vi.fn>;
  stop: () => void;
}

const nodeProcess = (globalThis as unknown as { process?: NodeProcess }).process;
const activeUnhandledListeners: UnhandledRejectionListener[] = [];

const meOf = (user: User): AuthSession => ({
  user,
  session: { method: 'pin', expires_at: '2026-12-31T00:00:00Z', persistent: false },
});

function Probe({ onTap }: { onTap?: (ctx: ReturnType<typeof useAuth>) => void }) {
  const ctx = useAuth();
  onTap?.(ctx);
  return null;
}

type AuthProbe = () => ReturnType<typeof useAuth>;

/** Testing Library 的 render 内部已包 act；不得再嵌套 act（React 19 会重叠）。 */
function mountAuth(): AuthProbe {
  let ctx: ReturnType<typeof useAuth> | undefined;
  render(
    <AuthProvider>
      <Probe onTap={c => { ctx = c; }} />
    </AuthProvider>,
  );
  return () => {
    if (!ctx) throw new Error('context not captured');
    return ctx;
  };
}

/** isLoading=false 表示挂载期的 me/options 已 settle。 */
async function renderAuth(): Promise<AuthProbe> {
  const getCtx = mountAuth();
  await waitFor(() => expect(getCtx().isLoading).toBe(false));
  return getCtx;
}

const flushMicrotasks = async (): Promise<void> => {
  for (let i = 0; i < 6; i++) await Promise.resolve();
};

/** act 外调用：让 Node 的 unhandledRejection（若有）与定时器队列浮出。 */
const flushAll = async (): Promise<void> => {
  await flushMicrotasks();
  await new Promise<void>(resolve => setTimeout(resolve, 0));
  await flushMicrotasks();
};

interface Deferred {
  promise: Promise<void>;
  resolve: () => void;
}
interface TwoRoundClear {
  // 生产契约 signIn/clearTaxonomyCaches 返回 Promise<boolean>：门控 promise 在
  // 两轮完成后以可观测成功 true resolve，供 signIn 的 mock 直接复用。
  promise: Promise<boolean>;
  finishFirstRound: () => void;
  finishSecondRound: () => void;
}

const pendingClearSettlers: Deferred[] = [];
const pendingClearPromises: Promise<unknown>[] = [];
const pendingAuthActions: Promise<unknown>[] = [];

function deferred(): Deferred {
  let resolve!: () => void;
  const promise = new Promise<void>(res => { resolve = () => res(); });
  const gate: Deferred = { promise, resolve };
  pendingClearSettlers.push(gate);
  return gate;
}

/** 模拟 SW 双轮屏障：两轮均完成，clearTaxonomyCaches 的 promise 才 resolve。 */
function twoRoundClear(): TwoRoundClear {
  const first = deferred();
  const second = deferred();
  const promise = (async () => {
    await first.promise;
    await second.promise;
    return true;
  })();
  pendingClearPromises.push(promise);
  return {
    promise,
    finishFirstRound: first.resolve,
    finishSecondRound: second.resolve,
  };
}

/**
 * 在单个 await act 内启动认证边界、分阶段放行两轮清理，并断言：
 * 清理未完成时身份不动，清理完成后身份才变化。绝不嵌套 act，也不在 act
 * 内等待定时器（避免 React 19 act scope 重叠导致挂起）。
 */
async function runGatedBoundary(
  start: () => Promise<unknown>,
  gate: TwoRoundClear,
  assertBefore: () => void,
  assertAfter: () => void,
): Promise<void> {
  let action!: Promise<unknown>;
  // 每个阶段都用一个已 await 的 async act 结束：React 会在 act 退出时 flush
  // 状态队列，Probe 捕获的 context 是真实渲染结果。若生产提前采纳身份，
  // 第一个/第二个 assertBefore 就会失败，而不是被 act 的批处理掩盖。
  await act(async () => {
    action = start();
    pendingAuthActions.push(action);
    await flushMicrotasks();
  });
  assertBefore();

  await act(async () => {
    gate.finishFirstRound();
    await flushMicrotasks();
  });
  assertBefore();

  await act(async () => {
    gate.finishSecondRound();
    await action;
    await flushMicrotasks();
    await flushMicrotasks();
  });
  assertAfter();
  await flushAll();
}

function stopUnhandledListeners(): void {
  for (const listener of activeUnhandledListeners.splice(0)) {
    nodeProcess?.off?.('unhandledRejection', listener);
  }
}

function watchUnhandledRejections(): UnhandledWatcher {
  if (!nodeProcess?.on || !nodeProcess.off) {
    throw new Error('当前测试环境缺少 process unhandledRejection 监听能力');
  }
  const onUnhandled = vi.fn((_reason: unknown, _promise: Promise<unknown>) => undefined);
  const listener: UnhandledRejectionListener = onUnhandled;
  nodeProcess.on('unhandledRejection', listener);
  activeUnhandledListeners.push(listener);
  return {
    onUnhandled,
    stop: () => {
      const index = activeUnhandledListeners.indexOf(listener);
      if (index >= 0) activeUnhandledListeners.splice(index, 1);
      nodeProcess.off?.('unhandledRejection', listener);
    },
  };
}

beforeEach(() => {
  stopUnhandledListeners();
  vi.resetAllMocks();
  // 每个测试重建默认实现；vi.clearAllMocks 只清计数，无法清除上一测试留下的
  // mockResolvedValue / mockImplementationOnce 队列，是旧用例计数串味的根因。
  vi.mocked(api.auth.me).mockRejectedValue(new Error('signed out'));
  vi.mocked(api.auth.options).mockResolvedValue({ pin_sign_in: true, start_page: 'picker' });
  vi.mocked(api.auth.logout).mockImplementation(async () => undefined);
  clearTaxonomyCaches.mockImplementation(async () => true);
  window.localStorage.clear();
  window.sessionStorage.clear();
  window.history.replaceState({}, '', '/');
});

afterEach(async () => {
  try {
    const actions = pendingAuthActions.splice(0);
    const clearPromises = pendingClearPromises.splice(0);
    const settlers = pendingClearSettlers.splice(0);
    // 若某个断言提前失败，同步放行所有未完成的 gate 并排空 promise，
    // 避免异步状态更新泄到下一个测试。正常路径下这里没有待决项。
    settlers.forEach(gate => gate.resolve());
    await Promise.allSettled([...actions, ...clearPromises]);
    await flushAll();
  } finally {
    cleanup();
    stopUnhandledListeners();
    vi.resetAllMocks();
    window.localStorage.clear();
    window.sessionStorage.clear();
    window.history.replaceState({}, '', '/');
  }
});

const baseUser = { avatar_url: '', theme: 'sunroom' as const, has_pin: true, auth_providers: [] as string[], created_at: '2026-10-01' };
const aliceUser: User = { ...baseUser, id: 1, name: 'Alice', role: 'admin', paused: false };
const bobUser: User = { ...baseUser, id: 2, name: 'Bob', role: 'child', paused: false };
const alice: AuthSession = meOf(aliceUser);
const bob: AuthSession = meOf(bobUser);

// 生产缺陷告警（只读回归）：以下用例若失败，生产实现可能把状态更新提前到
// clearTaxonomyCaches 的 promise settle 之前，或退回 fire-and-forget 清理。
describe('AuthContext 冻结隐私契约：等待双轮清理后再改身份', () => {
  describe('signIn：登录与同页用户切换', () => {
    it('登录先 await 双轮 clearTaxonomyCaches，完成后才采纳身份', async () => {
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).not.toHaveBeenCalled();

      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      await runGatedBoundary(
        () => getCtx().signIn(alice),
        gate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
          expect(getCtx().user).toBeNull();
          expect(getCtx().session).toBeNull();
          expect(getCtx().signedOutHere).toBe(false);
        },
        () => {
          expect(getCtx().user?.id).toBe(1);
          expect(getCtx().session?.method).toBe('pin');
          expect(getCtx().signedOutHere).toBe(false);
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
        },
      );
    });

    it('同一页面两次 signIn 不同 user id：每次都等各自双轮清理', async () => {
      const getCtx = await renderAuth();
      const firstGate = twoRoundClear();
      const secondGate = twoRoundClear();
      clearTaxonomyCaches
        .mockImplementationOnce(() => firstGate.promise)
        .mockImplementationOnce(() => secondGate.promise);

      await runGatedBoundary(
        () => getCtx().signIn(alice),
        firstGate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
          expect(getCtx().user).toBeNull();
        },
        () => expect(getCtx().user?.id).toBe(1),
      );

      await runGatedBoundary(
        () => getCtx().signIn(bob),
        secondGate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
          expect(getCtx().user?.id).toBe(1); // 第二次清理未完，旧身份仍保留
        },
        () => {
          expect(getCtx().user?.id).toBe(2);
          expect(getCtx().session?.method).toBe('pin');
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
        },
      );
    });

    it('清理 reject 不阻塞登录，且不产生 unhandled rejection', async () => {
      const watcher = watchUnhandledRejections();
      try {
        const getCtx = await renderAuth();
        clearTaxonomyCaches.mockRejectedValueOnce(new Error('cache busy'));

        await act(async () => { await getCtx().signIn(alice); });
        await flushAll();

        expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
        expect(getCtx().user?.id).toBe(1);
        expect(getCtx().session?.method).toBe('pin');
        expect(watcher.onUnhandled).not.toHaveBeenCalled();
      } finally {
        watcher.stop();
      }
    });

    it('signIn 返回 boolean：清理成功为 true，清理失败为 false 且不推进已清身份', async () => {
      // refresh 的 /auth/me 恒返回 signed out，避免第二次 refresh 读到别的身份。
      const getCtx = await renderAuth();
      // 默认实现已返回 true（见 beforeEach）。
      const signingIn = getCtx().signIn(alice);
      await act(async () => { await signingIn; });
      const firstResult = await signingIn;
      expect(firstResult).toBe(true);
      expect(getCtx().user?.id).toBe(1);
      const callsAfterFirst = clearTaxonomyCaches.mock.calls.length;
      // 本次 signIn 恰好一次清理（挂载期 me 失败路径不清）。
      expect(callsAfterFirst).toBe(1);

      // 清理失败：身份照常采纳（隐私清理不阻塞认证），但不推进
      // lastClearedUserId——后一次 refresh（id 仍不匹配）必须重试清理。
      // Once 队列优先于默认实现（只影响一次清理调用）。
      clearTaxonomyCaches.mockImplementationOnce(async () => false);
      const switching = getCtx().signIn(bob);
      await act(async () => { await switching; });
      await switching;
      expect(getCtx().user?.id).toBe(2);
      expect(clearTaxonomyCaches.mock.calls.length).toBe(callsAfterFirst + 1);

      await act(async () => { await getCtx().refresh(); });
      await flushAll();
      // 清理失败未推进已清身份：refresh 重试一次清理。
      expect(clearTaxonomyCaches.mock.calls.length).toBe(callsAfterFirst + 2);
      expect(getCtx().user).toBeNull();
    });

    it('清理失败的 signIn 在 refresh 中可重试，直到一次可观测成功的清理落地', async () => {
      vi.mocked(api.auth.me).mockRejectedValue(new Error('signed out'));
      const getCtx = await renderAuth();
      clearTaxonomyCaches.mockImplementationOnce(async () => false);
      const signingIn = getCtx().signIn(alice);
      await act(async () => { await signingIn; });
      await signingIn;
      expect(getCtx().user?.id).toBe(1);

      // 第一次 refresh：/auth/me 读回同一身份。清理在 signIn 时已失败、未推进
      // lastClearedUserId，本次 refresh 必须重试清理（默认实现成功），身份不变。
      vi.mocked(api.auth.me).mockResolvedValue(meOf(aliceUser));
      const callsAtStart = clearTaxonomyCaches.mock.calls.length;
      await act(async () => { await getCtx().refresh(); });
      await flushAll();
      expect(clearTaxonomyCaches.mock.calls.length).toBe(callsAtStart + 1);
      expect(getCtx().user?.id).toBe(1);

      // 重试清理已可观测成功：同一 user 的后续 refresh 不再重复清理。
      await act(async () => { await getCtx().refresh(); });
      await flushAll();
      expect(clearTaxonomyCaches.mock.calls.length).toBe(callsAtStart + 1);
      expect(getCtx().user?.id).toBe(1);
    });

    it('清理未完成时被更新的认证操作取代：signIn 返回 false 且不采纳身份', async () => {
      const getCtx = await renderAuth();
      const gate = twoRoundClear();
      // 门控清理：signIn 卡在清理上，期间发生一次更新代的认证操作。
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      let solved = false;
      const signInTask = getCtx().signIn(alice).then((r) => { solved = true; return r; });
      pendingAuthActions.push(signInTask);
      await flushMicrotasks();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
      expect(solved).toBe(false);

      // 更新代操作（signOut）先于 signIn 的清理完成：signIn 必须放弃采纳。
      await act(async () => {
        await getCtx().signOut();
        await flushMicrotasks();
      });
      await act(async () => {
        gate.finishFirstRound();
        gate.finishSecondRound();
        await flushMicrotasks();
        await flushMicrotasks();
      });
      expect(await signInTask).toBe(false);
      expect(getCtx().user).toBeNull();
    });
  });

  describe('refresh：首次恢复、用户切换与匿名失败', () => {
    it('首次匿名刷新失败不清公共缓存（重复匿名刷新也不清）', async () => {
      const getCtx = await renderAuth();
      expect(api.auth.me).toHaveBeenCalledTimes(1);
      expect(clearTaxonomyCaches).not.toHaveBeenCalled();

      await act(async () => { await getCtx().refresh(); });

      expect(api.auth.me).toHaveBeenCalledTimes(2);
      expect(clearTaxonomyCaches).not.toHaveBeenCalled();
      expect(getCtx().user).toBeNull();
      expect(getCtx().session).toBeNull();
    });

    it('首次 refresh null → 有效 id：必须等双轮清理完成才采纳会话', async () => {
      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = mountAuth();
      // 挂载期 refresh 已在等待 gate；isLoading 也不能提前变为 false。
      await act(async () => { await flushMicrotasks(); });
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
      expect(getCtx().isLoading).toBe(true);
      expect(getCtx().user).toBeNull();

      await act(async () => { gate.finishFirstRound(); await flushMicrotasks(); });
      expect(getCtx().isLoading).toBe(true);
      expect(getCtx().user).toBeNull();

      await act(async () => {
        gate.finishSecondRound();
        await gate.promise;
        await flushMicrotasks();
        await flushMicrotasks();
      });
      await flushAll();

      expect(getCtx().isLoading).toBe(false);
      expect(getCtx().user?.id).toBe(1);
      expect(getCtx().session?.method).toBe('pin');
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
    });

    it('冷启动恢复时清理 reject 仍采纳会话，且不产生 unhandled rejection', async () => {
      const watcher = watchUnhandledRejections();
      try {
        clearTaxonomyCaches.mockRejectedValueOnce(new Error('cache busy'));
        vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));

        const getCtx = await renderAuth();
        await flushAll();

        expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
        expect(getCtx().isLoading).toBe(false);
        expect(getCtx().user?.id).toBe(1);
        expect(getCtx().session?.method).toBe('pin');
        expect(watcher.onUnhandled).not.toHaveBeenCalled();
      } finally {
        watcher.stop();
      }
    });

    it('同一 user 重复 refresh 不重复清理', async () => {
      vi.mocked(api.auth.me).mockResolvedValue(meOf(aliceUser));
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      await act(async () => {
        await getCtx().refresh();
        await getCtx().refresh();
      });

      expect(api.auth.me).toHaveBeenCalledTimes(3);
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);
      expect(getCtx().user?.id).toBe(1);
    });

    it('refresh 发现用户切换：等双轮清理后才采纳新身份', async () => {
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(bobUser));
      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      await runGatedBoundary(
        () => getCtx().refresh(),
        gate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
          expect(getCtx().user?.id).toBe(1);
        },
        () => {
          expect(getCtx().user?.id).toBe(2);
          expect(getCtx().session?.method).toBe('pin');
        },
      );
    });

    it('身份切换时清理 reject 仍采纳新会话，且不产生 unhandled rejection', async () => {
      const watcher = watchUnhandledRejections();
      try {
        vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
        const getCtx = await renderAuth();
        vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(bobUser));
        clearTaxonomyCaches.mockRejectedValueOnce(new Error('cache busy'));

        await act(async () => { await getCtx().refresh(); });
        await flushAll();

        expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
        expect(getCtx().user?.id).toBe(2);
        expect(getCtx().session?.method).toBe('pin');
        expect(watcher.onUnhandled).not.toHaveBeenCalled();
      } finally {
        watcher.stop();
      }
    });

    it('曾持有会话的 refresh 失败：等双轮清理后才清除身份', async () => {
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      vi.mocked(api.auth.me).mockRejectedValueOnce(new Error('offline'));
      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      await runGatedBoundary(
        () => getCtx().refresh(),
        gate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
          expect(getCtx().user?.id).toBe(1);
        },
        () => {
          expect(getCtx().user).toBeNull();
          expect(getCtx().session).toBeNull();
        },
      );
    });
  });

  describe('SESSION_EXPIRED_EVENT：清理完成后才清除身份', () => {
    it('等双轮清理完成后才清除 user/session', async () => {
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      await runGatedBoundary(
        async () => {
          window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
          await gate.promise;
          await flushMicrotasks();
        },
        gate,
        () => {
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
          expect(getCtx().user?.id).toBe(1);
          expect(getCtx().session?.method).toBe('pin');
        },
        () => {
          expect(getCtx().user).toBeNull();
          expect(getCtx().session).toBeNull();
        },
      );
    });

    it('清理 reject 仍完成状态重置，且不产生 unhandled rejection', async () => {
      const watcher = watchUnhandledRejections();
      try {
        vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
        const getCtx = await renderAuth();
        clearTaxonomyCaches.mockRejectedValueOnce(new Error('cache busy'));

        await act(async () => {
          window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
          await flushMicrotasks();
        });
        await flushAll();

        expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
        expect(getCtx().user).toBeNull();
        expect(getCtx().session).toBeNull();
        expect(watcher.onUnhandled).not.toHaveBeenCalled();
      } finally {
        watcher.stop();
      }
    });
  });

  describe('signOut：清理完成后才清除身份', () => {
    it('等双轮清理完成后才清除 user/session 并置 signedOutHere', async () => {
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      const gate = twoRoundClear();
      clearTaxonomyCaches.mockImplementationOnce(() => gate.promise);
      await runGatedBoundary(
        () => getCtx().signOut(),
        gate,
        () => {
          expect(api.auth.logout).toHaveBeenCalledTimes(1);
          expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
          expect(getCtx().user?.id).toBe(1);
          expect(getCtx().signedOutHere).toBe(false);
        },
        () => {
          expect(getCtx().user).toBeNull();
          expect(getCtx().session).toBeNull();
          expect(getCtx().signedOutHere).toBe(true);
        },
      );
    });

    it('signOut 权威窗口：logout 未落地时 refresh 读到有效身份也不写回，登出后 user 为 null', async () => {
      // DEF-PWA-01：窗口内 me() 读回的是旧 cookie 身份（logout 尚未落地时
      // 服务端 cookie 仍有效）。refresh 不得写状态、不得清缓存；signOut 落地
      // 后 user/session 必须清零，window 关闭后 refresh 才恢复正常。
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      const userAtMount = getCtx().user;
      expect(userAtMount?.id).toBe(1);

      let releaseLogout!: () => void;
      const logoutGate = new Promise<void>(resolve => { releaseLogout = () => resolve(); });
      vi.mocked(api.auth.logout).mockImplementationOnce(() => logoutGate);
      // logout 挂起期间 /auth/me 返回的有效身份（会被"复活"的那一份）。
      vi.mocked(api.auth.me).mockResolvedValue(meOf(aliceUser));

      let signOutPromise!: Promise<void>;
      await act(async () => {
        signOutPromise = getCtx().signOut();
        pendingAuthActions.push(signOutPromise);
        await flushMicrotasks();
      });
      expect(api.auth.logout).toHaveBeenCalledTimes(1);

      // 窗口内 refresh：让位，不调用 me()、不清缓存、不推进已清身份。
      await act(async () => { await getCtx().refresh(); });
      expect(api.auth.me).toHaveBeenCalledTimes(1);
      expect(clearTaxonomyCaches).toHaveBeenCalledTimes(1);

      // 释放 logout：signOut 清理并清零状态。
      await act(async () => {
        releaseLogout();
        await signOutPromise;
        await flushMicrotasks();
      });
      expect(getCtx().user).toBeNull();
      expect(getCtx().session).toBeNull();
      expect(getCtx().signedOutHere).toBe(true);

      // 窗口已关闭：此时 me() 若仍返回有效身份（模拟 cookie 被另一端恢复），
      // refresh 属于 signOut 之后的新转换，按生产语义会重新采纳——这不是
      // 窗口泄漏；本条只断言窗口内零写入、signOut 落地后清零。
      // 反向对照：若此刻窗口仍开着，me() 不会被调用。
      await act(async () => { await getCtx().refresh(); });
      expect(api.auth.me).toHaveBeenCalledTimes(2);
    });

    it('signOut 权威窗口：窗口内迟到的 refresh 成功不会把 user 写回', async () => {
      // 更强版本：refresh 在窗口打开前已启动（me() 在途），signOut 启动后
      // 才拿到有效身份——代数机制必须挡住这次迟到写入，而不是靠入口检查。
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();
      const userAtMount = getCtx().user;
      expect(userAtMount?.id).toBe(1);

      let resolveMe!: (v: AuthSession) => void;
      vi.mocked(api.auth.me).mockImplementationOnce(
        () => new Promise<AuthSession>(resolve => { resolveMe = resolve; }),
      );
      let refreshPromise!: Promise<void>;
      await act(async () => {
        refreshPromise = getCtx().refresh();
        pendingAuthActions.push(refreshPromise);
        await flushMicrotasks();
      });

      // 在 refresh 在途时登出（logout 立即完成，清理也立即完成）。
      await act(async () => {
        await getCtx().signOut();
        await flushMicrotasks();
      });
      expect(getCtx().user).toBeNull();
      expect(api.auth.logout).toHaveBeenCalledTimes(1);

      // 迟到的 me() 现在返回有效身份，再释放 refresh：不得写回 user。
      await act(async () => {
        resolveMe(meOf(aliceUser));
        await refreshPromise;
        await flushMicrotasks();
      });
      expect(getCtx().user).toBeNull();
      expect(getCtx().session).toBeNull();
      expect(getCtx().signedOutHere).toBe(true);
      await flushAll();
    });

    it('signOut 权威窗口：signOut 进行期间 SESSION_EXPIRED 不会复活身份', async () => {
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();

      let releaseLogout!: () => void;
      const logoutGate = new Promise<void>(resolve => { releaseLogout = () => resolve(); });
      vi.mocked(api.auth.logout).mockImplementationOnce(() => logoutGate);
      vi.mocked(api.auth.me).mockResolvedValue(meOf(aliceUser));

      let signOutPromise!: Promise<void>;
      await act(async () => {
        signOutPromise = getCtx().signOut();
        pendingAuthActions.push(signOutPromise);
        await flushMicrotasks();
      });

      // SESSION_EXPIRED 与 signOut 并发：两者都指向"已登出"，不得出现有效身份。
      await act(async () => {
        window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
        await flushMicrotasks();
      });
      expect(api.auth.me).toHaveBeenCalledTimes(1);

      await act(async () => {
        releaseLogout();
        await signOutPromise;
        await flushMicrotasks();
      });
      expect(getCtx().user).toBeNull();
      expect(getCtx().session).toBeNull();
      // 记录行为：signOut 落地时的 `signedOutHere = true` 写入因并发
      // SESSION_EXPIRED 已推進 authGeneration 而被 supersede 检查挡住，故为
      // false。对本条场景（两者终态都是"已登出"）路由语义等价（user 为 null
      // 已足以离开登录页），本卡不据此判缺陷；如产品要求严格 true，交主会话
      // 裁决（AuthContext.tsx signOut 的代数判定）。
      expect(getCtx().signedOutHere).toBe(false);
      await flushAll();
    });

    it('signOut 权威窗口被新 signIn 取代时关闭：后续 refresh 不再被挡', async () => {
      // finally 关窗契约：被 supersede 的 signOut 不得留下永久窗口。
      vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
      const getCtx = await renderAuth();

      let releaseLogout!: () => void;
      const logoutGate = new Promise<void>(resolve => { releaseLogout = () => resolve(); });
      vi.mocked(api.auth.logout).mockImplementationOnce(() => logoutGate);

      let signOutPromise!: Promise<void>;
      await act(async () => {
        signOutPromise = getCtx().signOut();
        pendingAuthActions.push(signOutPromise);
        await flushMicrotasks();
      });

      await act(async () => {
        expect(await getCtx().signIn(bob)).toBe(true);
        await flushMicrotasks();
      });
      expect(getCtx().user?.id).toBe(2);

      await act(async () => {
        releaseLogout();
        await signOutPromise;
        await flushMicrotasks();
      });
      // 被取代的 signOut 不得清掉新身份。
      expect(getCtx().user?.id).toBe(2);

      // 窗口已关闭：refresh 必须恢复生效（入口检查命中窗口时会直接 return，
      // me() 不会被调用）。me() 保持挂载时的默认 reject（已登出），故身份
      // 保持 null——此处只验证窗口未泄漏，不验证身份采纳。
      const meCalls = vi.mocked(api.auth.me).mock.calls.length;
      await act(async () => { await getCtx().refresh(); });
      expect(vi.mocked(api.auth.me).mock.calls.length).toBe(meCalls + 1);
      expect(getCtx().user).toBeNull();
      await flushAll();
    });

    it('清理 reject 不阻塞 signOut，且不产生 unhandled rejection', async () => {
      const watcher = watchUnhandledRejections();
      try {
        vi.mocked(api.auth.me).mockResolvedValueOnce(meOf(aliceUser));
        const getCtx = await renderAuth();
        clearTaxonomyCaches.mockRejectedValueOnce(new Error('cache busy'));

        await act(async () => { await getCtx().signOut(); });
        await flushAll();

        expect(api.auth.logout).toHaveBeenCalledTimes(1);
        expect(clearTaxonomyCaches).toHaveBeenCalledTimes(2);
        expect(getCtx().user).toBeNull();
        expect(getCtx().session).toBeNull();
        expect(getCtx().signedOutHere).toBe(true);
        expect(watcher.onUnhandled).not.toHaveBeenCalled();
      } finally {
        watcher.stop();
      }
    });
  });
});
