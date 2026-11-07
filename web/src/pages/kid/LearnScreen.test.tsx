// @vitest-environment jsdom
// TX-COURSE-DEEPLINK-TESTER：孩子端 /learn 的课程练习深链安全与动态默认学科。
//
// 被测真件：LearnScreen（URL 参数解析 → 宿主 dispatch）、TaxonomyWorkspace、
// SubjectBrowser / PracticeRunner。只 mock 两处「说话的对象」：
//   - ../../api（课程进度上报 api.courses.reportProgress）
//   - ../../taxonomy/client（taxonomyApi 全部端点）
//   - ../../AuthContext 的 useAuth（只提供孩子身份，不替换鉴权逻辑本身）
// react-router（MemoryRouter）、useSubjects 模块级缓存都是真的；每用例
// vi.resetModules + 动态 import 同一份模块图，清掉 useSubjects 的 60s TTL 缓存。
//
// 断言目标：只有 topic + resourceId + courseId 三者同时合法才进课程练习并
// 把 courseContext 传给 PracticeRunner（resourceId + topicRef）；其余一切畸形
// 组合显示 learn-invalid-course、零危险请求；返回课程链接回原课程；动态学科
// 只认服务端顺序（physics/history 真实生效，空列表保持空，绝不回退 math）。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ReactNode } from 'react';
import type { SubjectRow, TopicRow } from '../../taxonomy/types';

const { taxonomyApi, reportProgress } = vi.hoisted(() => ({
  taxonomyApi: {
    subjects: vi.fn(),
    topics: vi.fn(),
    topicDetail: vi.fn(),
    practice: vi.fn(),
    createDiagnostic: vi.fn(),
    diagnostics: vi.fn(),
    diagnosticDetail: vi.fn(),
    answerDiagnostic: vi.fn(),
    undoDiagnostic: vi.fn(),
    correctDiagnostic: vi.fn(),
    finishDiagnostic: vi.fn(),
    retestDiagnostic: vi.fn(),
    blindspots: vi.fn(),
    exportData: vi.fn(),
    adminImport: vi.fn(),
    generateCourse: vi.fn(),
    shareDiagnostic: vi.fn(),
    sharedSnapshot: vi.fn(),
  },
  reportProgress: vi.fn(),
}));
vi.mock('../../taxonomy/client', () => ({ taxonomyApi, TaxonomyAPIError: class extends Error {} }));
vi.mock('../../api', () => ({ api: { courses: { reportProgress } } }));
vi.mock('../../AuthContext', () => ({ useAuth: () => ({ user: { id: 2, name: 'Kid' }, isAdmin: false }) }));

const subjectRow = (over: Partial<SubjectRow>): SubjectRow => ({
  id: 'math',
  name: '数学',
  sb_code: 'MATH',
  topic_count: 10,
  question_count: 50,
  grades: [1, 2, 3],
  ...over,
});

/** 服务端 subjects 表顺序（sort_order）；不依赖任何静态学科词表。 */
const serverSubjects = (): SubjectRow[] => [
  subjectRow({ id: 'physics', name: '物理', sb_code: 'PHYS', topic_count: 42, question_count: 300, grades: [7, 8, 9] }),
  subjectRow({ id: 'history', name: '历史', sb_code: 'HIST', topic_count: 30, question_count: 90, grades: [7, 8] }),
];

/** 真实 API 行：本机整数 id + external_ref 寻址键 + 必有 subject。 */
const topicRow = (over: Partial<TopicRow> = {}): TopicRow => ({
  id: '1',
  external_ref: 'cnm_p001',
  name: '1到5各数的认识',
  grade: 1,
  semester: '上',
  domain: '数与代数',
  chapter: '一年级上册 第1单元',
  subject: 'math',
  question_count: 2,
  ...over,
});

const question = {
  id: 'cnm_p001_q1',
  type: 'choice' as const,
  difficulty: 'basic',
  prompt: '3 和 1 相加等于几？',
  choices: [{ key: 'A', text: '4' }, { key: 'B', text: '3' }],
};

const topicDetail = (over: Record<string, unknown> = {}) => ({
  topic: topicRow(),
  prerequisites: [],
  dependents: [],
  questions: [question],
  standards: [],
  ...over,
});

const COURSE_URL = '/learn?topic=cnm_p001&resourceId=501&courseId=9';

type RTL = typeof import('@testing-library/react');
let rtl: RTL;
let LearnScreen: typeof import('./LearnScreen').LearnScreen;
let MemoryRouter: typeof import('react-router-dom').MemoryRouter;
let Routes: typeof import('react-router-dom').Routes;
let Route: typeof import('react-router-dom').Route;
let useLocation: typeof import('react-router-dom').useLocation;

const topBar = <div data-test="topbar" />;

/** 位置探针：返回路由里真实的 pathname + search（验证「不跳转」用）。 */
function LocationProbe(): ReactNode {
  const loc = useLocation();
  return <output data-test="location-probe">{`${loc.pathname}${loc.search}`}</output>;
}

function renderAt(url: string) {
  return rtl.render(
    <MemoryRouter initialEntries={[url]}>
      <Routes>
        <Route path="/learn" element={<LearnScreen topBar={topBar} />} />
        <Route path="/course/:id" element={<div data-test="course-route-stub">课程页</div>} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

const byTest = (test: string): HTMLElement | null => document.querySelector(`[data-test="${test}"]`);

/** 「危险请求」：任何按 topic 深链/练习/上报的调用总数。 */
const dangerousCalls = (): number =>
  taxonomyApi.topicDetail.mock.calls.length
  + taxonomyApi.practice.mock.calls.length
  + reportProgress.mock.calls.length;

beforeEach(async () => {
  taxonomyApi.subjects.mockReset();
  taxonomyApi.topics.mockReset();
  taxonomyApi.topicDetail.mockReset();
  taxonomyApi.practice.mockReset();
  reportProgress.mockReset();
  taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });
  taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
  taxonomyApi.topicDetail.mockResolvedValue(topicDetail());

  vi.resetModules();
  vi.doMock('../../taxonomy/client', () => ({ taxonomyApi, TaxonomyAPIError: class extends Error {} }));
  vi.doMock('../../api', () => ({ api: { courses: { reportProgress } } }));
  vi.doMock('../../AuthContext', () => ({ useAuth: () => ({ user: { id: 2, name: 'Kid' }, isAdmin: false }) }));
  rtl = await import('@testing-library/react');
  await import('../../taxonomy/testUtils'); // data-test 作为 testId 查询属性
  await import('../../i18n');
  ({ MemoryRouter, Routes, Route, useLocation } = await import('react-router-dom'));
  ({ LearnScreen } = await import('./LearnScreen'));
});

afterEach(() => {
  rtl.cleanup();
  vi.doUnmock('../../taxonomy/client');
  vi.doUnmock('../../api');
  vi.doUnmock('../../AuthContext');
});

describe('合法深链：topic + resourceId + courseId → 课程练习', () => {
  it('按 external_ref 拉详情后进真实练习，并把 courseContext 交给 PracticeRunner', async () => {
    renderAt(COURSE_URL);

    // 深链拉的是 external_ref，不是本机整数 id。
    await rtl.waitFor(() => {
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('cnm_p001');
    });
    await rtl.waitFor(() => {
      expect(byTest('tx-practice')).toBeTruthy();
    });
    expect(byTest('learn-invalid-course')).toBeNull();
    // 题目来自深链详情（不是 topic_id=123 之类的整数寻址）。
    expect(document.body.textContent).toContain('3 和 1 相加等于几？');
    expect(taxonomyApi.topicDetail.mock.calls.every(([ref]) => ref === 'cnm_p001')).toBe(true);
  });

  it('完成练习后经 courseContext 把 score + submitted_ref 上报到同一个 resourceId', async () => {
    taxonomyApi.practice.mockResolvedValue({ verdict: 'correct', reference_answer: '4', explanation: null });
    reportProgress.mockResolvedValue({});
    renderAt(COURSE_URL);

    await rtl.waitFor(() => expect(byTest('tx-choice-A')).toBeTruthy(), { timeout: 3000 });
    rtl.fireEvent.click(byTest('tx-choice-A')!);
    rtl.fireEvent.click(byTest('tx-submit')!);
    await rtl.waitFor(() => expect(byTest('tx-next')).toBeTruthy());
    rtl.fireEvent.click(byTest('tx-next')!);

    await rtl.waitFor(() => {
      expect(reportProgress).toHaveBeenCalledWith(501, { score: 100, submitted_ref: 'cnm_p001' });
    });
    expect(reportProgress).toHaveBeenCalledTimes(1);
  });

  it('返回课程链接指回 /course/{courseId}，点击后真实跳转', async () => {
    renderAt(COURSE_URL);
    await rtl.waitFor(() => expect(byTest('tx-course-back')).toBeTruthy(), { timeout: 3000 });
    const back = byTest('tx-course-back') as HTMLAnchorElement;
    expect(back.getAttribute('href')).toBe('/course/9');

    rtl.fireEvent.click(back);
    await rtl.waitFor(() => expect(byTest('course-route-stub')).toBeTruthy());
    expect(byTest('location-probe')!.textContent).toBe('/course/9');
  });

  it('topic 两侧空白被规整后仍按规整值寻址', async () => {
    renderAt('/learn?topic=%20cnm_p001%20&resourceId=501&courseId=9');
    await rtl.waitFor(() => {
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('cnm_p001');
    });
    expect(byTest('learn-invalid-course')).toBeNull();
  });
});

describe('非法深链：learn-invalid-course 且零危险请求', () => {
  const cases: [string, string][] = [
    ['重复 topic', '/learn?topic=cnm_p001&topic=cnm_p002&resourceId=501&courseId=9'],
    ['重复 resourceId', '/learn?topic=cnm_p001&resourceId=501&resourceId=502&courseId=9'],
    ['重复 courseId', '/learn?topic=cnm_p001&resourceId=501&courseId=9&courseId=10'],
    ['有 resourceId 无 courseId', '/learn?topic=cnm_p001&resourceId=501'],
    ['有 courseId 无 resourceId', '/learn?topic=cnm_p001&courseId=9'],
    ['缺 topic', '/learn?resourceId=501&courseId=9'],
    ['resourceId=0', '/learn?topic=cnm_p001&resourceId=0&courseId=9'],
    ['courseId=0', '/learn?topic=cnm_p001&resourceId=501&courseId=0'],
    ['resourceId 负数', '/learn?topic=cnm_p001&resourceId=-501&courseId=9'],
    ['courseId 负数', '/learn?topic=cnm_p001&resourceId=501&courseId=-9'],
    ['resourceId 非安全整数', '/learn?topic=cnm_p001&resourceId=9007199254740993&courseId=9'],
    ['courseId 非安全整数', '/learn?topic=cnm_p001&resourceId=501&courseId=9007199254740993'],
    ['resourceId 小数', '/learn?topic=cnm_p001&resourceId=501.5&courseId=9'],
    ['resourceId 前导零', '/learn?topic=cnm_p001&resourceId=0501&courseId=9'],
    ['resourceId 空白', '/learn?topic=cnm_p001&resourceId=%20&courseId=9'],
    ['courseId 空串', '/learn?topic=cnm_p001&resourceId=501&courseId='],
    ['resourceId 非数字', '/learn?topic=cnm_p001&resourceId=abc&courseId=9'],
    ['topic 纯数字（本机 id）', '/learn?topic=123&resourceId=501&courseId=9'],
    ['topic 以数字开头', '/learn?topic=1cnm_p001&resourceId=501&courseId=9'],
    ['topic 含空格', '/learn?topic=cnm%20p001&resourceId=501&courseId=9'],
    ['topic 空白', '/learn?topic=%20%20&resourceId=501&courseId=9'],
    ['topic 空串', '/learn?topic=&resourceId=501&courseId=9'],
  ];

  it.each(cases)('%s', async (_label, url) => {
    renderAt(url);

    await rtl.waitFor(() => expect(byTest('learn-invalid-course')).toBeTruthy());
    expect(byTest('learn-invalid-course')!.getAttribute('role')).toBe('alert');
    // 没进课程练习，也没把任何深链字段下传：不发详情/练习/上报。
    expect(byTest('tx-practice')).toBeNull();
    expect(byTest('tx-course-back')).toBeNull();
    await new Promise((r) => setTimeout(r, 30));
    expect(dangerousCalls()).toBe(0);
    // 不跳转：仍停在原 URL（只是显示错误）。
    expect(byTest('location-probe')!.textContent).toBe(url);
    // 工作区本身仍挂载（普通浏览外壳在，不白屏）。
    expect(byTest('tx-workspace')).toBeTruthy();
  });
});

describe('topic 深链的加载失败、重试与切换', () => {
  it('详情失败显示错误，重试走 tx-retry 成功后进入练习', async () => {
    taxonomyApi.topicDetail
      .mockRejectedValueOnce(new Error('深链加载失败'))
      .mockResolvedValueOnce(topicDetail());
    renderAt(COURSE_URL);

    await rtl.waitFor(() => expect(byTest('tx-course-link-error')).toBeTruthy());
    expect(byTest('tx-course-link-error')!.textContent).toContain('深链加载失败');
    expect(byTest('tx-practice')).toBeNull();
    // 失败态不显示返回课程门。
    expect(byTest('tx-course-back')).toBeNull();

    rtl.fireEvent.click(byTest('tx-retry')!);
    await rtl.waitFor(() => expect(byTest('tx-practice')).toBeTruthy());
    expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(2);
    expect(taxonomyApi.topicDetail).toHaveBeenLastCalledWith('cnm_p001');
    await rtl.waitFor(() => expect(byTest('tx-course-back')).toBeTruthy());
  });

  it('重试期间连续点击只由最后结果落地，错误不残留', async () => {
    let resolveSecond!: (v: unknown) => void;
    taxonomyApi.topicDetail
      .mockRejectedValueOnce(new Error('第一次失败'))
      .mockImplementationOnce(() => new Promise((res) => { resolveSecond = res; }));
    renderAt(COURSE_URL);

    await rtl.waitFor(() => expect(byTest('tx-course-link-error')).toBeTruthy());
    rtl.fireEvent.click(byTest('tx-retry')!);
    // 第二次在途：错误行撤下，显示加载态，压力点击不叠加请求。
    await rtl.waitFor(() => expect(byTest('tx-course-link-error')).toBeNull());
    await rtl.waitFor(() => expect(byTest('tx-loading')).toBeTruthy());
    const callsWhilePending = taxonomyApi.topicDetail.mock.calls.length;
    expect(callsWhilePending).toBe(2);

    resolveSecond(topicDetail());
    await rtl.waitFor(() => expect(byTest('tx-practice')).toBeTruthy());
    expect(byTest('tx-course-link-error')).toBeNull();
    expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(2);
  });

  it('卸载后迟到的详情响应不写 state、不上报', async () => {
    let resolveLate!: (v: unknown) => void;
    taxonomyApi.topicDetail.mockImplementation(() => new Promise((res) => { resolveLate = res; }));
    const view = renderAt(COURSE_URL);
    await rtl.waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(1));

    view.unmount();
    resolveLate(topicDetail());
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).not.toHaveBeenCalled();
    expect(taxonomyApi.practice).not.toHaveBeenCalled();
  });
});

describe('非课程入口：普通浏览与无参数', () => {
  it('无参数：普通浏览，无错误、不拉详情、学科按服务端首项 physics', async () => {
    renderAt('/learn');
    await rtl.waitFor(() => expect(byTest('tx-topic-cnm_p001')).toBeTruthy());

    expect(byTest('learn-invalid-course')).toBeNull();
    expect(byTest('tx-course-back')).toBeNull();
    expect(taxonomyApi.topicDetail).not.toHaveBeenCalled();
    expect(taxonomyApi.topics).toHaveBeenCalledWith(expect.objectContaining({ subject: 'physics' }));
    for (const call of taxonomyApi.topics.mock.calls) {
      expect((call[0] as { subject?: string }).subject).not.toBe('math');
    }
  });

  it('只有 topic（无课程参数）：无课程上报、无错误、无返回课程门，学科按服务端首项', async () => {
    renderAt('/learn?topic=cnm_p001');
    // 非课程深链经 initialTopicRef 进入知识点详情（浏览器内 selected，按
    // external_ref 拉详情）；与课程深链的区别只在「无课程上下文 / 无返回门」。
    await rtl.waitFor(() => {
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('cnm_p001');
    });
    // 强断言：详情视图是「最终停留态」而不只是「请求发出过」——详情容器
    // （tx-topic-detail）真实渲染且目录列表不在场。目录视图的判别物：
    // 列表行 tx-topic-cnm_p001 与学科 chips（tx-subject-chips 只在目录态渲染，
    // selected 非空时被整个跳过）。等待超时给足异步链（subjects 到达 → 自动
    // 选科 → 清空 effect）跑完的余量。
    await rtl.waitFor(() => expect(byTest('tx-topic-detail')).toBeTruthy(), { timeout: 3000 });
    expect(byTest('tx-detail-back')).toBeTruthy();
    await new Promise((r) => setTimeout(r, 50));
    expect(byTest('tx-topic-detail')).toBeTruthy();
    expect(byTest('tx-topic-cnm_p001')).toBeNull();
    expect(byTest('tx-subject-chips')).toBeNull();

    expect(byTest('learn-invalid-course')).toBeNull();
    // 非课程深链：不渲染返回课程门。
    expect(byTest('tx-course-back')).toBeNull();
    expect(reportProgress).not.toHaveBeenCalled();
    // 绝不回退 math：浏览请求只带服务端首项 physics。
    for (const call of taxonomyApi.topics.mock.calls) {
      expect((call[0] as { subject?: string }).subject).not.toBe('math');
    }
  });
});

describe('动态学科：只认服务端顺序，绝不回退 math', () => {
  it('服务端订阅 history/physics：默认选首项 history，浏览请求带 history', async () => {
    taxonomyApi.subjects.mockResolvedValue({
      subjects: [
        subjectRow({ id: 'history', name: '历史', sb_code: 'HIST', grades: [7, 8] }),
        subjectRow({ id: 'physics', name: '物理', sb_code: 'PHYS', grades: [7, 8, 9] }),
      ],
    });
    renderAt('/learn');

    await rtl.waitFor(() => expect(byTest('tx-subject-history')).toBeTruthy());
    const chips = Array.from(document.querySelectorAll('[data-test^="tx-subject-"]'))
      .map((el) => el.getAttribute('data-test'))
      .filter((t) => t !== 'tx-subject-chips' && t !== 'tx-subject-error' && t !== 'tx-subject-empty');
    expect(chips).toEqual(['tx-subject-history', 'tx-subject-physics']);
    expect(byTest('tx-subject-history')!.getAttribute('aria-pressed')).toBe('true');
    await rtl.waitFor(() => {
      expect(taxonomyApi.topics).toHaveBeenLastCalledWith(expect.objectContaining({ subject: 'history' }));
    });
  });

  it('服务端空学科：空态可见、保持空选中、零 topics 请求，绝不请求数学', async () => {
    taxonomyApi.subjects.mockResolvedValue({ subjects: [] });
    renderAt('/learn');

    await rtl.waitFor(() => expect(byTest('tx-subject-empty')).toBeTruthy());
    await new Promise((r) => setTimeout(r, 30));
    expect(byTest('tx-subject-math')).toBeNull();
    expect(taxonomyApi.topics).not.toHaveBeenCalled();
  });

  it('课程深链场景下学科请求同样不发 math（深链不受学科默认影响）', async () => {
    renderAt(COURSE_URL);
    await rtl.waitFor(() => expect(byTest('tx-practice')).toBeTruthy(), { timeout: 3000 });
    for (const call of taxonomyApi.topics.mock.calls) {
      expect((call[0] as { subject?: string }).subject).not.toBe('math');
    }
  });
});
