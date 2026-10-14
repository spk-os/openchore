// @vitest-environment jsdom
// The kid's spaced-repetition session (L4): the live queue renders one
// card at a time, flip reveals the two honest buttons, a grade reloads
// the queue, and finishing everything shows the completion banner.
// The Today entry card hides itself when nothing is due.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';
import { MemoryRouter } from 'react-router-dom';

// The API client is mocked at the module level: the screen must only
// talk to api.review.today / api.review.grade.
const { api } = vi.hoisted(() => ({
  api: { review: { today: vi.fn(), grade: vi.fn() } },
}));
vi.mock('../../api', () => ({ api }));

import type { ReviewItem, ReviewToday } from '../../types';
import { ReviewScreen } from './ReviewScreen';
import { ReviewEntryCard } from './ReviewEntryCard';

const card = (over: Partial<ReviewItem>): ReviewItem => ({
  id: 1,
  user_id: 2,
  source_type: 'error_item',
  source_id: 11,
  front_text: '$3+5=?$',
  back_text: '$8$\n\nAdd the ones first.',
  subject: 'math',
  due: '2026-10-14T08:00:00Z',
  stability: 0,
  difficulty: 5,
  scheduled_days: 0,
  reps: 0,
  lapses: 0,
  state: 'New',
  remaining_steps: 0,
  suspended: false,
  created_at: '2026-10-14T00:00:00Z',
  source_label: 'Midterm · Q3',
  ...over,
});

const today = (over: Partial<ReviewToday>): ReviewToday => ({
  due: [],
  upcoming: [],
  streak: 0,
  ...over,
});

const topBar = <div data-test="topbar" />;

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('ReviewScreen', () => {
  it('renders the current card with progress and the honest note', async () => {
    vi.mocked(api.review.today).mockResolvedValue(today({
      due: [card({ id: 7 })],
      upcoming: [card({ id: 8, front_text: 'A later question', due: '2026-10-17T08:00:00Z' })],
      streak: 3,
    }));
    const { container } = render(<MemoryRouter><ReviewScreen topBar={topBar} /></MemoryRouter>);

    // The card front renders (KaTeX wraps the formula, so wait on the
    // data-test hooks instead of the text).
    const flip = await waitFor(() => {
      const el = container.querySelector('[data-test="rv-flip"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    expect(q(container, '[data-test="rv-progress-num"]').textContent).toBe('0 / 1 cards');
    // The streak from the queue response surfaces in the ETA line.
    expect(q(container, '[data-test="rv-eta"]').textContent).toContain('3');
    // The honest note sits under the card.
    expect(q(container, '[data-test="rv-honest-note"]')).toBeTruthy();
    // The upcoming preview lists the scheduled card.
    expect(q(container, '[data-test="rv-queue"]')).toBeTruthy();

    // Flip: the answer and the two buttons appear.
    fireEvent.click(flip);
    expect(q(container, '[data-test="rv-back"]')).toBeTruthy();
    expect(q(container, '[data-test="rv-again"]')).toBeTruthy();
    expect(q(container, '[data-test="rv-good"]')).toBeTruthy();
    expect(api.review.today).toHaveBeenCalledOnce();
  });

  it('grades a card and reloads the live queue', async () => {
    vi.mocked(api.review.today)
      .mockResolvedValueOnce(today({ due: [card({ id: 7 }), card({ id: 8 })] }))
      .mockResolvedValueOnce(today({ due: [card({ id: 8 })], streak: 1 }));
    vi.mocked(api.review.grade).mockResolvedValue(card({ id: 7, state: 'Learning' }));
    const { container } = render(<MemoryRouter><ReviewScreen topBar={topBar} /></MemoryRouter>);

    const flip = await waitFor(() => {
      const el = container.querySelector('[data-test="rv-flip"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    fireEvent.click(flip);
    fireEvent.click(q(container, '[data-test="rv-good"]'));
    await waitFor(() => expect(api.review.grade).toHaveBeenCalledWith(7, 'Good'));
    // The queue reloads: the next card is 8 and the progress moved.
    await waitFor(() => expect(q(container, '[data-test="rv-progress-num"]').textContent).toBe('1 / 2 cards'));
  });

  it('shows the completion banner when the queue is drained', async () => {
    vi.mocked(api.review.today)
      .mockResolvedValueOnce(today({ due: [card({ id: 7 })], streak: 2 }))
      .mockResolvedValueOnce(today({ due: [], streak: 2 }));
    vi.mocked(api.review.grade).mockResolvedValue(card({ id: 7 }));
    const { container } = render(<MemoryRouter><ReviewScreen topBar={topBar} /></MemoryRouter>);

    const flip = await waitFor(() => {
      const el = container.querySelector('[data-test="rv-flip"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    fireEvent.click(flip);
    fireEvent.click(q(container, '[data-test="rv-again"]'));
    await waitFor(() => expect(q(container, '[data-test="rv-done"]').getAttribute('role')).toBe('status'));
    expect(q(container, '[data-test="rv-done-title"]').textContent).toContain('+6');
    expect(q(container, '[data-test="rv-streak"]').textContent).toContain('2');
  });

  it('shows the empty state when nothing is due', async () => {
    vi.mocked(api.review.today).mockResolvedValue(today({}));
    const { container } = render(<MemoryRouter><ReviewScreen topBar={topBar} /></MemoryRouter>);
    await waitFor(() => expect(q(container, '[data-test="rv-empty"]')).toBeTruthy());
    expect(screen.getByText(/created from your wrong questions/)).toBeTruthy();
  });

  it('keeps the error state visible on a load failure', async () => {
    vi.mocked(api.review.today).mockRejectedValue(new Error('offline'));
    render(<MemoryRouter><ReviewScreen topBar={topBar} /></MemoryRouter>);
    expect(await screen.findByRole('alert')).toBeTruthy();
  });
});

describe('ReviewEntryCard', () => {
  it('shows the entry with the due count when cards are due', async () => {
    vi.mocked(api.review.today).mockResolvedValue(today({ due: [card({ id: 1 }), card({ id: 2 })] }));
    const { container } = render(<MemoryRouter><ReviewEntryCard /></MemoryRouter>);
    const entry = await waitFor(() => {
      const el = container.querySelector('[data-test="review-entry"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(entry.getAttribute('href')).toBe('/review');
    expect(q(container, '[data-test="review-due-count"]').textContent).toContain('2');
  });

  it('hides entirely when nothing is due', async () => {
    vi.mocked(api.review.today).mockResolvedValue(today({ due: [] }));
    const { container } = render(<MemoryRouter><ReviewEntryCard /></MemoryRouter>);
    // Resolve the fetch, then the card must render nothing.
    await waitFor(() => expect(api.review.today).toHaveBeenCalledOnce());
    await new Promise(r => setTimeout(r, 0));
    expect(container.querySelector('[data-test="review-entry"]')).toBeNull();
  });
});
