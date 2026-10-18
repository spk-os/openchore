// @vitest-environment jsdom
// 036: the parent-side media library tab — subject/class chips with live
// counts, the kid filter, the clear button, the empty state. The API
// client is mocked at the module level: the tab must only talk to
// users.list / chores.listMedia.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';

const { api } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    chores: { listMedia: vi.fn() },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { MediaLibraryResponse, MediaItem, User } from '../../types';
import { MediaTab } from './MediaTab';

const kid = (over: Partial<User> = {}): User => ({
  ...(over as User),
  id: 2,
  name: 'Mika',
  role: 'child',
} as User);

const item = (over: Partial<MediaItem> = {}): MediaItem => ({
  attachment_id: 1,
  completion_id: 1,
  file_path: '/uploads/a1.jpg',
  display_name: 'page1.jpg',
  mime_type: 'image/jpeg',
  mime_class: 'image',
  size_bytes: 2048,
  created_at: '2026-10-17T10:00:00Z',
  chore_id: 1,
  chore_title: 'Math workbook',
  subject: 'math',
  category: 'core',
  user_id: 2,
  user_name: 'Mika',
  status: 'pending',
  ...over,
} as MediaItem);

const library = (over: Partial<MediaLibraryResponse> = {}): MediaLibraryResponse => ({
  items: [],
  subjects: [],
  classes: [],
  total: 0,
  ...over,
});

const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('MediaTab', () => {
  it('renders the grid with counts on every chip', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [
        item({ attachment_id: 1, subject: 'math', mime_class: 'image' }),
        item({ attachment_id: 2, subject: 'math', mime_class: 'video', file_path: '/uploads/a2.mp4', display_name: 'demo.mp4' }),
        item({ attachment_id: 3, subject: '', mime_class: 'audio', chore_id: 3, chore_title: 'Dishes' }),
      ],
      subjects: [{ subject: 'math', count: 2 }, { subject: '', count: 1 }],
      classes: [
        { mime_class: 'image', count: 1 },
        { mime_class: 'video', count: 1 },
        { mime_class: 'audio', count: 1 },
      ],
      total: 3,
    }));

    const { container } = render(<MediaTab />);

    // Three cells once loaded.
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(3);
    });
    // Subject chip math shows its live count 2.
    expect(q(container, '[data-test="media-subject-math"]').textContent).toContain('2');
    // Class chips show their counts.
    expect(q(container, '[data-test="media-class-video"]').textContent).toContain('1');
    expect(q(container, '[data-test="media-class-audio"]').textContent).toContain('1');
    // The AI badge renders for reviewed items only (none here).
    expect(container.querySelector('[class*="aiBadge"]')).toBeNull();
  });

  it('filters by subject and class through the API', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      subjects: [{ subject: 'math', count: 2 }],
      classes: [{ mime_class: 'audio', count: 1 }],
      total: 2,
    }));

    const { container } = render(<MediaTab />);
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenCalled());

    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="media-subject-math"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenLastCalledWith(expect.objectContaining({ subject: 'math' })));

    fireEvent.click(q(container, '[data-test="media-class-audio"]'));
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenLastCalledWith(
      expect.objectContaining({ subject: 'math', cls: 'audio' })));
  });

  it('clears every filter with the clear button', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item()],
      subjects: [{ subject: 'math', count: 1 }],
      total: 1,
    }));

    const { container } = render(<MediaTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="media-subject-math"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenLastCalledWith(expect.objectContaining({ subject: 'math' })));

    // The clear button only exists while a filter is on.
    const clear = await waitFor(() => {
      const el = container.querySelector('[data-test="media-clear"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    fireEvent.click(clear);
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenLastCalledWith(
      expect.objectContaining({ subject: '', cls: '', from: '', to: '' })));
  });

  it('shows the empty state when the library has nothing', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library());

    const { container } = render(<MediaTab />);
    await waitFor(() => {
      expect(container.querySelector('[data-test="media-grid"]')).toBeNull();
    });
    // The i18n empty copy is up (en).
    expect(container.textContent).toContain('No photos, videos or recordings yet');
  });

  it('marks reviewed items with the AI badge', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item({ ai_complete: true, ai_confidence: 0.92, ai_feedback: 'Read the whole poem.' })],
      total: 1,
    }));

    const { container } = render(<MediaTab />);
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1);
    });
    const badge = container.querySelector('[class*="aiBadge"]');
    expect(badge).toBeTruthy();
    expect(badge!.textContent).toContain('92%');
  });

  it('always renders all six subject chips, zero counts included', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    // Only math has content: the other four subjects and unclassified
    // still render at count 0 — a missing chip reads as "没有做分类".
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item()],
      subjects: [{ subject: 'math', count: 1 }],
      total: 1,
    }));

    const { container } = render(<MediaTab />);
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1);
    });
    for (const subj of ['math', 'chinese', 'english', 'physics', 'chemistry']) {
      const chipEl = container.querySelector(`[data-test="media-subject-${subj}"]`);
      expect(chipEl, `subject chip ${subj} should always render`).toBeTruthy();
      expect(chipEl!.textContent).toContain(subj === 'math' ? '1' : '0');
    }
    // The unclassified chip has no data-test — it is the last chip of the
    // subject row after the five; find it by its en label.
    expect(container.textContent).toContain('Unclassified');
  });

  it('opens the in-page preview instead of navigating on cell click', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      // A video: the old bare <a href> opened the raw file in a new tab
      // (the "点视频直接报错" report); the cell is now a button that opens
      // the in-page player.
      items: [item({ mime_class: 'video', mime_type: 'video/mp4', file_path: '/uploads/a2.mp4', display_name: 'demo.mp4' })],
      total: 1,
    }));

    const { container } = render(<MediaTab />);
    const cell = await waitFor(() => {
      const el = container.querySelector('[data-test="media-item"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    // The cell must no longer be a raw link.
    expect((cell as HTMLAnchorElement).tagName).not.toBe('A');
    fireEvent.click(cell);
    // The modal portals to document.body, not the render container.
    const preview = await waitFor(() => {
      const el = document.querySelector('[data-test="media-preview"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(preview.querySelector('video')).toBeTruthy();
    expect(preview.querySelector('video')!.getAttribute('src')).toBe('/uploads/a2.mp4');
  });
});
