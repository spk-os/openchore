// @vitest-environment jsdom
// PaperAnalysisModal (L1, parent side): the two-stage paper analysis flow
// behind the approval card — subject pick + launch, the 2s poll until
// done, the diagnosis report (stats / error mix / per-question rows) and
// the one-tap correction task from a wrong question.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react';
import i18n from '../../i18n';
import type { PaperAnalysis } from '../../types';

// The API client is mocked at the module level: the modal must only talk
// to api.admin.startPaperAnalysis / getPaperAnalysis / createCorrectionTask
// and api.errorItems.list (the manual-fix queue).
const { api } = vi.hoisted(() => ({
  api: {
    admin: {
      startPaperAnalysis: vi.fn(),
      getPaperAnalysis: vi.fn(),
      createCorrectionTask: vi.fn(),
      updateErrorItem: vi.fn(),
    },
    errorItems: { list: vi.fn() },
  },
}));
vi.mock('../../api', () => ({ api }));

import { PaperAnalysisModal } from './PaperAnalysisCard';

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

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); vi.useRealTimers(); });

// The components use data-test (not data-testid): query through the body.
const q = (sel: string) => document.body.querySelector(sel)!;

describe('PaperAnalysisModal', () => {
  it('launches the analysis with the picked subject', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(runningAnalysis());
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);

    const subject = screen.getByLabelText(/Subject/);
    fireEvent.change(subject, { target: { value: 'physics' } });
    fireEvent.click(screen.getByRole('button', { name: /AI paper analysis/i }));

    await waitFor(() => expect(api.admin.startPaperAnalysis).toHaveBeenCalledWith(42, 'physics'));
    // The progress rail replaces the launch form while running.
    await waitFor(() => expect(q('[data-test="pa-progress"]')).toBeTruthy());
    expect(q('[data-test="pa-steps"]')).toBeTruthy();
  });

  it('polls the running analysis every 2s until the report lands', async () => {
    vi.useFakeTimers();
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(runningAnalysis());
    vi.mocked(api.admin.getPaperAnalysis)
      .mockResolvedValueOnce(runningAnalysis({ stage: 'judging', progress: 60 }))
      .mockResolvedValueOnce(doneAnalysis());
    // The manual-fix queue is fetched once the report is in.
    vi.mocked(api.errorItems.list).mockResolvedValue([]);

    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={vi.fn()} />);
    fireEvent.click(screen.getByRole('button', { name: /AI paper analysis/i }));
    // The launch resolves (running) before the poll interval can arm.
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });

    // Two poll ticks: judging → done.
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(api.admin.getPaperAnalysis).toHaveBeenCalledTimes(1);
    await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
    expect(api.admin.getPaperAnalysis).toHaveBeenCalledTimes(2);

    // The report: stats, the error mix, the weak-point list, per-question rows.
    await act(async () => { await vi.advanceTimersByTimeAsync(0); });
    expect(q('[data-test="pa-report"]')).toBeTruthy();
    expect(q('[data-test="pa-error-dist"]')).toBeTruthy();
    expect(q('[data-test="pa-weak-kc"]')).toBeTruthy();
    expect(q('[data-test="pa-q-1"]')).toBeTruthy();
    expect(q('[data-test="pa-q-2"]')).toBeTruthy();
  });

  it('creates a correction task from a wrong question with one tap', async () => {
    vi.mocked(api.admin.startPaperAnalysis).mockResolvedValue(doneAnalysis());
    vi.mocked(api.errorItems.list).mockResolvedValue([]);
    const onToast = vi.fn();
    render(<PaperAnalysisModal completionId={42} onClose={vi.fn()} onToast={onToast} />);

    fireEvent.click(screen.getByRole('button', { name: /AI paper analysis/i }));
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

    fireEvent.click(screen.getByRole('button', { name: /AI paper analysis/i }));
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

    fireEvent.click(screen.getByRole('button', { name: /AI paper analysis/i }));
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(screen.getByText(/vision model unavailable/)).toBeTruthy();
  });
});
