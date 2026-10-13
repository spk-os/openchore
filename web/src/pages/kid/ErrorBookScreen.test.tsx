// @vitest-environment jsdom
// The kid's wrong-question book (L1): list with subject filters, the
// three-state mastery badge, and the "我订正完了" retry that advances
// New → Reviewing → Mastered through the API.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';
import { MemoryRouter } from 'react-router-dom';

// The API client is mocked at the module level: the screen must only talk
// to api.errorItems.list / api.errorItems.submitReviewResult.
const { api } = vi.hoisted(() => ({
  api: { errorItems: { list: vi.fn(), submitReviewResult: vi.fn() } },
}));
vi.mock('../../api', () => ({ api }));

import type { ErrorItem } from '../../types';
import { ErrorBookScreen } from './ErrorBookScreen';

const item = (over: Partial<ErrorItem>): ErrorItem => ({
  id: 17,
  user_id: 2,
  subject: 'math',
  original_image_url: '/uploads/p.jpg',
  question_text: '解方程 $x+2=5$',
  wrong_answer_text: '$6$',
  answer_text: '$3$',
  mistake_analysis: '移项后符号没变。',
  mistake_status: 'wrong_attempt',
  error_type: 'calculation',
  mastery_level: 0,
  created_at: '2026-10-12T00:00:00Z',
  updated_at: '2026-10-12T00:00:00Z',
  tags: [{ id: 1, name: '一元一次方程', subject: 'math', sort_order: 0, is_system: false }],
  ...over,
});

const topBar = <div data-test="topbar" />;

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;
const qAll = (root: HTMLElement, sel: string) => [...root.querySelectorAll(sel)];

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('ErrorBookScreen', () => {
  it('renders the book with filters and mastery badges', async () => {
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 1, mastery_level: 0 }),
      item({ id: 2, subject: 'english', mastery_level: 1 }),
      item({ id: 3, subject: 'english', mastery_level: 2 }),
    ]);
    const { container } = render(<MemoryRouter><ErrorBookScreen topBar={topBar} /></MemoryRouter>);

    // KaTeX renders the LaTeX, so wait on the card list instead. The
    // selector hits both the card root and its header, hence 2 per card.
    await waitFor(() => expect(qAll(container, '[data-test^="eb-card-"]').length).toBe(6));
    // Filters: all + one per subject with counts.
    expect(q(container, '[data-test="eb-filter-all"]')).toBeTruthy();
    expect(q(container, '[data-test="eb-filter-math"]')).toBeTruthy();
    expect(q(container, '[data-test="eb-filter-english"]')).toBeTruthy();
    // The three mastery states each render their badge.
    expect(q(container, '[data-test="eb-mastery-new"]')).toBeTruthy();
    expect(q(container, '[data-test="eb-mastery-reviewing"]')).toBeTruthy();
    expect(q(container, '[data-test="eb-mastery-mastered"]')).toBeTruthy();
    // The AI explanation card and the kid's own answer are both shown.
    expect(q(container, '[data-test="eb-ai-card"]')).toBeTruthy();
    expect(qAll(container, 'span').filter(el => el.textContent === 'You wrote:').length).toBe(3);
    expect(api.errorItems.list).toHaveBeenCalledOnce();
  });

  it('filters by subject on tap', async () => {
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 1 })]);
    const { container } = render(<MemoryRouter><ErrorBookScreen topBar={topBar} /></MemoryRouter>);
    const chip = await waitFor(() => {
      const el = container.querySelector('[data-test="eb-filter-math"]');
      expect(el).toBeTruthy();
      return el!;
    });
    fireEvent.click(chip);
    await waitFor(() => expect(api.errorItems.list).toHaveBeenLastCalledWith({ subject: 'math' }));
  });

  it('advances mastery when the kid reports a correct retry', async () => {
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 1, mastery_level: 0 })]);
    vi.mocked(api.errorItems.submitReviewResult).mockResolvedValue(
      item({ id: 1, mastery_level: 1 }));
    const { container } = render(<MemoryRouter><ErrorBookScreen topBar={topBar} /></MemoryRouter>);

    const done = await waitFor(() => {
      const el = container.querySelector('[data-test="eb-done-btn"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    fireEvent.click(done);
    await waitFor(() => expect(api.errorItems.submitReviewResult).toHaveBeenCalledWith(1, true));
    // The card re-renders in the Reviewing state.
    await waitFor(() => expect(q(container, '[data-test="eb-mastery-reviewing"]')).toBeTruthy());
  });

  it('shows the empty state when the book has nothing', async () => {
    vi.mocked(api.errorItems.list).mockResolvedValue([]);
    const { container } = render(<MemoryRouter><ErrorBookScreen topBar={topBar} /></MemoryRouter>);
    await waitFor(() => expect(q(container, '[data-test="eb-empty"]')).toBeTruthy());
    expect(screen.getByText(/Mistakes will appear here automatically/)).toBeTruthy();
  });

  it('keeps the list on a load failure with a retry note', async () => {
    vi.mocked(api.errorItems.list).mockRejectedValue(new Error('offline'));
    render(<MemoryRouter><ErrorBookScreen topBar={topBar} /></MemoryRouter>);
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(qAll(document.body, '[data-test="eb-empty"]').length).toBe(0);
  });
});
