// @vitest-environment jsdom
// The parent's mistake-management tab (L1+): the stat strip, week scope,
// subject chips + mastery filters, and the expandable wrong-question rows
// with fix/delete/makeTask. The measures (034) live in their own tab.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';

// The API client is mocked at the module level: the tab must only talk to
// users.list / errorItems.list / chores.list / admin.deleteErrorItem /
// admin.createCorrectionTask.
const { api } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    errorItems: { list: vi.fn() },
    chores: { list: vi.fn() },
    admin: {
      deleteErrorItem: vi.fn(),
      createCorrectionTask: vi.fn(),
    },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { ErrorItem, User } from '../../types';
import { ErrorbookTab } from './ErrorbookTab';

const kid = (over: Partial<User> = {}): User => ({
  ...(over as User),
  id: 2,
  name: 'Mika',
  role: 'child',
} as User);

const item = (over: Partial<ErrorItem> = {}): ErrorItem => ({
  id: 31,
  user_id: 2,
  subject: 'math',
  original_image_url: '/f/1.jpg',
  question_text: 'x² − 4 = 0, x = ?',
  answer_text: 'x = ±2',
  wrong_answer_text: 'x = 2',
  mistake_analysis: 'Missed the negative root',
  mistake_status: 'wrong_attempt',
  error_type: 'concept',
  mastery_level: 0,
  source: '期中卷分析',
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
  tags: [],
  ...over,
} as ErrorItem);

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

// 任务②：错题→订正任务关联（external_id=error_item:<id>）是页内一次
// chores.list 拉取建的映射——每个用例默认空映射（无关联任务）。
beforeEach(() => {
  vi.mocked(api.chores.list).mockResolvedValue([] as never);
});

describe('ErrorbookTab', () => {
  it('shows the stats, week scope and the questions with subject chips', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, subject: 'math', mastery_level: 0 }),
      item({ id: 32, subject: 'math', mastery_level: 2 }),
      item({ id: 33, subject: 'english', mastery_level: 1, mistake_status: 'unknown' }),
    ]);

    const { container } = render(<ErrorbookTab />);

    // The rows arrive.
    await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"]');
      expect(el).toBeTruthy();
    });
    // Three questions; the cause span carries pe-item-<id>-cause and the
    // date row pe-item-<id>-date so the prefix selector hits root + verdict
    // + date per row.
    expect(container.querySelectorAll('[data-test^="pe-item-"]').length).toBe(9);
    expect(q(container, '[data-test="pe-filter-subject-math"]').textContent).toContain('2');
    expect(q(container, '[data-test="pe-filter-subject-english"]').textContent).toContain('1');
    // The mastery filters are all present.
    expect(q(container, '[data-test="pe-filter-new"]')).toBeTruthy();
    expect(q(container, '[data-test="pe-filter-reviewing"]')).toBeTruthy();
    expect(q(container, '[data-test="pe-filter-mastered"]')).toBeTruthy();
    // The week scope shows this week's three questions.
    expect(q(container, '[data-test="pa-week-scope"]').textContent).toContain('3');
  });

  it('expands a row: the AI note, the tags, and makeTask', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [{ id: 7, name: 'quadratic', subject: 'math', sort_order: 1, is_system: true }] }),
    ]);
    vi.mocked(api.admin.createCorrectionTask).mockResolvedValue({} as never);

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));

    // The expanded body: the mistake analysis and the knowledge tags.
    await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31-analysis"]');
      expect(el).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-item-31-analysis"]').textContent).toContain('Missed the negative root');
    expect(q(container, '[data-test="pe-item-31"]').textContent).toContain('quadratic');

    // makeTask calls the correction-task endpoint.
    fireEvent.click(q(container, '[data-test="pe-item-31"] button[data-test="pe-make-task"]') as HTMLButtonElement);
    await waitFor(() => expect(api.admin.createCorrectionTask).toHaveBeenCalledWith(31));
  });

  it('filters by mastery', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, mastery_level: 0 }),
      item({ id: 32, mastery_level: 2 }),
    ]);

    const { container } = render(<ErrorbookTab />);
    await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"]');
      expect(el).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-filter-mastered"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31"]')).toBeNull();
    });
    expect(q(container, '[data-test="pe-item-32"]')).toBeTruthy();
  });

  it('shows the empty state when the book is empty', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([]);

    const { container } = render(<ErrorbookTab />);
    await waitFor(() => {
      const el = container.querySelector('[data-test="pe-empty"]');
      expect(el).toBeTruthy();
    });
  });

  it('filters by error cause (the 讯飞 third axis)', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, error_type: 'concept' }),
      item({ id: 32, error_type: 'calculation' }),
    ]);

    const { container } = render(<ErrorbookTab />);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31"]')).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-filter-etype-concept"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-32"]')).toBeNull();
    });
    expect(q(container, '[data-test="pe-item-31"]')).toBeTruthy();

    // Back to all via the "all" chip.
    fireEvent.click(q(container, '[data-test="pe-filter-etype-all"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-32"]')).toBeTruthy();
    });
  });

  it('任务②: shows the linked correction task and origin line on live-shaped data', async () => {
    // live 存量形状：errored_date/chore_title 全空，只有 created_at；
    // 关联任务走 chores.external_id=error_item:<id>。
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, errored_date: undefined, chore_title: undefined, created_at: '2026-10-13T22:01:29Z' }),
    ]);
    vi.mocked(api.chores.list).mockResolvedValue([
      { id: 14, title: '订正错题 解方程：3x+6=18', external_id: 'error_item:31' },
    ] as never);

    const { container } = render(<ErrorbookTab />);
    // 行头日期恒显示（created_at 回退，不再是 errored_date 才有）。
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31-date"]')).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    // 展开体：来历行 + 已关联订正任务行 + makeTask 直接呈已挂状态。
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31-origin"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-item-31-task"]').textContent).toContain('订正错题 解方程：3x+6=18');
    expect((q(container, '[data-test="pe-make-task"]') as HTMLButtonElement).disabled).toBe(true);
    expect(q(container, '[data-test="pe-item-31-origin"]').textContent).toContain('2026-10-13');
  });
});
