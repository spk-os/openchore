// @vitest-environment jsdom
// TaxonomyPrerequisitesCard 组件测试（卡 TX-PREREQ-UI-CODER）：
//  - 学科不默认 math：服务端顺序原样渲染，空表/失败呈现真实空态；
//  - PUT 参数（topic_id/prerequisite_id/strength/reason 全 external_ref）；
//  - PATCH rejected 参数与 404/409/400 服务端消息 role=alert 透传；
//  - busy 防重复、成功后重读详情刷新、失败不乐观污染；
//  - 学科/知识点切换乱序：旧慢响应不覆盖新选择；
//  - 空态（无学科、无知识点、无边）。
//
// 网络边界：taxonomyApi 在模块级 mock，组件只通过 subjects/topics/
// topicDetail/putPrerequisite/reviewPrerequisite 说话。
// useSubjects 走同一份 mock（模块图 doMock 后动态 import，避免 60s TTL
// 缓存与真实 fetch 串味）。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SubjectRow, TopicDetailResponse, TopicEdge, TopicRow } from '../../taxonomy/types';

const { taxonomyApi } = vi.hoisted(() => ({
  taxonomyApi: {
    subjects: vi.fn(),
    topics: vi.fn(),
    topicDetail: vi.fn(),
    putPrerequisite: vi.fn(),
    reviewPrerequisite: vi.fn(),
  },
}));

// i18n：mock t 按 key 返回（本卡不编辑 locale 文件，键由集成卡补齐）；
// 带插值参数时把值附在 key 后（key:v1,v2），便于断言 effective_edges 等真实入参。
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, opts?: unknown) => {
      if (opts && typeof opts === 'object') {
        const vals = Object.values(opts as Record<string, unknown>);
        if (vals.length) return `${key}:${vals.map(String).join(',')}`;
      }
      return key;
    },
  }),
}));

type RTL = typeof import('@testing-library/react');
let rtl: RTL;
let render: RTL['render'];
let screen: RTL['screen'];
let fireEvent: RTL['fireEvent'];
let waitFor: RTL['waitFor'];
let act: RTL['act'];
let TaxonomyPrerequisitesCard: typeof import('./TaxonomyPrerequisitesCard').TaxonomyPrerequisitesCard;

const subjectRow = (over: Partial<SubjectRow> = {}): SubjectRow => ({
  id: 'physics',
  name: '物理',
  sb_code: 'PHYS',
  topic_count: 42,
  question_count: 300,
  grades: [7, 8, 9],
  ...over,
});

/** 服务端学科顺序：physics 在前（服务端 sort_order），不依赖任何静态词表。 */
const serverSubjects = (): SubjectRow[] => [
  subjectRow({ id: 'physics', name: '物理' }),
  subjectRow({ id: 'math', name: '数学', sb_code: 'MATH', topic_count: 3108, question_count: 9000 }),
  subjectRow({ id: 'chinese', name: '语文', sb_code: 'CHN', topic_count: 120, question_count: 800, grades: [1, 2] }),
];

const topicRow = (over: Partial<TopicRow> = {}): TopicRow => ({
  id: '1',
  external_ref: 'phy_001',
  name: '力学基础',
  grade: 8,
  semester: null,
  domain: '力学',
  chapter: '第1章',
  subject: 'physics',
  ...over,
});

const topicEdge = (over: Partial<TopicEdge> = {}): TopicEdge => ({
  id: '2',
  external_ref: 'phy_000',
  name: '测量与单位',
  strength: 'hard',
  reason: '教材顺序',
  grade: 7,
  chapter: '第0章',
  ...over,
});

const detailRes = (over: Partial<TopicDetailResponse> = {}): TopicDetailResponse => ({
  topic: {
    ...topicRow(),
    type: null,
    mastery_criteria: [],
    root_reason: null,
  },
  prerequisites: [topicEdge()],
  dependents: [
    topicEdge({ id: '3', external_ref: 'phy_010', name: '牛顿定律', strength: 'soft', reason: '', grade: 8, chapter: '第2章' }),
  ],
  questions: [],
  standards: [],
  ...over,
});

beforeEach(async () => {
  vi.clearAllMocks();
  Object.values(taxonomyApi).forEach((fn) => fn.mockReset());
  taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });
  taxonomyApi.topics.mockResolvedValue({ topics: [topicRow()], next_cursor: null });
  taxonomyApi.topicDetail.mockResolvedValue(detailRes());

  // useSubjects 的 cached/cachedAt/inflight 是模块级共享状态；动态 import
  // 同一份模块图（RTL / testUtils / 组件），避免上一用例的成功快照串味。
  vi.resetModules();
  vi.doMock('../../taxonomy/client', () => ({ taxonomyApi }));
  rtl = await import('@testing-library/react');
  ({ render, screen, fireEvent, waitFor, act } = rtl);
  await import('../../taxonomy/testUtils'); // data-test 作为 testId 查询属性
  ({ TaxonomyPrerequisitesCard } = await import('./TaxonomyPrerequisitesCard'));
});

afterEach(() => {
  rtl.cleanup();
  vi.doUnmock('../../taxonomy/client');
});

/** 选中一个知识点（真实交互路径：先等候选行渲染，再点击，external_ref 寻址）。 */
async function pickTopic(ref: string) {
  const row = await screen.findByTestId(`tx-prereq-topic-${ref}`);
  fireEvent.click(row);
  await waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledWith(ref));
}

describe('TaxonomyPrerequisitesCard（家长先修边管理）', () => {
  describe('学科：服务端表驱动、不默认 math', () => {
    it('默认学科取服务端第一项（physics 在前），不默认 math', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await waitFor(() =>
        expect((screen.getByTestId('tx-prereq-subject') as HTMLSelectElement).value).toBe('physics'),
      );
      // select 值先落地，topics 随防抖定时器才发：等调用而非断言即得值。
      await waitFor(() =>
        expect(taxonomyApi.topics).toHaveBeenCalledWith(expect.objectContaining({ subject: 'physics' })),
      );
    });

    it('空学科表呈现空态、不请求 topics、绝不预设 math', async () => {
      taxonomyApi.subjects.mockResolvedValue({ subjects: [] });
      render(<TaxonomyPrerequisitesCard />);
      await screen.findByTestId('tx-prereq-subject-empty');
      expect(taxonomyApi.topics).not.toHaveBeenCalled();
      expect(screen.queryByTestId('tx-prereq-topic-search')).toBeNull();
    });

    it('学科加载失败显示 role=alert 且可重试', async () => {
      taxonomyApi.subjects.mockRejectedValue(new Error('subjects down'));
      render(<TaxonomyPrerequisitesCard />);
      const err = await screen.findByTestId('tx-prereq-subject-error');
      expect(err.getAttribute('role')).toBe('alert');
      expect(err.textContent).toContain('subjects down');
      taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });
      fireEvent.click(screen.getByTestId('tx-prereq-subject-retry'));
      await waitFor(() =>
        expect((screen.getByTestId('tx-prereq-subject') as HTMLSelectElement).value).toBe('physics'),
      );
    });

    it('显式 subject prop 直用（不取服务端第一项）', async () => {
      render(<TaxonomyPrerequisitesCard subject="chinese" />);
      await waitFor(() =>
        expect((screen.getByTestId('tx-prereq-subject') as HTMLSelectElement).value).toBe('chinese'),
      );
      expect(taxonomyApi.topics).toHaveBeenCalledWith(expect.objectContaining({ subject: 'chinese' }));
    });

    it('学科切换重拉 topics 并清空已选知识点（跨学科 ref 不可达）', async () => {
      // topics 按学科分流：切到 math 后候选不再是 physics 的知识点行。
      taxonomyApi.topics.mockImplementation((q: { subject?: string }) =>
        Promise.resolve(
          q?.subject === 'math'
            ? {
                topics: [topicRow({ id: '20', external_ref: 'math_001', name: '有理数', subject: 'math', chapter: '第1章' })],
                next_cursor: null,
              }
            : { topics: [topicRow()], next_cursor: null },
        ),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-subject'), { target: { value: 'math' } });
      await waitFor(() =>
        expect(taxonomyApi.topics).toHaveBeenLastCalledWith(expect.objectContaining({ subject: 'math' })),
      );
      expect(screen.queryByTestId('tx-prereq-detail')).toBeNull();
      expect(screen.queryByTestId('tx-prereq-topic-phy_001')).toBeNull();
    });
  });

  describe('知识点选择与详情', () => {
    it('候选列表按 external_ref 寻址，点击后请求详情', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      expect(taxonomyApi.topicDetail).toHaveBeenCalledWith('phy_001');
      expect(screen.getByTestId('tx-prereq-detail')).toBeTruthy();
    });

    it('缺 external_ref 的行（自建 tag）不可选：disabled 且点击无请求', async () => {
      taxonomyApi.topics.mockResolvedValue({
        topics: [topicRow({ id: '9', external_ref: '' }), topicRow()],
        next_cursor: null,
      });
      render(<TaxonomyPrerequisitesCard />);
      const noRef = (await screen.findByTestId('tx-prereq-topic-9')) as HTMLButtonElement;
      expect(noRef.disabled).toBe(true);
      fireEvent.click(noRef);
      await new Promise((r) => setTimeout(r, 0));
      expect(taxonomyApi.topicDetail).not.toHaveBeenCalled();
    });

    it('空候选列表显示空态（区分无搜索词/无匹配）', async () => {
      taxonomyApi.topics.mockResolvedValue({ topics: [], next_cursor: null });
      render(<TaxonomyPrerequisitesCard />);
      await screen.findByTestId('tx-prereq-topics-empty');
      fireEvent.change(screen.getByTestId('tx-prereq-topic-search'), { target: { value: '不存在' } });
      // 无匹配空态：空态元素就地切到 no-match 文案（旧 rows 保留期间不闪）。
      await screen.findByTestId('tx-prereq-topics-no-match');
      // 防抖后的重新请求把搜索词上送（q 显式非空）。
      await waitFor(() =>
        expect(taxonomyApi.topics).toHaveBeenLastCalledWith(expect.objectContaining({ q: '不存在' })),
      );
      expect(screen.getByTestId('tx-prereq-topics-no-match').textContent).toContain('admin.taxonomyTab.prereqTopicsNoMatch');
    });

    it('详情加载失败显示服务端错误且可重试', async () => {
      taxonomyApi.topicDetail
        .mockRejectedValueOnce(new Error('topic not found'))
        .mockResolvedValueOnce(detailRes());
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      const err = await screen.findByTestId('tx-prereq-detail-error');
      expect(err.getAttribute('role')).toBe('alert');
      expect(err.textContent).toContain('topic not found');
      fireEvent.click(screen.getByTestId('tx-prereq-detail-retry'));
      // 选择 1 次 + 重试 1 次；重试成功后详情渲染（边行回来）。
      await waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(2));
      await screen.findByTestId('tx-prereq-edge-phy_000');
      expect(screen.queryByTestId('tx-prereq-detail-error')).toBeNull();
    });

    it('有效边渲染 hard/soft 徽章、reason 空显示未记录；被依赖清单同屏', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      expect((await screen.findByTestId('tx-prereq-edge-phy_000')).textContent).toContain('测量与单位');
      expect(screen.getByTestId('tx-prereq-edge-strength-phy_000').textContent).toContain('admin.taxonomyTab.prereqHard');
      // soft 依赖行 reason 为空：显示未记录占位。
      expect(screen.getByTestId('tx-prereq-dependent-phy_010').textContent).toContain('admin.taxonomyTab.prereqNoReason');
      expect(screen.getByTestId('tx-prereq-dependent-strength-phy_010').textContent).toContain('admin.taxonomyTab.prereqSoftDep');
    });

    it('无先修边/无被依赖时显示空态', async () => {
      taxonomyApi.topicDetail.mockResolvedValue(detailRes({ prerequisites: [], dependents: [] }));
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      await screen.findByTestId('tx-prereq-edges-empty');
      expect(screen.getByTestId('tx-prereq-dependents-empty')).toBeTruthy();
      expect(screen.queryByTestId('tx-prereq-edge-phy_000')).toBeNull();
    });

    it('详情返回 prerequisites 为 null 防御为空列表（契约空数组，脏响应不崩溃）', async () => {
      taxonomyApi.topicDetail.mockResolvedValue(detailRes({ prerequisites: null, dependents: null }));
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      await screen.findByTestId('tx-prereq-edges-empty');
      expect(screen.getByTestId('tx-prereq-dependents-empty')).toBeTruthy();
    });

    it('乱序：切换知识点后旧知识点的慢详情不覆盖新知识点', async () => {
      let resolveSlow: ((d: TopicDetailResponse) => void) | undefined;
      taxonomyApi.topicDetail
        .mockImplementationOnce(() => new Promise<TopicDetailResponse>((resolve) => { resolveSlow = resolve; }))
        .mockResolvedValueOnce(detailRes({
          prerequisites: [topicEdge({ external_ref: 'phy_777', name: '新知识点先修' })],
        }));
      taxonomyApi.topics.mockResolvedValue({
        topics: [topicRow(), topicRow({ id: '2', external_ref: 'phy_002', name: '牛顿定律' })],
        next_cursor: null,
      });
      render(<TaxonomyPrerequisitesCard />);
      // 第一个知识点（phy_001）的详情慢；期间切到 phy_002。
      fireEvent.click(await screen.findByTestId('tx-prereq-topic-phy_001'));
      await waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(1));
      fireEvent.click(screen.getByTestId('tx-prereq-topic-phy_002'));
      await screen.findByTestId('tx-prereq-edge-phy_777');
      // 旧慢响应此刻到达：代数作废，不得覆盖新知识点详情。
      await act(async () => { resolveSlow!(detailRes()); });
      expect(screen.getByTestId('tx-prereq-edge-phy_777')).toBeTruthy();
      expect(screen.queryByTestId('tx-prereq-edge-phy_000')).toBeNull();
    });
  });

  describe('新增/改写边（PUT putPrerequisite）', () => {
    it('提交 PUT：四个字段全上送（ref 是 external_ref，reason trim）', async () => {
      // 候选含 phy_001（先修）与 phy_002（目标）：同屏可选目标知识点。
      taxonomyApi.topics.mockResolvedValue({
        topics: [topicRow(), topicRow({ id: '2', external_ref: 'phy_002', name: '牛顿定律', chapter: '第2章' })],
        next_cursor: null,
      });
      taxonomyApi.putPrerequisite.mockResolvedValue({
        edge: { topic_id: 'phy_002', prerequisite_id: 'phy_001', strength: 'soft', reason: '力的概念先于牛顿定律', review_status: 'reviewed' },
        effective_edges: 4626,
      });
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_002');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_001' } });
      fireEvent.change(screen.getByTestId('tx-prereq-strength'), { target: { value: 'soft' } });
      fireEvent.change(screen.getByTestId('tx-prereq-input-reason'), { target: { value: '  力的概念先于牛顿定律  ' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      await waitFor(() => expect(taxonomyApi.putPrerequisite).toHaveBeenCalledTimes(1));
      expect(taxonomyApi.putPrerequisite).toHaveBeenCalledWith({
        topic_id: 'phy_002',
        prerequisite_id: 'phy_001',
        strength: 'soft',
        reason: '力的概念先于牛顿定律',
      });
      // 不发 review_status：管理员新增即 reviewed 由后端语义处理。
      expect(Object.keys(taxonomyApi.putPrerequisite.mock.calls[0][0])).not.toContain('review_status');
    });

    it('成功后显示 effective_edges 提示并重读详情刷新', async () => {
      taxonomyApi.putPrerequisite.mockResolvedValue({
        edge: { topic_id: 'phy_001', prerequisite_id: 'phy_000', strength: 'hard', reason: '', review_status: 'reviewed' },
        effective_edges: 4626,
      });
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_000' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      const ok = await screen.findByTestId('tx-prereq-ok');
      expect(ok.getAttribute('role')).toBe('status');
      expect(ok.textContent).toContain('4626');
      // 重新读取详情（第一次是选择时，第二次是成功后刷新）。
      await waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(2));
    });

    it('未选知识点：提交禁用 + 本地提示，不发 PUT', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await screen.findByTestId('tx-prereq-topic-phy_001');
      const submit = screen.getByTestId('tx-prereq-submit') as HTMLButtonElement;
      expect(submit.disabled).toBe(true);
      expect(taxonomyApi.putPrerequisite).not.toHaveBeenCalled();
    });

    it('先修 ref 为空：本地提示（role=alert），不发 PUT', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      const err = await screen.findByTestId('tx-prereq-error');
      expect(err.getAttribute('role')).toBe('alert');
      expect(err.textContent).toContain('admin.taxonomyTab.prereqPickPrereqFirst');
      expect(taxonomyApi.putPrerequisite).not.toHaveBeenCalled();
    });

    it('自环（topic = prereq）：本地 400 语义提示，不发 PUT', async () => {
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_001' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      const err = await screen.findByTestId('tx-prereq-error');
      expect(err.textContent).toContain('admin.taxonomyTab.prereqSelfLoop');
      expect(taxonomyApi.putPrerequisite).not.toHaveBeenCalled();
    });

    it('409 成环：服务端消息 role=alert 透传，列表保持原样（不乐观污染）', async () => {
      taxonomyApi.putPrerequisite.mockRejectedValue(
        Object.assign(new Error('prerequisite edge conflicts with the graph: 先修图存在环'), { status: 409 }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_010' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      const err = await screen.findByTestId('tx-prereq-error');
      expect(err.getAttribute('role')).toBe('alert');
      expect(err.textContent).toContain('conflicts with the graph');
      // 失败不清边列表、不重读详情。
      expect(screen.getByTestId('tx-prereq-edge-phy_000')).toBeTruthy();
      expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(1);
      expect(screen.queryByTestId('tx-prereq-ok')).toBeNull();
    });

    it('404 未知 ref：服务端消息透传', async () => {
      taxonomyApi.putPrerequisite.mockRejectedValue(
        Object.assign(new Error('unknown topic_id'), { status: 404 }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'cnm_missing' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      const err = await screen.findByTestId('tx-prereq-error');
      expect(err.textContent).toContain('unknown topic_id');
    });

    it('400 服务端参数错误（如非法 strength）：消息透传', async () => {
      taxonomyApi.putPrerequisite.mockRejectedValue(
        Object.assign(new Error('strength must be "hard" or "soft"'), { status: 400 }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_010' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      expect((await screen.findByTestId('tx-prereq-error')).textContent).toContain('strength must be');
    });

    it('busy：PUT 在飞期间重复点击不重复请求，完成后恢复', async () => {
      let resolvePut: ((r: { edge: unknown; effective_edges: number }) => void) | undefined;
      taxonomyApi.putPrerequisite.mockImplementation(
        () => new Promise((resolve) => { resolvePut = resolve; }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_000' } });
      const submit = screen.getByTestId('tx-prereq-submit') as HTMLButtonElement;
      fireEvent.click(submit);
      await waitFor(() => expect(submit.disabled).toBe(true));
      fireEvent.click(submit);
      expect(taxonomyApi.putPrerequisite).toHaveBeenCalledTimes(1);
      await act(async () => {
        resolvePut!({
          edge: { topic_id: 'phy_001', prerequisite_id: 'phy_000', strength: 'hard', reason: '', review_status: 'reviewed' },
          effective_edges: 4626,
        });
      });
      await waitFor(() => expect(submit.disabled).toBe(false));
    });
  });

  describe('驳回已知边（PATCH reviewPrerequisite）', () => {
    it('PATCH rejected：三字段上送（topic/prereq 均 external_ref）', async () => {
      taxonomyApi.reviewPrerequisite.mockResolvedValue({
        edge: { topic_id: 'phy_001', prerequisite_id: 'phy_000', review_status: 'rejected' },
        effective_edges: 4625,
      });
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.click(screen.getByTestId('tx-prereq-reject-phy_000'));
      await waitFor(() => expect(taxonomyApi.reviewPrerequisite).toHaveBeenCalledTimes(1));
      expect(taxonomyApi.reviewPrerequisite).toHaveBeenCalledWith({
        topic_id: 'phy_001',
        prerequisite_id: 'phy_000',
        review_status: 'rejected',
      });
      await screen.findByTestId('tx-prereq-ok');
      await waitFor(() => expect(taxonomyApi.topicDetail).toHaveBeenCalledTimes(2));
    });

    it('404 未知边（prerequisite edge not found）：服务端消息 role=alert 透传', async () => {
      taxonomyApi.reviewPrerequisite.mockRejectedValue(
        Object.assign(new Error('prerequisite edge not found'), { status: 404 }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.click(screen.getByTestId('tx-prereq-reject-phy_000'));
      const err = await screen.findByTestId('tx-prereq-error');
      expect(err.getAttribute('role')).toBe('alert');
      expect(err.textContent).toContain('prerequisite edge not found');
      // 边行保留（失败不乐观删除）。
      expect(screen.getByTestId('tx-prereq-edge-phy_000')).toBeTruthy();
    });

    it('409（恢复会成环的场景）：服务端消息透传', async () => {
      taxonomyApi.reviewPrerequisite.mockRejectedValue(
        Object.assign(new Error('review_status flip conflicts with the graph: 先修图存在环'), { status: 409 }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      fireEvent.click(screen.getByTestId('tx-prereq-reject-phy_000'));
      expect((await screen.findByTestId('tx-prereq-error')).textContent).toContain('flip conflicts');
    });

    it('busy：PATCH 在飞期间驳回按钮禁用、PUT 提交也不重复', async () => {
      let resolvePatch: ((r: { edge: unknown; effective_edges: number }) => void) | undefined;
      taxonomyApi.reviewPrerequisite.mockImplementation(
        () => new Promise((resolve) => { resolvePatch = resolve; }),
      );
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      const reject = screen.getByTestId('tx-prereq-reject-phy_000') as HTMLButtonElement;
      fireEvent.click(reject);
      await waitFor(() => expect(reject.disabled).toBe(true));
      const submit = screen.getByTestId('tx-prereq-submit') as HTMLButtonElement;
      // busy 期间点击驳回按钮（即使 enabled）也不重复请求。
      fireEvent.click(reject);
      expect(taxonomyApi.reviewPrerequisite).toHaveBeenCalledTimes(1);
      expect(submit.disabled).toBe(true);
      await act(async () => {
        resolvePatch!({
          edge: { topic_id: 'phy_001', prerequisite_id: 'phy_000', review_status: 'rejected' },
          effective_edges: 4625,
        });
      });
      await waitFor(() => expect(reject.disabled).toBe(false));
    });
  });

  describe('跨状态隔离', () => {
    it('成功消息与错误消息互斥（新操作清旧反馈）', async () => {
      taxonomyApi.putPrerequisite.mockResolvedValue({
        edge: { topic_id: 'phy_001', prerequisite_id: 'phy_000', strength: 'hard', reason: '', review_status: 'reviewed' },
        effective_edges: 4626,
      });
      render(<TaxonomyPrerequisitesCard />);
      await pickTopic('phy_001');
      // 先制造一个本地错误（空 prereq）。
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      await screen.findByTestId('tx-prereq-error');
      // 再提交成功：错误清空。
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_000' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      await screen.findByTestId('tx-prereq-ok');
      expect(screen.queryByTestId('tx-prereq-error')).toBeNull();
      // 再制造失败：成功清空。成功提交后表单已清空，重填 ref 再提交。
      taxonomyApi.putPrerequisite.mockRejectedValue(new Error('later failure'));
      fireEvent.change(screen.getByTestId('tx-prereq-input-prereq'), { target: { value: 'phy_000' } });
      fireEvent.click(screen.getByTestId('tx-prereq-submit'));
      await waitFor(() => expect(screen.getByTestId('tx-prereq-error').textContent).toContain('later failure'));
      expect(screen.queryByTestId('tx-prereq-ok')).toBeNull();
    });

    it('乱序：学科切换后旧学科的慢 topics 响应不覆盖新学科列表', async () => {
      let resolveSlow: ((p: { topics: TopicRow[]; next_cursor: string | null }) => void) | undefined;
      taxonomyApi.topics
        .mockImplementationOnce(() => new Promise((resolve) => { resolveSlow = resolve; }))
        .mockResolvedValueOnce({
          topics: [topicRow({ id: '5', external_ref: 'chn_001', name: '拼音', subject: 'chinese', chapter: '第1课' })],
          next_cursor: null,
        });
      render(<TaxonomyPrerequisitesCard />);
      await waitFor(() => expect(taxonomyApi.topics).toHaveBeenCalledTimes(1));
      fireEvent.change(screen.getByTestId('tx-prereq-subject'), { target: { value: 'chinese' } });
      await screen.findByTestId('tx-prereq-topic-chn_001');
      // 旧学科慢响应到达：代数作废，不得覆盖新学科候选列表。
      await act(async () => {
        resolveSlow!({ topics: [topicRow()], next_cursor: null });
      });
      expect(screen.getByTestId('tx-prereq-topic-chn_001')).toBeTruthy();
      expect(screen.queryByTestId('tx-prereq-topic-phy_001')).toBeNull();
    });
  });
});
