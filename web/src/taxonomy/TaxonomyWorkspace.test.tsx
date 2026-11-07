// @vitest-environment jsdom
// TaxonomyWorkspace 组件关键测试（卡 TX-FE-CODER）：标签切换、学科浏览的
// loading/empty/error/重试、题目渲染（LaTeX 回退 + 安全 SVG）、诊断答题流的
// choice 提交与服务端推进。api 客户端在模块级 mock——组件只通过 taxonomyApi
// 说话；这些用例验证的是「组件 ↔ 契约」的映射，不是端到端（后端联调 pending）。
//
// 042 动态学科夹具：SubjectBrowser 经 useSubjects 读 taxonomyApi.subjects；
// 本文件按 useSubjects.test.tsx 的模式逐用例 resetModules + 动态 import 同一份
// 模块图，提供真实 SubjectRow 形状的服务端响应，避免共享缓存串味和
// undefined.then / jsdom 真实 fetch。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SubjectRow, TopicRow } from './types';

const { taxonomyApi } = vi.hoisted(() => ({
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
}));
vi.mock('./client', () => ({ taxonomyApi, TaxonomyAPIError: class extends Error {} }));

const subjectRow = (over: Partial<SubjectRow> = {}): SubjectRow => ({
  id: 'math',
  name: '数学',
  sb_code: 'MATH',
  topic_count: 3108,
  question_count: 9000,
  grades: [1, 2, 3],
  ...over,
});

/** 服务端 subjects 表顺序（sort_order）：physics 在前，不依赖任何静态词表。 */
const serverSubjects = (): SubjectRow[] => [
  subjectRow({ id: 'physics', name: '物理', sb_code: 'PHYS', topic_count: 42, question_count: 300, grades: [7, 8, 9] }),
  subjectRow({ id: 'math', name: '数学', sb_code: 'MATH' }),
  subjectRow({ id: 'chinese', name: '语文', sb_code: 'CHN', topic_count: 120, question_count: 800, grades: [1, 2] }),
];

// 真实 API browse 行（GET /api/taxonomy/topics）：id 是 DB 自增整数
// （knowledge_tags.id，如 1），知识点寻址键是 external_ref（cnm_p001）。
// 组件的 data-test 与点击必须用 external_ref——传本地整数 id 会被
// /api/taxonomy/topics/{externalRef} 404。
// 真实 API 必有 subject（GET /api/taxonomy/topics 行带学科）；夹具显式给 math，
// 不依赖组件层的任何默认学科——诊断请求的 subject 必须来自行数据本身。
const topicRow = (over: Partial<TopicRow> = {}): TopicRow => ({
  id: '1',
  name: '1到5各数的认识',
  subject: 'math',
  grade: 1,
  semester: '上',
  domain: '数与代数',
  chapter: '一年级上册 第1单元 5以内数的认识和加、减法',
  external_ref: 'cnm_p001',
  ...over,
});

const subjectChipSelector = '[data-test^="tx-subject-"]:not([data-test="tx-subject-chips"]):not([data-test="tx-subject-error"]):not([data-test="tx-subject-empty"])';

type RTL = typeof import('@testing-library/react');
let rtl: RTL;
let cleanup: RTL['cleanup'];
let fireEvent: RTL['fireEvent'];
let render: RTL['render'];
let screen: RTL['screen'];
let waitFor: RTL['waitFor'];
let TaxonomyWorkspace: typeof import('./TaxonomyWorkspace').TaxonomyWorkspace;

beforeEach(async () => {
  vi.clearAllMocks();
  // 逐用例清空实现（含 once 队列），再登记默认服务端响应。
  Object.values(taxonomyApi).forEach(fn => fn.mockReset());
  taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });
  taxonomyApi.topics.mockResolvedValue({ topics: [], next_cursor: null });

  // useSubjects 的 cached/cachedAt/inflight 是模块级共享状态；动态 import 同一份
  // 模块图（RTL / testUtils / 组件），保证 React 实例与 configure 的 testId 属性一致。
  vi.resetModules();
  vi.doMock('./client', () => ({ taxonomyApi, TaxonomyAPIError: class extends Error {} }));
  rtl = await import('@testing-library/react');
  ({ cleanup, fireEvent, render, screen, waitFor } = rtl);
  await import('./testUtils'); // data-test 作为 testId 查询属性（仓库约定）
  ({ TaxonomyWorkspace } = await import('./TaxonomyWorkspace'));
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.doUnmock('./client');
});

describe('浏览标签', () => {
  it('学科清单按服务端顺序渲染，不回退静态学科表', async () => {
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
    render(<TaxonomyWorkspace audience="kid" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
    const chips = [...document.querySelectorAll(subjectChipSelector)];
    expect(chips.map(el => el.getAttribute('data-test'))).toEqual([
      'tx-subject-physics', 'tx-subject-math', 'tx-subject-chinese',
    ]);
    expect(screen.queryByTestId('tx-subject-biology')).toBeNull();
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(1);
  });

  it('学科清单失败显示错误态，重试成功后按服务端数据渲染', async () => {
    // 显式控制 rejected/resolved：失败不回退硬编码学科，重试走真实 subjects 请求。
    taxonomyApi.subjects
      .mockRejectedValueOnce(new Error('学科服务不可用'))
      .mockResolvedValueOnce({ subjects: [subjectRow({ id: 'history', name: '历史', sb_code: 'HIST' })] });
    render(<TaxonomyWorkspace audience="parent" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-subject-error')).toBeTruthy();
    });
    expect(screen.getByText('学科服务不可用')).toBeTruthy();
    expect(document.querySelector(subjectChipSelector)).toBeNull();

    fireEvent.click(screen.getByTestId('tx-retry'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-subject-history')).toBeTruthy();
    });
    expect(screen.queryByTestId('tx-subject-error')).toBeNull();
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(2);
  });

  it('学科切换重新拉列表；列表按章分组渲染', async () => {
    taxonomyApi.topics.mockResolvedValue({
      topics: [topicRow()],
      next_cursor: null,
    });
    render(<TaxonomyWorkspace audience="kid" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
    expect(screen.getByText(/一年级上册 第1单元/)).toBeTruthy();

    // 切换学科 → 重新请求（subject=chinese）。
    fireEvent.click(screen.getByTestId('tx-subject-chinese'));
    await waitFor(() => {
      expect(taxonomyApi.topics).toHaveBeenLastCalledWith(
        expect.objectContaining({ subject: 'chinese' }),
      );
    });
  });

  it('空列表显示空态', async () => {
    taxonomyApi.topics.mockResolvedValue({ topics: [], next_cursor: null });
    render(<TaxonomyWorkspace audience="parent" userId={2} />);
    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-empty')).toBeTruthy();
    });
  });

  it('错误态显示服务端消息并提供重试', async () => {
    taxonomyApi.topics.mockRejectedValueOnce(new Error('网络中断'));
    taxonomyApi.topics.mockResolvedValueOnce({ topics: [topicRow()], next_cursor: null });
    render(<TaxonomyWorkspace audience="parent" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-error')).toBeTruthy();
    });
    expect(screen.getByText('网络中断')).toBeTruthy();

    fireEvent.click(screen.getByTestId('tx-retry'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
  });

  it('加载更多按钮游标分页', async () => {
    taxonomyApi.topics.mockResolvedValueOnce({ topics: [topicRow({ id: '2', external_ref: 'cnm_p002' })], next_cursor: 'c2' });
    taxonomyApi.topics.mockResolvedValueOnce({ topics: [topicRow({ id: '3', external_ref: 'cnm_p003' })], next_cursor: null });
    render(<TaxonomyWorkspace audience="parent" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p002')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-load-more'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p003')).toBeTruthy();
      expect(screen.queryByTestId('tx-load-more')).toBeNull();
    });
  });
});

describe('详情与诊断联动', () => {
  it('点知识点进详情，危险 SVG 题图显示拒绝占位，正常题图渲染', async () => {
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
    taxonomyApi.topicDetail.mockResolvedValue({
      topic: topicRow({ description: '数一数，比一比' }),
      prerequisites: [
        { id: '0', name: '数的认识', strength: 'hard', reason: '', grade: 1, chapter: '', external_ref: 'cnm_p000' },
      ],
      dependents: [],
      questions: [
        {
          id: 'cnm_p001_q1',
          type: 'numeric',
          difficulty: 'basic',
          prompt: '3 和 1 相加等于几？$3+1=4$',
          choices: null,
          figure: '<svg><script>alert(1)</script></svg>',
        },
      ],
      standards: [{ id: 'cms-2022:1.NA.01', confidence: 'exact', note: null, text: '条目' }],
    });
    const { container } = render(<TaxonomyWorkspace audience="kid" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-topic-cnm_p001'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-detail')).toBeTruthy();
    });
    // 点击发出的是 external_ref（详情按 ref 寻址），不是本地整数 id=1。
    await waitFor(() => {
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('cnm_p001');
    });
    // 局部先修图渲染了先修节点。
    expect(container.querySelector('[data-test="tx-graph-node-cnm_p000"]')).toBeTruthy();
    // 危险题图被拒绝：没有 script，只有占位。
    expect(container.querySelector('script')).toBeNull();
    expect(screen.getByTestId('figure-blocked')).toBeTruthy();
    // 课标条目。
    expect(screen.getByText('cms-2022:1.NA.01 · 直接对应')).toBeTruthy();
  });

  it('「诊断这个知识点」切到诊断标签并发起（请求体带目标）', async () => {
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
    taxonomyApi.topicDetail.mockResolvedValue({
      topic: topicRow(),
      prerequisites: [],
      dependents: [],
      questions: [],
      standards: [],
    });
    taxonomyApi.createDiagnostic.mockResolvedValue({
      session: { id: 11, subject: 'math', mode: 'target', status: 'active', limit: 16 },
      // 真实契约：question 是 {topic_id, attempt, question} 两段结构。
      question: {
        topic_id: 'cnm_p001',
        attempt: 1,
        question: {
          id: 'cnm_p001_q1',
          type: 'choice',
          difficulty: 'basic',
          prompt: '4 和 2 比，哪个数大？',
          choices: [
            { key: 'A', text: '4' },
            { key: 'B', text: '2' },
            { key: 'C', text: '一样大' },
          ],
        },
      },
      attempts: [],
    });
    render(<TaxonomyWorkspace audience="kid" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-topic-cnm_p001'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-detail-diagnose')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-detail-diagnose'));

    // 详情页「诊断这个知识点」切到诊断标签：StartPanel 预填目标，需点「开始挑战」发起。
    await waitFor(() => {
      expect(screen.getByTestId('tx-diag-begin')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-diag-begin'));

    await waitFor(() => {
      expect(taxonomyApi.createDiagnostic).toHaveBeenCalledWith(
        expect.objectContaining({ mode: 'target', subject: 'math', targets: ['cnm_p001'] }),
      );
    });
    // 答题流出现。
    await waitFor(() => {
      expect(screen.getByTestId('tx-diag-question')).toBeTruthy();
    });
    // choice 点选即提交。
    fireEvent.click(screen.getByTestId('tx-dchoice-A'));
    await waitFor(() => {
      expect(taxonomyApi.answerDiagnostic).toHaveBeenCalledWith(11, {
        question_id: 'cnm_p001_q1',
        answer: 'A',
      });
    });
  });

  it('列表行 data-test 与选择都走 external_ref，绝不发本地整数 id', async () => {
    // 真实 API browse 行：数字 id=1 + external_ref=cnm_p001。
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
    taxonomyApi.topicDetail.mockResolvedValue({
      topic: topicRow(),
      prerequisites: [],
      dependents: [],
      questions: [],
      standards: [],
    });
    const { container } = render(<TaxonomyWorkspace audience="parent" userId={2} />);

    await waitFor(() => {
      expect(screen.getByTestId('tx-topic-cnm_p001')).toBeTruthy();
    });
    // data-test 绝不用本地整数 id（tx-topic-1 是 bug 的形状）。
    expect(container.querySelector('[data-test="tx-topic-1"]')).toBeNull();

    fireEvent.click(screen.getByTestId('tx-topic-cnm_p001'));
    await waitFor(() => {
      expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(1);
    });
    expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('cnm_p001');
  });
});

describe('盲区与历史标签', () => {
  it('盲区空态（孩子端文案适龄）', async () => {
    taxonomyApi.blindspots.mockResolvedValue({ weak: [], shaky: [], mastered: [], by_chapter: [] });
    render(<TaxonomyWorkspace audience="kid" userId={2} />);
    fireEvent.click(screen.getByTestId('tx-tab-blindspots'));
    await waitFor(() => {
      expect(screen.getByTestId('tx-blind-empty')).toBeTruthy();
    });
    expect(screen.getByTestId('tx-blind-empty').textContent).toContain('再挑战');
  });

  it('盲区有薄弱点可整组再测（切诊断并发起）', async () => {
    taxonomyApi.blindspots.mockResolvedValue({
      weak: [
        { id: 'cnm_p003', name: '分与合', status: 'weak', root: true, chapter: '第一章', grade: 1, subject: 'math' },
      ],
      shaky: [],
      mastered: [],
      by_chapter: [],
    });
    taxonomyApi.createDiagnostic.mockResolvedValue({
      session: { id: 12, subject: 'math', mode: 'target', status: 'active', limit: 16 },
      attempts: [],
    });
    render(<TaxonomyWorkspace audience="parent" userId={3} />);
    fireEvent.click(screen.getByTestId('tx-tab-blindspots'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-retest-roots')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-retest-roots'));
    // 「再测薄弱根因」切到诊断标签并预填目标，点「开始诊断」发起。
    await waitFor(() => {
      expect(screen.getByTestId('tx-diag-begin')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-diag-begin'));
    await waitFor(() => {
      expect(taxonomyApi.createDiagnostic).toHaveBeenCalledWith(
        expect.objectContaining({ targets: ['cnm_p003'] }),
      );
    });
  });

  it('历史列表点击续答', async () => {
    taxonomyApi.diagnostics.mockResolvedValue({
      sessions: [
        { id: 21, subject: 'math', mode: 'screen', status: 'active', label: '七年级筛查', asked: 3 },
      ],
    });
    taxonomyApi.diagnosticDetail.mockResolvedValue({
      session: { id: 21, subject: 'math', mode: 'screen', status: 'active', limit: 40 },
      // 真实契约：question 两段结构 {topic_id, attempt, question}。
      question: {
        topic_id: 'cnm_j001',
        attempt: 2,
        question: {
          id: 'q9',
          type: 'numeric',
          difficulty: 'standard',
          prompt: '计算',
          choices: null,
        },
      },
      attempts: [{ id: 1, seq: 1, result: 'correct', answer_json: '4', status: 'answered' }],
    });
    render(<TaxonomyWorkspace audience="parent" userId={4} />);
    fireEvent.click(screen.getByTestId('tx-tab-history'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-history-21')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-history-21'));
    await waitFor(() => {
      expect(taxonomyApi.diagnosticDetail).toHaveBeenCalledWith(21);
    });
    await waitFor(() => {
      expect(screen.getByTestId('tx-diag-question')).toBeTruthy();
    });
  });
});

describe('历史标签导出（卡 TX-FE-EXPORT）', () => {
  // jsdom 无真实下载；用 spy 验证 URL.createObjectURL + <a download> 路径被走到。
  const createObjectURL = vi.fn(() => 'blob:mock');
  const revokeObjectURL = vi.fn();

  beforeEach(() => {
    createObjectURL.mockClear();
    revokeObjectURL.mockClear();
    Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, writable: true });
    Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, writable: true });
  });

  it('孩子端：点导出 → exportData(自己) → Blob 下载 + 成功反馈', async () => {
    // Safari 延迟 revoke：fake timers 全程启用，click 后 revoke 不该
    // 同步发生，推进 10s 后才释放 URL。
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      taxonomyApi.diagnostics.mockResolvedValue({ sessions: [] });
      taxonomyApi.exportData.mockResolvedValue(new Blob(['{}'], { type: 'application/json' }));
      render(<TaxonomyWorkspace audience="kid" userId={7} />);
      fireEvent.click(screen.getByTestId('tx-tab-history'));

      await waitFor(() => {
        expect(screen.getByTestId('tx-export-download')).toBeTruthy();
      });
      // 孩子端文案适龄。
      expect(screen.getByTestId('tx-export-download').textContent).toContain('保存我的学习记录');

      fireEvent.click(screen.getByTestId('tx-export-download'));
      await waitFor(() => {
        expect(taxonomyApi.exportData).toHaveBeenCalledWith(7);
      });
      await waitFor(() => {
        expect(createObjectURL).toHaveBeenCalled();
      });
      // revoke 是延迟的（Safari 截断修复）：click 返回时还没释放。
      expect(revokeObjectURL).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(10_500);
      expect(revokeObjectURL).toHaveBeenCalledWith('blob:mock');
      // 成功反馈出现，无错误。
      await waitFor(() => {
        expect(screen.getByText('已下载。')).toBeTruthy();
      });
      expect(screen.queryByTestId('tx-export-error')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('家长端：exportData 带当前选中的孩子 user_id', async () => {
    taxonomyApi.diagnostics.mockResolvedValue({ sessions: [] });
    taxonomyApi.exportData.mockResolvedValue(new Blob(['{}'], { type: 'application/json' }));
    render(<TaxonomyWorkspace audience="parent" userId={12} />);
    fireEvent.click(screen.getByTestId('tx-tab-history'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-export-download')).toBeTruthy();
    });
    expect(screen.getByTestId('tx-export-download').textContent).toContain('导出学习数据');
    fireEvent.click(screen.getByTestId('tx-export-download'));
    await waitFor(() => {
      expect(taxonomyApi.exportData).toHaveBeenCalledWith(12);
    });
  });

  it('失败显示服务端错误消息，可重试成功', async () => {
    taxonomyApi.diagnostics.mockResolvedValue({ sessions: [] });
    taxonomyApi.exportData
      .mockRejectedValueOnce(new Error('需要管理员权限'))
      .mockResolvedValueOnce(new Blob(['{}'], { type: 'application/json' }));
    render(<TaxonomyWorkspace audience="parent" userId={5} />);
    fireEvent.click(screen.getByTestId('tx-tab-history'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-export-download')).toBeTruthy();
    });
    fireEvent.click(screen.getByTestId('tx-export-download'));

    await waitFor(() => {
      expect(screen.getByTestId('tx-export-error')).toBeTruthy();
    });
    expect(screen.getByTestId('tx-export-error').textContent).toContain('需要管理员权限');

    // 重试（ErrorRow 自带 tx-retry）→ 成功。
    fireEvent.click(screen.getByTestId('tx-retry'));
    await waitFor(() => {
      expect(screen.getByText('已下载。')).toBeTruthy();
    });
    expect(screen.queryByTestId('tx-export-error')).toBeNull();
  });
});
