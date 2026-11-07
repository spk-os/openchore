// @vitest-environment jsdom
// PaperAnalysisModal (L1, parent side): the two-stage paper analysis flow
// behind the approval card — subject pick + launch, the 2s poll until
// done, the diagnosis report (stats / error mix / per-question rows) and
// the one-tap correction task from a wrong question.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { PaperAnalysis } from '../../types';
import type { SubjectRow } from '../../taxonomy/types';

// The API client is mocked at the module level: the modal must only talk
// to api.admin.startPaperAnalysis / getPaperAnalysis / createCorrectionTask
// and api.errorItems.list (the manual-fix queue).
const { api, taxonomyApi } = vi.hoisted(() => ({
  api: {
    admin: {
      startPaperAnalysis: vi.fn(),
      getPaperAnalysis: vi.fn(),
      createCorrectionTask: vi.fn(),
      updateErrorItem: vi.fn(),
    },
    errorItems: { list: vi.fn() },
  },
  taxonomyApi: { subjects: vi.fn() },
}));
vi.mock('../../api', () => ({ api }));
// 组件经 useSubjects 读学科清单：按 hook 的 ../taxonomy/client 解析路径提供真实形状的
// subjects()，避免 jsdom 真实 fetch 与 undefined.then；其余 api mock 原样保留。
vi.mock('../../taxonomy/client', () => ({ taxonomyApi }));

const subjectRow = (over: Partial<SubjectRow> = {}): SubjectRow => ({
  id: 'math',
  name: '数学',
  sb_code: 'MATH',
  topic_count: 3108,
  question_count: 9000,
  grades: [1, 2, 3],
  ...over,
});

const serverSubjects = (): SubjectRow[] => [
  subjectRow({ id: 'math', name: '数学', sb_code: 'MATH' }),
  subjectRow({ id: 'physics', name: '物理', sb_code: 'PHYS', topic_count: 42, question_count: 300, grades: [7, 8, 9] }),
];


const runningAnalysis = (over: Partial<PaperAnalysis> = {}): PaperAnalysis => ({
  id: 5,
  completion_id: 42,
  user_id: 2,
  created_by: 1,
  subject: 'math',
  status: 'running',
  stage: 'segmenting',
  progress: 20,
  total_questions: 8,
  wrong_count: 0,
  error: '',
  created_at: '2026-10-13T00:00:00Z',
  updated_at: '2026-10-13T00:00:00Z',
  ...over,
});

const doneAnalysis = (over: Partial<PaperAnalysis> = {}): PaperAnalysis => runningAnalysis({
  id: 5,
  status: 'done',
  stage: 'done',
  progress: 100,
  total_questions: 3,
  wrong_count: 1,
  summary: {
    questions: [
      {
        number: '1',
        question_text: '计算 $1+1$',
        attempted: true,
        is_wrong: false,
        answer_text: '$2$',
        mistake_analysis: '',
        error_type: '',
        knowledge_points: [],
        confidence: 0.99,
      },
      {
        number: '2',
        question_text: '解方程 $x-2=0$',
        attempted: true,
        is_wrong: true,
        wrong_answer_text: '$3$',
        answer_text: '$2$',
        mistake_analysis: '移项出错',
        error_type: 'calculation',
        knowledge_points: ['一元一次方程'],
        confidence: 0.9,
        error_item_id: 17,
      },
    ],
    error_type_counts: { calculation: 1 },
    weak_tags: [{ tag_id: 1, name: '一元一次方程', subject: 'math', wrong_count: 1 }],
    notes: ['两道题已判读'],
  },
  ...over,
});

// useSubjects 的缓存是模块级单例：逐用例 resetModules + 动态 import 同一份模块图
// （RTL / i18n / 组件），让每个用例独立从 taxonomyApi.subjects 拿学科，且 React 实例一致。
type RTL = typeof import('@testing-library/react');
let rtl: RTL;
let render: RTL['render'];
let screen: RTL['screen'];
let fireEvent: RTL['fireEvent'];
let waitFor: RTL['waitFor'];
let act: RTL['act'];
let PaperAnalysisModal: typeof import('./PaperAnalysisCard').PaperAnalysisModal;

beforeEach(async () => {
  vi.clearAllMocks();
  taxonomyApi.subjects.mockReset();
  taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });
  vi.mocked(api.errorItems.list).mockResolvedValue([]);

  vi.resetModules();
  vi.doMock('../../api', () => ({ api }));
  vi.doMock('../../taxonomy/client', () => ({ taxonomyApi }));
  rtl = await import('@testing-library/react');
  ({ render, screen, fireEvent, waitFor, act } = rtl);
  const { default: freshI18n } = await import('../../i18n');
  await freshI18n.changeLanguage('en');
  ({ PaperAnalysisModal } = await import('./PaperAnalysisCard'));
});

afterEach(() => {
  rtl.cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
  vi.doUnmock('../../api');
  vi.doUnmock('../../taxonomy/client');
});


/**
 * R2：subjects 在途时「AI paper analysis」按钮是 disabled（不许在无学科时起手），
 * 旧用例的裸 click 打在 disabled 按钮上什么也不发生。这里先等 select 里出现
 * 合法学科 option（hook 已就绪），再点真正的按钮；绝不以恢复空 subject/math
 * 默认值来绕过。
 */
const launchAfterSubjects = async () => {
  await waitFor(() => {
    expect(q('[data-test="pa-subject-select"] option[value="physics"]')).toBeTruthy();
  });
  const btn = screen.getByRole('button', { name: /AI paper analysis/i }) as HTMLButtonElement;
  expect(btn.disabled).toBe(false);
  fireEvent.click(btn);
};

// The components use data-test (not data-testid): query through the body.
const q = (sel: string) => document.body.querySelector(sel)!;

describe('PaperAnalysisModal', () => {
  it('launches the analysis with the picked subject', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(runningAnalysis());
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    // OD pa-upload / pa-upload-note: the launch view explains the flow and
    // the failed-parse promise before the parent commits.
    expect(q('[data-test="pa-upload"]')).toBeTruthy();
    expect(q('[data-test="pa-upload-note"]').textContent).toBeTruthy();

    // 等 subjects promise resolve：select 里出现 physics/math 后才 change，
    // 否则受控 select 的 value 仍停留在空串（旧夹具的 '' → startPaperAnalysis）。
    await waitFor(() => {
      expect(q('[data-test="pa-subject-select"] option[value="physics"]')).toBeTruthy();
    });
    const subject = screen.getByLabelText(/Subject/) as HTMLSelectElement;
    // 组件 state 起手 ''：subjects 到位前没有可选项，到位后也不把 state 改写成
    // math（DOM 无 <option value=""> 时浏览器会把 select.value 显示成首个 option，
    // 但发给后端的仍是 React state，见下一个用例）。
    fireEvent.change(subject, { target: { value: 'physics' } });
    expect(subject.value).toBe('physics');
    // 问题③: the parent's free-text focus rides the start request.
    fireEvent.change(q('[data-test="pa-instructions"]'), { target: { value: 'focus on question 3' } });
    await launchAfterSubjects();

    await waitFor(() => expect(api.admin.startPaperAnalysis).toHaveBeenCalledWith(42, 'physics', 'focus on question 3'));
    // The progress rail replaces the launch form while running.
    await waitFor(() => expect(q('[data-test="pa-progress"]')).toBeTruthy());
    expect(q('[data-test="pa-steps"]')).toBeTruthy();
    // OD pa-paper-thumb + pa-cut-1/2/3: the live page thumbnail and the
    // three stage mini cards ride along the progress bar.
    expect(q('[data-test="pa-paper-thumb"]')).toBeTruthy();
    expect(q('[data-test="pa-cut-1"]')).toBeTruthy();
    expect(q('[data-test="pa-cut-2"]')).toBeTruthy();
    expect(q('[data-test="pa-cut-3"]')).toBeTruthy();
  });

  it('does not silently default an unselected subject to math', async () => {
    // 空串起手：受控 select 的 value='' 在无空 option 时 DOM 显示首个 option，
    // 但组件 state 仍为 ''；家长不选就按空学科发起，绝不静默改 math。
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(doneAnalysis());
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    await waitFor(() => {
      const el = q('[data-test="pa-subject-select"]') as HTMLSelectElement;
      expect(el.querySelector('option[value="physics"]')).toBeTruthy();
      expect(el.querySelector('option[value="math"]')).toBeTruthy();
    });
    expect(Array.from((q('[data-test="pa-subject-select"]') as HTMLSelectElement).options).some(o => o.value === '')).toBe(false);
    await launchAfterSubjects();
    await waitFor(() => expect(api.admin.startPaperAnalysis).toHaveBeenCalledWith(42, '', undefined));
    expect(api.admin.startPaperAnalysis).not.toHaveBeenCalledWith(42, 'math', undefined);
  });

  it('polls the running analysis every 2s until the report lands', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(runningAnalysis());
    vi.mocked(api.admin.getPaperAnalysis)
      .mockResolvedValueOnce(runningAnalysis({ stage: 'judging', progress: 60 }))
      .mockResolvedValueOnce(doneAnalysis());
    // The manual-fix queue is fetched once the report is in.
    vi.mocked(api.errorItems.list).mockResolvedValue([]);

    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);
    // 先在真实计时器下等 subjects 就绪再点 launch（无学科时按钮 disabled），
    // 之后才切 fake timers 驱动轮询；绝不以空 subject/math 默认值绕过。
    await launchAfterSubjects();
    vi.useFakeTimers();

    // The launch resolves (running) before the poll interval can arm.
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });

    // Two poll ticks: judging → done.
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(api.admin.getPaperAnalysis).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(api.admin.getPaperAnalysis).toHaveBeenCalledTimes(2);

    // The report: stats (OD's four-cell grid), the error mix, the
    // weak-point list, per-question rows.
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(q('[data-test="pa-report"]')).toBeTruthy();
    expect(q('[data-test="pa-error-dist"]')).toBeTruthy();
    expect(q('[data-test="pa-weak-kc"]')).toBeTruthy();
    expect(q('[data-test="pa-q-1"]')).toBeTruthy();
    expect(q('[data-test="pa-q-2"]')).toBeTruthy();
    // The four-cell stat grid: wrong rate, sample, wrong count, corrected.
    const stats = [...document.body.querySelectorAll('[data-test="pa-report-stats"] > div')];
    expect(stats.length).toBe(4);
    expect(stats[0].textContent).toContain('33%');
    expect(stats[1].textContent).toContain('3');
  });

  it('renders the report header, export button, source note and follow-up links', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(doneAnalysis());
    vi.mocked(api.errorItems.list).mockResolvedValue([]);
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    await launchAfterSubjects();

    // The one-line verdict (OD pa-report-head): totals + wrong rate.
    await waitFor(() => expect(q('[data-test="pa-report-head"]')).toBeTruthy());
    expect(q('[data-test="pa-report-head"]').textContent).toContain('3');
    expect(q('[data-test="pa-report-head"]').textContent).toContain('33%');
    // Export (OD pa-export-btn) and the traceability note (pa-source-note).
    expect(q('[data-test="pa-export-btn"]')).toBeTruthy();
    expect(q('[data-test="pa-source-note"]').textContent).toContain('paper analysis');
    // The follow-up links (OD pa-links-card) navigate to dashboard tabs.
    expect(q('[data-test="pa-links-card"]')).toBeTruthy();
    expect(q('[data-test="pa-link-errorbook"]').getAttribute('href')).toBe('#tab=errorbook');
    expect(q('[data-test="pa-link-settings"]').getAttribute('href')).toBe('#tab=settings');
  });

  it('creates a correction task from a wrong question with one tap', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(doneAnalysis());
    vi.mocked(api.errorItems.list).mockResolvedValue([]);
    const onToast = vi.fn();
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={onToast} />);

    await launchAfterSubjects();
    const btn = await waitFor(() => {
      const el = q('[data-test="pa-gen-task"]') as HTMLButtonElement;
      expect(el).toBeTruthy();
      return el;
    });
    vi.mocked(api.admin.createCorrectionTask).mockResolvedValue({} as never);
    fireEvent.click(btn);

    await waitFor(() => expect(api.admin.createCorrectionTask).toHaveBeenCalledWith(17));
    await waitFor(() => expect(onToast).toHaveBeenCalled());
  });

  it('shows the manual-fix queue for items the AI could not judge', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(doneAnalysis());
    vi.mocked(api.errorItems.list).mockResolvedValue([
      {
        id: 9, user_id: 2, subject: 'math',
        original_image_url: '/uploads/x.jpg',
        question_text: '', wrong_answer_text: '', answer_text: '',
        mistake_analysis: '', mistake_status: 'unknown', error_type: '',
        mastery_level: 0,
        created_at: '2026-10-13T00:00:00Z', updated_at: '2026-10-13T00:00:00Z',
        tags: [],
      },
    ]);
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    await launchAfterSubjects();
    await waitFor(() => expect(q('[data-test="pa-manual-card"]')).toBeTruthy());
    // The original photo stays reachable for the parent to read.
    expect(screen.getByRole('img')).toBeTruthy();

    // Saving the fix goes through the parent's correction endpoint.
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue({} as never);
    const textareas = document.body.querySelectorAll('textarea');
    fireEvent.change(textareas[0], { target: { value: '抄写题目' } });
    fireEvent.click(q('[data-test="pa-manual-btn"]'));
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledWith(9, expect.objectContaining({
      question_text: '抄写题目',
    })));
  });

  it('surfaces a failed analysis as an error, not a stuck spinner', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(
      runningAnalysis({ status: 'failed', error: 'vision model unavailable' }));
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    await launchAfterSubjects();
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByText(/vision model unavailable/)).toBeTruthy();
  });
});

// 问题③「打不开了」regression: a pre-037 summary stored questions: null (a
// 0-question parse of a non-exam photo). The report must render the empty
// state, not crash the whole dashboard.
describe('PaperAnalysisModal null-summary tolerance', () => {
  it('renders a done report whose questions/notes are null', async () => {
    vi.mocked(api.admin.getPaperAnalysis).mockResolvedValue(doneAnalysis({
      total_questions: 0,
      wrong_count: 0,
      summary: {
        questions: null,
        error_type_counts: {},
        weak_tags: [],
        notes: null,
      },
    }));
    vi.mocked(api.errorItems.list).mockResolvedValue([]);
    render(<PaperAnalysisModal
      completionId={42}
      initialAnalysis={doneAnalysis({
        total_questions: 0,
        wrong_count: 0,
        summary: { questions: null, error_type_counts: {}, weak_tags: [], notes: null },
      })}
      onClose={vi.fn()}
      onToast={vi.fn()}
    />);
    // The report head renders with zero counts instead of throwing.
    await waitFor(() => expect(q('[data-test="pa-report"]')).toBeTruthy());
    expect(q('[data-test="pa-report-head"]').textContent).toContain('0');
    expect(q('[data-test="pa-error-dist"]')).toBeNull();
  });
});
