// @vitest-environment jsdom
// useSubjects hook 单测（卡 TX-SUBJECTS-HOOK-FIX1-TESTER），冻结行为与用例一一对应：
//  1. 60 秒 TTL：成功后 60 秒内的新消费者零请求、不闪 loading，周期性地表快过期了才重拉；
//  2. TTL 过期重新请求（vi fake timers 拨表，不真实等待），过期后的请求失败也不会误改成功快照；
//  3. retry 强制绕过新鲜缓存：已有成功（含空数组）后 retry 仍发请求；
//  4. 缓存命中清旧 error：先失败 → 别人成功 → 后挂载者 retry，不残留 error 且拿到新数据；
//  5. 失败不缓存新结果：失败后旧快照保留为 stale 数据，重试拿到新结果才覆盖；
//  6. subjects null / 缺键归一为 []，且空数组成功同样进入缓存；
//  7. unmount 后 resolve/失败都不更新（卸载告警 + setState 全部归零）；
//  8. 并发消费者只发一次请求（inflight 去重），retry 撞上在途请求同样复用。
//
// 测试隔离：
//  - 模块级 cached/cachedAt/inflight 是跨用例共享状态，每个用例用 vi.resetModules()
//    + 动态 import 拿全新模块副本；不使用静态 import 的 hook 类型（那会触发真实模块求值）；
//  - 模块 mock 用 vi.mock 工厂返回普通对象（subjects 转发到逐用例登记的
//    subjectsImpl），调用次数由本文件 subjectsCalls 记录；beforeEach 清空
//    记录与实现，不依赖 vi.fn 的 mockReset/ResetAllMocks；
//  - retry 按钮是组件内真实 <button>，用 fireEvent.click + act 触发，不往 document.body 塞裸节点；
//  - 全部 deferred 由用例手动 settle（finishPending），不回应用例也不会遗留永不 resolve 的 promise；
//  - 每个用例 afterEach 恢复 timers（vi.useRealTimers）、还原 mock 模块（vi.doUnmock）
//    与 spy，并清掉 DOM。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import '../taxonomy/testUtils'; // data-test 作为 testId 查询属性（仓库约定）
import type { SubjectRow } from '../taxonomy/types';

const SUBJECTS_CLIENT = '../taxonomy/client';

// 注意：不用 vi.fn()。hook 模块在 vi.resetModules() 下是全新实例，它 import 到的
// taxonomyApi 必须是测试里同一个对象，因此工厂直接返回一个普通对象；调用记录
// 由本文件的 subjectsCalls 数组维护（不用 vi.resetAllMocks，也就不存在
// 「reset 之后 mock 变残、hook 拿到 undefined」串味问题）。
const { taxonomyApi, subjectsCalls } = vi.hoisted(() => {
  const calls: unknown[][] = [];
  return {
    subjectsCalls: calls,
    taxonomyApi: {
      subjects: () => {
        calls.push([]);
        if (!subjectsImpl) throw new Error('taxonomyApi.subjects 被调用前未登记 subjectsImpl');
        return subjectsImpl();
      },
    },
  };
});
vi.mock('../taxonomy/client', () => ({ taxonomyApi }));

/** 模块形状：只描述本文件用到的导出，避免静态 import 真实 hook 模块。 */
interface UseSubjectsModule {
  useSubjects: () => {
    subjects: SubjectRow[];
    loading: boolean;
    error: unknown;
    retry: () => void;
  };
}
type UseSubjectsHook = UseSubjectsModule['useSubjects'];

type SubjectsResponse = { subjects: SubjectRow[] | null };

/** deferred + settled 标记：用例结尾只处理未 settle 的那个，避免泄漏。 */
interface Deferred {
  promise: Promise<SubjectsResponse>;
  resolve: (v: SubjectsResponse) => void;
  reject: (e: unknown) => void;
  settled: boolean;
}

function deferred(): Deferred {
  const d: Deferred = {
    promise: Promise.resolve({ subjects: [] }),
    resolve: () => {},
    reject: () => {},
    settled: false,
  };
  d.promise = new Promise<SubjectsResponse>((res, rej) => {
    d.resolve = (v) => {
      d.settled = true;
      res(v);
    };
    d.reject = (e) => {
      d.settled = true;
      rej(e);
    };
  });
  return d;
}

const pending: Deferred[] = [];

let subjectsImpl: (() => Promise<SubjectsResponse>) | null = null;

/**
 * 登记 subjects 的响应方式（每次调用都会新建 deferred 并登记到 pending），
 * 返回 pending 数组本身：调用发生后取 pending[i] 即可拿到对应 deferred。
 */
function deferSubjects(impl?: () => Promise<SubjectsResponse>): Deferred[] {
  subjectsImpl = () => {
    const d = deferred();
    pending.push(d);
    return d.promise;
  };
  if (impl) subjectsImpl = impl;
  return pending;
}

/** 断言请求次数（基于本文件维护的调用记录，不依赖 vi.fn 状态）。 */
const subjectsRequestCount = (): number => subjectsCalls.length;

/**
 * 收尾：把仍未 settle 的 deferred 全部结算（默认按失败结算，保证没有悬挂 promise），
 * 再 flush 一轮微任务。只作用于本用例登记过的 pending。
 */
async function finishPending(): Promise<void> {
  const open = pending.splice(0).filter((d) => !d.settled);
  await act(async () => {
    for (const d of open) d.reject(new Error('test teardown'));
    await Promise.resolve();
    await Promise.resolve();
  });
}

/** 拨表：fake timers 下 Date.now 与 setTimeout 同步推进。 */
async function advance(ms: number): Promise<void> {
  await act(async () => {
    vi.advanceTimersByTime(ms);
    await Promise.resolve();
  });
}

const subjectRow = (over: Partial<SubjectRow> = {}): SubjectRow => ({
  id: 'math',
  name: '数学',
  sb_code: 'MATH',
  topic_count: 3108,
  question_count: 9000,
  grades: [1, 2, 3],
  ...over,
});

/**
 * 渲染一个消费 useSubjects 的小组件：真实 <button data-test="retry"> 触发 retry，
 * 列表项 data-test=subject-<id>，loading/error 各有标记。seen 记录每次渲染快照。
 */
function renderConsumer(hook: UseSubjectsHook) {
  const seen: Array<ReturnType<UseSubjectsHook>> = [];
  const Consumer: React.FC<{ label?: string }> = ({ label = 'A' }) => {
    const result = hook();
    seen.push(result);
    return (
      <div data-test={`consumer-${label}`}>
        <button data-test={`retry-${label}`} onClick={result.retry}>
          retry
        </button>
        {result.loading && <span data-test={`loading-${label}`}>loading</span>}
        {result.error != null && <span data-test={`error-${label}`}>error</span>}
        <ul>
          {result.subjects.map((s) => (
            <li key={s.id} data-test={`subject-${s.id}`}>
              {s.name}
            </li>
          ))}
        </ul>
      </div>
    );
  };
  const utils = render(<Consumer />);
  return {
    ...utils,
    seen,
    latest: () => seen[seen.length - 1]!,
    clickRetry: () => act(() => {
      fireEvent.click(screen.getByTestId('retry-A'));
    }),
  };
}

/** 每个用例拿全新模块副本：清掉模块级 cached/cachedAt/inflight。 */
async function freshUseSubjects(): Promise<UseSubjectsModule> {
  vi.resetModules();
  vi.doMock(SUBJECTS_CLIENT, () => ({ taxonomyApi }));
  return (await import('./useSubjects')) as unknown as UseSubjectsModule;
}

/** 观测「错误成功」：React 在卸载后 setState 会 console.error，unmount 用例断言为零。 */
function spyConsoleError() {
  return vi.spyOn(console, 'error').mockImplementation(() => {});
}

beforeEach(() => {
  // 调用记录与响应工厂逐用例清空（没有 vi.fn，不需要 resetAllMocks）。
  subjectsCalls.length = 0;
  subjectsImpl = null;
  vi.useFakeTimers();
});

afterEach(async () => {
  await finishPending();
  cleanup();
  vi.useRealTimers();
  vi.doUnmock(SUBJECTS_CLIENT);
  vi.restoreAllMocks();
  pending.length = 0;
  subjectsImpl = null;
});

describe('useSubjects 缓存生命周期（60 秒 TTL）', () => {
  it('成功后 60 秒内新消费者命中新鲜缓存：零请求、不闪 loading、展示缓存数据', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const first = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(1);
    expect(screen.getByTestId('loading-A')).toBeTruthy();

    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(screen.getByTestId('subject-math')).toBeTruthy();
    expect(first.latest().loading).toBe(false);

    // 距缓存写入 59_999ms：仍在新鲜期内。
    await advance(59_999);
    cleanup();
    const second = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(1);
    expect(screen.queryByTestId('loading-A')).toBeNull();
    expect(screen.getByTestId('subject-math')).toBeTruthy();
    expect(second.latest().subjects.map((s) => s.id)).toEqual(['math']);
    expect(second.latest().error).toBeNull();
  });

  it('TTL 过期后新消费者重新请求：loading 出现，新结果替换旧快照', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });

    // 恰好 60_000ms：不再新鲜（严格小于 TTL 才算新鲜）。
    await advance(60_000);
    cleanup();
    const second = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(2);
    expect(screen.getByTestId('loading-A')).toBeTruthy();
    // 过期期间旧快照仍可作首帧数据，不闪空态。
    expect(screen.getByTestId('subject-math')).toBeTruthy();

    await act(async () => {
      pending[1]!.resolve({
        subjects: [subjectRow({ id: 'math', name: '数学' }), subjectRow({ id: 'history', name: '历史' })],
      });
    });
    expect(screen.getByTestId('subject-history')).toBeTruthy();
    expect(second.latest().loading).toBe(false);
    expect(second.latest().subjects.map((s) => s.id)).toEqual(['math', 'history']);
  });

  it('TTL 过期后的请求失败：不刷新缓存时间戳，下一次挂载仍会重拉', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });

    await advance(60_000);
    cleanup();
    const second = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(2);
    await act(async () => {
      pending[1]!.reject(new Error('expired-refetch-failed'));
    });
    expect(screen.getByTestId('error-A')).toBeTruthy();
    // 失败不写缓存：旧快照保留作 stale 数据。
    expect(second.latest().subjects.map((s) => s.id)).toEqual(['math']);

    // 只等了 1ms：若失败被误当成「缓存刷新」并写入时间戳，这里就会命中新鲜缓存而零请求。
    await advance(1);
    cleanup();
    renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(3);
  });
});

describe('useSubjects retry', () => {
  it('已有成功结果（新鲜期内）后 retry 仍强制发请求', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(subjectsRequestCount()).toBe(1);

    await view.clickRetry();
    expect(subjectsRequestCount()).toBe(2);
    expect(screen.getByTestId('loading-A')).toBeTruthy();

    await act(async () => {
      pending[1]!.resolve({
        subjects: [subjectRow({ id: 'math', name: '数学' }), subjectRow({ id: 'chinese', name: '语文' })],
      });
    });
    expect(screen.getByTestId('subject-chinese')).toBeTruthy();
    expect(view.latest().subjects.map((s) => s.id)).toEqual(['math', 'chinese']);
  });

  it('成功结果为空数组（已缓存）后 retry 仍强制发请求', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: [] });
    });
    expect(view.latest().subjects).toEqual([]);
    expect(view.latest().loading).toBe(false);

    await view.clickRetry();
    expect(subjectsRequestCount()).toBe(2);

    await act(async () => {
      pending[1]!.resolve({ subjects: [subjectRow({ id: 'history', name: '历史' })] });
    });
    expect(screen.getByTestId('subject-history')).toBeTruthy();
    expect(view.latest().subjects.map((s) => s.id)).toEqual(['history']);
  });

  it('先失败 → 其他消费者成功 → 后挂载者 retry：不残留 error，拿到新数据', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();

    // 第一个消费者失败。
    const first = renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.reject(new Error('boom'));
    });
    expect(first.latest().error).toBeInstanceOf(Error);
    expect(first.latest().subjects).toEqual([]);

    // 第二个消费者成功（失败未缓存，会真实重发）。
    cleanup();
    deferSubjects();
    const second = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(2);
    await act(async () => {
      pending[1]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(second.latest().error).toBeNull();

    // 第三个消费者命中新鲜缓存，再 retry：error 保持清空，数据被新响应替换。
    cleanup();
    const third = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(2);
    await third.clickRetry();
    expect(subjectsRequestCount()).toBe(3);
    expect(screen.queryByTestId('error-A')).toBeNull();
    await act(async () => {
      pending[2]!.resolve({ subjects: [subjectRow({ id: 'english', name: '英语' })] });
    });
    expect(screen.queryByTestId('error-A')).toBeNull();
    expect(screen.getByTestId('subject-english')).toBeTruthy();
    expect(third.latest().error).toBeNull();
    expect(third.latest().subjects.map((s) => s.id)).toEqual(['english']);
  });

  it('失败后 retry 拿到新结果才覆盖旧快照；失败本身不缓存', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });

    // retry 失败：旧快照保留，error 可见。
    await view.clickRetry();
    await act(async () => {
      pending[1]!.reject(new Error('retry-failed'));
    });
    expect(view.latest().error).toBeInstanceOf(Error);
    expect(view.latest().subjects.map((s) => s.id)).toEqual(['math']);

    // 再 retry 成功：error 清空，数据替换。
    await view.clickRetry();
    expect(subjectsRequestCount()).toBe(3);
    await act(async () => {
      pending[2]!.resolve({ subjects: [subjectRow({ id: 'history', name: '历史' })] });
    });
    expect(view.latest().error).toBeNull();
    expect(view.latest().subjects.map((s) => s.id)).toEqual(['history']);
  });
});

describe('useSubjects 响应归一', () => {
  it('subjects 为 null 时归一为 []，并以空数组作为成功结果缓存', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    await act(async () => {
      pending[0]!.resolve({ subjects: null });
    });
    expect(view.latest().subjects).toEqual([]);
    expect(view.latest().error).toBeNull();
    expect(view.latest().loading).toBe(false);

    cleanup();
    const second = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(1);
    expect(second.latest().subjects).toEqual([]);
    expect(screen.queryByTestId('loading-A')).toBeNull();
  });

  it('缺 subjects 键时归一为 []', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    const d = pending[0]!;
    // fake timers 下不用 waitFor：直接结算 deferred 并 flush 微任务。
    await act(async () => {
      d.resolve({} as SubjectsResponse);
    });
    expect(view.latest().loading).toBe(false);
    expect(view.latest().subjects).toEqual([]);
    expect(view.latest().error).toBeNull();
    // 空数组同样进入缓存：后挂载者零请求。
    cleanup();
    renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(1);
  });

  it('服务端只返 math/history：subjects 就是这两项，不含硬编码十学科', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    const d = pending[0]!;

    await act(async () => {
      d.resolve({
        subjects: [subjectRow({ id: 'math', name: '数学' }), subjectRow({ id: 'history', name: '历史' })],
      });
    });
    expect(screen.queryByTestId('subject-history')).toBeTruthy();
    expect(screen.queryByTestId('subject-math')).toBeTruthy();
    for (const key of ['chinese', 'english', 'physics', 'chemistry', 'biology', 'science', 'morality', 'geography']) {
      expect(screen.queryByTestId(`subject-${key}`)).toBeNull();
    }
    expect(subjectsRequestCount()).toBe(1);
    expect(view.latest().subjects.map((s) => s.id)).toEqual(['math', 'history']);
    expect(view.latest().error).toBeNull();
  });
});

describe('useSubjects 卸载与并发', () => {
  it('unmount 后成功响应不更新状态、不告警', async () => {
    const errorSpy = spyConsoleError();
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    const d = pending[0]!;
    expect(subjectsRequestCount()).toBe(1);
    view.unmount();

    await act(async () => {
      d.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(screen.queryByTestId('subject-math')).toBeNull();
    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('unmount 后失败响应不更新状态、不告警', async () => {
    const errorSpy = spyConsoleError();
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    const d = pending[0]!;
    expect(subjectsRequestCount()).toBe(1);
    view.unmount();

    await act(async () => {
      d.reject(new Error('late-failure'));
    });
    expect(screen.queryByTestId('error-A')).toBeNull();
    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('两个组件并发消费只发一次请求（inflight 去重）', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const seenA: Array<ReturnType<UseSubjectsHook>> = [];
    const seenB: Array<ReturnType<UseSubjectsHook>> = [];
    const ConsumerA: React.FC = () => {
      seenA.push(mod.useSubjects());
      return null;
    };
    const ConsumerB: React.FC = () => {
      seenB.push(mod.useSubjects());
      return null;
    };
    render(
      <>
        <ConsumerA />
        <ConsumerB />
      </>,
    );

    // 两个消费者都已挂载、请求仍未 resolve：只发一次。
    expect(subjectsRequestCount()).toBe(1);
    const inflight = pending[0]!;
    await act(async () => {
      inflight.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(seenA[seenA.length - 1]!.subjects.map((s) => s.id)).toEqual(['math']);
    expect(seenB[seenB.length - 1]!.subjects.map((s) => s.id)).toEqual(['math']);
    expect(subjectsRequestCount()).toBe(1);
  });

  it('retry 撞上在途请求时复用 inflight，不额外发请求', async () => {
    deferSubjects();
    const mod = await freshUseSubjects();
    const view = renderConsumer(mod.useSubjects);
    expect(subjectsRequestCount()).toBe(1);

    await view.clickRetry();
    expect(subjectsRequestCount()).toBe(1);

    await act(async () => {
      pending[0]!.resolve({ subjects: [subjectRow({ id: 'math', name: '数学' })] });
    });
    expect(screen.getByTestId('subject-math')).toBeTruthy();
    expect(view.latest().loading).toBe(false);
    // 请求结束后没有遗留的「补发」：retry 产生的在途复用已随之结算。
    expect(subjectsRequestCount()).toBe(1);
  });
});
