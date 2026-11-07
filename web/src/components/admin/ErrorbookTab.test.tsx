// @vitest-environment jsdom
// The parent's mistake-management tab (L1+): the stat strip, week scope,
// subject chips + mastery filters, and the expandable wrong-question rows
// with fix/delete/makeTask. The measures (034) live in their own tab.
import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup, act } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import i18n from '../../i18n';

// The API client is mocked at the module level: the tab must only talk to
// users.list / errorItems.list / chores.list / admin.deleteErrorItem /
// admin.createCorrectionTask / admin.updateErrorItem, plus the tag editor's
// candidate source taxonomyApi.topics (system tags of the item's subject).
const { api, taxonomyApi } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    errorItems: { list: vi.fn() },
    chores: { list: vi.fn() },
    admin: {
      deleteErrorItem: vi.fn(),
      createCorrectionTask: vi.fn(),
      updateErrorItem: vi.fn(),
    },
  },
  taxonomyApi: { topics: vi.fn() },
}));
vi.mock('../../api', () => ({ api }));
vi.mock('../../taxonomy/client', () => ({ taxonomyApi }));

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
// 知识标签编辑器候选源（taxonomyApi.topics）默认返回空页，标签用例里覆写。
beforeEach(() => {
  vi.mocked(api.chores.list).mockResolvedValue([] as never);
  taxonomyApi.topics.mockReset();
  taxonomyApi.topics.mockResolvedValue({ topics: [], next_cursor: null });
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

  // ---- 知识标签可见可编辑（卡 DEF-ERRORBOOK-TAG-UI-R2-CODER）----

  /** 系统标签候选行（BrowseTopics 行：id 是 JSON 数字，前端 Number() 归一）。 */
  const topicRow = (over: Partial<{ id: number; external_ref: string; name: string; subject: string }> = {}) => ({
    id: 101,
    external_ref: 'cnm_p101',
    name: 'quadratic equations',
    grade: 8,
    semester: 1,
    domain: 'algebra',
    chapter: 'Ch.2',
    subject: 'math',
    ...over,
  });

  it('知识标签: shows system + custom tag names and the edit entry', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({
        id: 31,
        tags: [
          // 自定义标签（is_system=0）：BrowseTopics 不返回，但必须显示名称。
          { id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false },
        ],
      }),
    ]);

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));

    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31-tagline"]')).toBeTruthy();
    });
    // 展示的是标签名称（不是本机 ID）。
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('my custom point');
    // 编辑入口可见。
    expect(q(container, '[data-test="pe-tags-edit-31"]')).toBeTruthy();
  });

  it('知识标签: edits and saves the body via updateErrorItem, adopting the fresh response', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    // 候选：同学科两条系统标签。
    taxonomyApi.topics.mockResolvedValue({
      topics: [topicRow({ id: 101, name: 'factorization' }), topicRow({ id: 102, external_ref: 'cnm_p102', name: 'quadratic formula' })],
      next_cursor: null,
    });
    // 服务端保存后重读的最新 item（tags 已 hydrate）。
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue(
      item({ id: 31, tags: [{ id: 101, name: 'factorization', subject: 'math', sort_order: 1, is_system: true }] }) as never,
    );

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));

    // 打开标签编辑器：候选按 item.subject 加载。
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    expect(taxonomyApi.topics).toHaveBeenCalledWith(expect.objectContaining({ subject: 'math' }));

    // 勾选一条系统标签 + 保存。
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));

    // body 只带 knowledge_tag_ids（不碰其它字段/FSRS）。
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1));
    expect(api.admin.updateErrorItem).toHaveBeenCalledWith(31, { knowledge_tag_ids: [101] });

    // 成功后采用响应的最新 tags（不是本地拼凑），编辑器关闭。
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    await waitFor(() => {
      expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('factorization');
    });
  });

  it('知识标签: clears the tags (knowledge_tag_ids: [])', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [{ id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: [], next_cursor: null });
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue(item({ id: 31, tags: [] }) as never);

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeTruthy();
    });

    // 清空 → 保存：body 发 knowledge_tag_ids: []。
    fireEvent.click(q(container, '[data-test="pe-tags-clear-31"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledWith(31, { knowledge_tag_ids: [] }));
  });

  it('知识标签: a failed save keeps the edit open with a visible alert', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101 })], next_cursor: null });
    vi.mocked(api.admin.updateErrorItem).mockRejectedValueOnce(new Error('400'));

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));

    // 失败：编辑器保留 + role=alert 错误可见 + 勾选不丢。
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-save-error-31"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-tags-save-error-31"]').getAttribute('role')).toBe('alert');
    expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeTruthy();
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
  });

  it('知识标签: candidates load per subject; cross-subject legacy tags stay out of the selection', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    // 学科变化的错题：english 题上挂着 math 的遗留标签（服务端校验会拒）。
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({
        id: 31,
        subject: 'english',
        tags: [{ id: 9, name: 'math legacy tag', subject: 'math', sort_order: 0, is_system: true }],
      }),
    ]);
    taxonomyApi.topics.mockResolvedValue({
      topics: [topicRow({ id: 201, external_ref: 'eng_p201', name: 'past tense', subject: 'english' })],
      next_cursor: null,
    });

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));

    // 候选按最终 subject（english）请求。
    await waitFor(() => {
      expect(taxonomyApi.topics).toHaveBeenCalledWith(expect.objectContaining({ subject: 'english' }));
    });
    // english 候选可见；math 遗留标签不进初始选中（不在候选池里出现为可选项）。
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-201"]')).toBeTruthy();
    });
    expect(container.querySelector('[data-test="pe-tag-31-9"]')).toBeNull();
    expect(q(container, '[data-test="pe-tag-31-201"]').getAttribute('aria-pressed')).toBe('false');
  });

  it('知识标签: the save button is busy-guarded while the PUT is in flight', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101 })], next_cursor: null });
    // 挂起的保存：断言 busy 期间按钮 disabled。
    let resolveSave: (v: unknown) => void = () => {};
    vi.mocked(api.admin.updateErrorItem).mockImplementation(
      () => new Promise(resolve => { resolveSave = resolve; }),
    );

    const { container } = render(<ErrorbookTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    const saveBtn = q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement;
    fireEvent.click(saveBtn);

    // busy：保存/清空/取消都 disabled，重复点击无效。
    expect((q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement).disabled).toBe(true);
    expect((q(container, '[data-test="pe-tags-clear-31"]') as HTMLButtonElement).disabled).toBe(true);
    expect((q(container, '[data-test="pe-tags-cancel-31"]') as HTMLButtonElement).disabled).toBe(true);
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);

    // 落定：成功保存后编辑器关闭（保存按钮随面板卸载），行内出现
    // 服务端响应的最新标签；busy 结束的可见证据是编辑入口重新出现。
    resolveSave(item({ id: 31, tags: [{ id: 101, name: 'quadratic equations', subject: 'math', sort_order: 1, is_system: true }] }));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    expect(q(container, '[data-test="pe-tags-edit-31"]')).toBeTruthy();
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('quadratic equations');
  });

  // ---- R2 tester 独立补缺（不复用 coder 的 12 条结论）----

  /** 单条错题 + 展开 + 打开标签编辑器。 */
  const openEditor = async (container: HTMLElement) => {
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-item-31"] button');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pe-tags-edit-31"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    }));
  };

  it('知识标签: renders the real tag names (not ids), one chip per system tag', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({
        id: 31,
        tags: [
          { id: 101, name: 'factorization', subject: 'math', sort_order: 1, is_system: true },
          { id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false },
        ],
      }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeTruthy();
    });

    const chip101 = q(container, '[data-test="pe-tag-31-101"]');
    const chip9 = q(container, '[data-test="pe-tag-31-9"]');
    expect(chip101.textContent).toBe('factorization');
    expect(chip9.textContent).toBe('my custom point');
    // 已挂标签进选中态；候选与自带标签按 id 去重，不出现两颗 101。
    expect(chip101.getAttribute('aria-pressed')).toBe('true');
    expect(chip9.getAttribute('aria-pressed')).toBe('true');
    expect(container.querySelectorAll('[data-test="pe-tag-31-101"]').length).toBe(1);
  });

  it('知识标签: opening the editor alone never PUTs (缺省不修改)', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [{ id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101 })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    // 勾一下再取消 = 回到打开时的值，也不算一次修改。
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-cancel-31"]'));
    // makeTask / delete 这些无关按钮也不得夹带标签字段。
    await new Promise(r => setTimeout(r, 0));
    expect(api.admin.updateErrorItem).not.toHaveBeenCalled();
  });

  it('知识标签: non-empty save replaces with exactly the same-subject picked ids', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [{ id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({
      topics: [topicRow({ id: 101, name: 'factorization' }), topicRow({ id: 102, external_ref: 'cnm_p102', name: 'quadratic formula' })],
      next_cursor: null,
    });
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue(
      item({
        id: 31,
        tags: [
          { id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false },
          { id: 102, name: 'quadratic formula', subject: 'math', sort_order: 1, is_system: true },
        ],
      }) as never,
    );

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-102"]')).toBeTruthy();
    });

    // 勾新 + 取消勾旧自定义 → 保存：替换而不是追加。
    fireEvent.click(q(container, '[data-test="pe-tag-31-102"]'));
    fireEvent.click(q(container, '[data-test="pe-tag-31-9"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));

    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1));
    const [id, body] = vi.mocked(api.admin.updateErrorItem).mock.calls[0] as [number, Record<string, unknown>];
    expect(id).toBe(31);
    // body 只允许 knowledge_tag_ids 一个键（不夹带错题字段）。
    expect(Object.keys(body)).toEqual(['knowledge_tag_ids']);
    expect([...(body.knowledge_tag_ids as number[])].sort()).toEqual([102]);
  });

  it('知识标签: cancel restores the server value and discards the local edits', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [{ id: 9, name: 'my custom point', subject: 'math', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });

    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(q(container, '[data-test="pe-tags-cancel-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    // 行内仍是服务端的原标签；重新打开编辑器：只有原标签选中。
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('my custom point');
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).not.toContain('factorization');
    fireEvent.click(q(container, '[data-test="pe-tags-edit-31"]'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('false');
    expect(q(container, '[data-test="pe-tag-31-9"]').getAttribute('aria-pressed')).toBe('true');
    expect(api.admin.updateErrorItem).not.toHaveBeenCalled();
  });

  it('知识标签: busy guard also swallows keyboard activation of a second save', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101 })], next_cursor: null });
    // 每次 save 都挂起：若缺 busy 闸门，第二次 PUT 一定会踏进来。
    const puts: Array<(v: unknown) => void> = [];
    vi.mocked(api.admin.updateErrorItem).mockImplementation(
      () => new Promise(resolve => { puts.push(resolve as (v: unknown) => void); }),
    );

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));

    // 鼠标连点 + 键盘 Enter 都打不进第二次 PUT。
    const saveBtn = q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement;
    fireEvent.click(saveBtn);
    fireEvent.click(saveBtn);
    saveBtn.focus();
    fireEvent.keyDown(saveBtn, { key: 'Enter', code: 'Enter' });
    fireEvent.keyUp(saveBtn, { key: 'Enter', code: 'Enter' });
    await new Promise(r => setTimeout(r, 0));
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);
    expect(puts.length).toBe(1);

    await act(async () => {
      puts[0](item({ id: 31, tags: [{ id: 101, name: 'factorization', subject: 'math', sort_order: 1, is_system: true }] }));
    });
    await waitFor(() => expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull());
  });

  it('知识标签: a failed candidate load shows role=alert and retry recovers the list', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics
      .mockRejectedValueOnce(new Error('502'))
      .mockResolvedValueOnce({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);

    const err = await waitFor(() => {
      const el = container.querySelector('[data-test="pe-tags-load-error-31"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(err.getAttribute('role')).toBe('alert');

    fireEvent.click(err.querySelector('button') as HTMLButtonElement);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    expect(container.querySelector('[data-test="pe-tags-load-error-31"]')).toBeNull();
    expect(taxonomyApi.topics).toHaveBeenCalledTimes(2);
  });

  it('知识标签: an empty candidate list shows the empty hint and still allows clearing', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, subject: 'physics', tags: [{ id: 9, name: 'my custom point', subject: 'physics', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: null, next_cursor: null });
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue(item({ id: 31, subject: 'physics', tags: [] }) as never);

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(q(container, '[data-test="pe-tags-editor-31"]').textContent).toContain('(1/50)');
    });
    expect(container.querySelector('[data-test="pe-tags-empty-31"]')).toBeNull();
    // 自定义标签仍可选（即使候选空）。
    expect(q(container, '[data-test="pe-tag-31-9"]')).toBeTruthy();
    fireEvent.click(q(container, '[data-test="pe-tags-clear-31"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledWith(31, { knowledge_tag_ids: [] }));
  });

  it('知识标签: 50-tag cap disables the un-picked chips and the count shows', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    // 50 条已挂标签 + 1 条可选 → 已达上限。
    const existing = Array.from({ length: 50 }, (_, i) => ({
      id: 1000 + i, name: `tag ${String(i).padStart(2, '0')}`, subject: 'math', sort_order: i, is_system: true,
    }));
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: existing })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'zz extra' })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });

    expect(q(container, '[data-test="pe-tags-editor-31"]').textContent).toContain('(50/50)');
    const extra = q(container, '[data-test="pe-tag-31-101"]') as HTMLButtonElement;
    expect(extra.disabled).toBe(true);
    expect(extra.getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(extra);
    // 点不进去；已选的仍可取消。
    expect(extra.getAttribute('aria-pressed')).toBe('false');
    expect((q(container, '[data-test="pe-tag-31-1000"]') as HTMLButtonElement).disabled).toBe(false);
  });

  it('知识标签: switching items does not adopt another item\'s save response', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([
      item({ id: 31, tags: [] }),
      item({ id: 32, tags: [{ id: 9, name: 'other item tag', subject: 'math', sort_order: 0, is_system: false }] }),
    ]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });
    let resolveSave: (v: unknown) => void = () => {};
    vi.mocked(api.admin.updateErrorItem).mockImplementation(
      () => new Promise(resolve => { resolveSave = resolve as (v: unknown) => void; }),
    );

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);

    // 面板此时整块卸载（行收起），随后慢响应才落地。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    await act(async () => {
      resolveSave(item({ id: 31, tags: [{ id: 101, name: 'factorization', subject: 'math', sort_order: 1, is_system: true }] }));
    });
    // 面板整块卸载后迟到的响应被丢弃：不回到 31 号行、也不污染 32 号。
    await new Promise(r => setTimeout(r, 0));
    expect(container.querySelector('[data-test="pe-item-32-tagline"]')).toBeNull();

    // 32 号错题一点没被 31 的响应污染。
    fireEvent.click(q(container, '[data-test="pe-item-32"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-32-tagline"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-item-32-tagline"]').textContent).toContain('other item tag');
    expect(q(container, '[data-test="pe-item-32-tagline"]').textContent).not.toContain('factorization');
  });

  it('知识标签: an unsubscribed/malformed save response never fabricates tags', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });
    // 非法响应：没有 id / 没有 tags。
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue({ knowledge_tag_ids: [101] } as never);

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1));
    await new Promise(r => setTimeout(r, 0));

    // 不凭空拼出 "factorization"：行内标签行保持不存在。
    expect(container.querySelector('[data-test="pe-item-31-tagline"]')).toBeNull();
  });

  it('知识标签: cancel/clear/save are keyboard reachable and toggle by Enter', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101 })], next_cursor: null });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });

    const user = userEvent.setup();
    await user.tab();
    const chip = q(container, '[data-test="pe-tag-31-101"]') as HTMLButtonElement;
    chip.focus();
    // 原生 button 的 Enter 触发 click：选中 → 再按取消。
    await user.keyboard('{Enter}');
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
    await user.keyboard('{Enter}');
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('false');
    // 三个操作按钮都是可聚焦的原生 type=button。
    for (const k of ['pe-tags-clear-31', 'pe-tags-cancel-31', 'pe-tags-save-31']) {
      const b = q(container, `[data-test="${k}"]`) as HTMLButtonElement;
      expect(b.tagName).toBe('BUTTON');
      expect(b.getAttribute('type')).toBe('button');
      expect(b.tabIndex).toBe(0);
    }
  });

  // ---- R1 定向复验：D1 全量 cursor 分页 + D2 保存状态上提 ----

  /** 一页 200 条系统标签（id 1000..1199）。 */
  const pageOf200 = () => Array.from({ length: 200 }, (_, i) => topicRow({
    id: 1000 + i,
    external_ref: `cnm_p${1000 + i}`,
    name: `tag ${String(i).padStart(3, '0')}`,
  }));

  it('D1: follows next_cursor to page 2, dedupes ids, and can save the 201st tag id', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    const page1 = pageOf200();
    taxonomyApi.topics.mockImplementation(async query => {
      if (!query.cursor) {
        return { topics: page1, next_cursor: 'cursor-1' };
      }
      // 第二页：故意重复第一页 id 1000（应保留第一页出现的名称），
      // 外加第 201 条合法同科标签。
      return {
        topics: [
          topicRow({ id: 1000, external_ref: 'dup', name: 'DUPLICATE SHOULD NOT WIN' }),
          topicRow({ id: 1201, external_ref: 'cnm_p1201', name: 'zzz 201st tag' }),
        ],
        next_cursor: null,
      };
    });
    vi.mocked(api.admin.updateErrorItem).mockResolvedValue(
      item({ id: 31, tags: [{ id: 1201, name: 'zzz 201st tag', subject: 'math', sort_order: 1, is_system: true }] }) as never,
    );

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-1201"]')).toBeTruthy();
    });

    // 续拉：第一页无 cursor，第二页带上一页的 next_cursor。
    expect(taxonomyApi.topics).toHaveBeenNthCalledWith(1, { subject: 'math', limit: 200 });
    expect(taxonomyApi.topics).toHaveBeenNthCalledWith(2, { subject: 'math', limit: 200, cursor: 'cursor-1' });
    // 顺序/id 去重：1000 只出现一次且保留第一页名称；总数 200 + 1。
    expect(container.querySelectorAll('[data-test="pe-tag-31-1000"]').length).toBe(1);
    expect(q(container, '[data-test="pe-tag-31-1000"]').textContent).toBe('tag 000');
    expect(container.querySelectorAll('[data-test^="pe-tag-31-"]').length).toBe(201);

    // 第 201 条可选并保存其 id。
    fireEvent.click(q(container, '[data-test="pe-tag-31-1201"]'));
    expect(q(container, '[data-test="pe-tag-31-1201"]').getAttribute('aria-pressed')).toBe('true');
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    await waitFor(() => expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1));
    expect(api.admin.updateErrorItem).toHaveBeenCalledWith(31, { knowledge_tag_ids: [1201] });
    await waitFor(() => {
      expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('zzz 201st tag');
    });
  });

  it('D1: a repeated cursor surfaces role=alert (2 calls, no spin, no silent truncation) and retries', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockImplementation(async () => ({
      topics: [topicRow({ id: 101, name: 'factorization' })],
      next_cursor: 'loop-cursor',
    }));

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);

    const err = await waitFor(() => {
      const el = container.querySelector('[data-test="pe-tags-load-error-31"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(err.getAttribute('role')).toBe('alert');
    // 死循环保护：第一页 + 第二页发现 cursor 重复即终止，绝不截断展示半页。
    expect(taxonomyApi.topics).toHaveBeenCalledTimes(2);
    expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeNull();
    expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeTruthy();

    // 同一个 alert 里的重试可恢复。
    taxonomyApi.topics.mockReset();
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });
    fireEvent.click(err.querySelector('button') as HTMLButtonElement);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    expect(container.querySelector('[data-test="pe-tags-load-error-31"]')).toBeNull();
  });

  it('D1: unique cursors that never terminate hit the 50-page fuse (alert, bounded calls)', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockImplementation(async query => {
      const n = query.cursor ? Number(query.cursor) : 0;
      return { topics: [topicRow({ id: 5000 + n, external_ref: `fuse-${n}`, name: `fuse ${n}` })], next_cursor: `${n + 1}` };
    });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    const err = await waitFor(() => {
      const el = container.querySelector('[data-test="pe-tags-load-error-31"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(err.getAttribute('role')).toBe('alert');
    expect(taxonomyApi.topics).toHaveBeenCalledTimes(50);
    expect(container.querySelector('[data-test="pe-tag-31-5000"]')).toBeNull();
  });

  it('D2: collapsing mid-save keeps the PUT, adopts the server tags by id, and never double-PUTs', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });
    let resolveSave: (v: unknown) => void = () => {};
    vi.mocked(api.admin.updateErrorItem).mockImplementation(
      () => new Promise(resolve => { resolveSave = resolve as (v: unknown) => void; }),
    );

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);

    // 保存挂起中收起行（picker 整块卸载），再展开：选择与 busy 都还在。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
    expect((q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement).disabled).toBe(true);

    // 收起/展开 + 键盘都打不进第二次 PUT。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-save-31"]')).toBeTruthy();
    });
    const saveBtn = q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement;
    fireEvent.click(saveBtn);
    saveBtn.focus();
    fireEvent.keyDown(saveBtn, { key: 'Enter', code: 'Enter' });
    fireEvent.keyUp(saveBtn, { key: 'Enter', code: 'Enter' });
    await new Promise(r => setTimeout(r, 0));
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);

    // 在 picker 卸载状态下落地：仍按同 id 采用服务端 fresh tags。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    await act(async () => {
      resolveSave(item({ id: 31, tags: [{ id: 101, name: 'server canonical name', subject: 'math', sort_order: 1, is_system: true }] }));
    });
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-item-31-tagline"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('server canonical name');
    expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    expect(api.admin.updateErrorItem).toHaveBeenCalledTimes(1);
  });

  it('D2: a failed save while collapsed keeps the selection and re-shows role=alert for retry', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.errorItems.list).mockResolvedValue([item({ id: 31, tags: [] })]);
    taxonomyApi.topics.mockResolvedValue({ topics: [topicRow({ id: 101, name: 'factorization' })], next_cursor: null });
    let rejectFirst: (reason?: unknown) => void = () => {};
    let puts = 0;
    vi.mocked(api.admin.updateErrorItem).mockImplementation(() => {
      puts += 1;
      if (puts === 1) {
        return new Promise((_resolve, reject) => { rejectFirst = reject; });
      }
      return Promise.resolve(item({ id: 31, tags: [{ id: 101, name: 'server canonical name', subject: 'math', sort_order: 1, is_system: true }] }));
    });

    const { container } = render(<ErrorbookTab />);
    await openEditor(container);
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tag-31-101"]')).toBeTruthy();
    });
    fireEvent.click(q(container, '[data-test="pe-tag-31-101"]'));
    fireEvent.click(q(container, '[data-test="pe-tags-save-31"]'));
    expect(puts).toBe(1);

    // 保存挂起中收起；失败时 picker 不在场，状态必须仍在行级。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    await act(async () => { rejectFirst(new Error('400')); });
    await new Promise(r => setTimeout(r, 0));

    // 重开：选择保留 + role=alert + 可重试；收起/展开/键盘没有多打 PUT。
    fireEvent.click(q(container, '[data-test="pe-item-31"] button'));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeTruthy();
    });
    expect(q(container, '[data-test="pe-tag-31-101"]').getAttribute('aria-pressed')).toBe('true');
    const alert = q(container, '[data-test="pe-tags-save-error-31"]');
    expect(alert.getAttribute('role')).toBe('alert');
    expect(puts).toBe(1);
    const saveBtn = q(container, '[data-test="pe-tags-save-31"]') as HTMLButtonElement;
    expect(saveBtn.disabled).toBe(false);
    saveBtn.focus();
    fireEvent.keyDown(saveBtn, { key: 'Enter', code: 'Enter' });
    await new Promise(r => setTimeout(r, 0));
    expect(puts).toBe(1);

    // 真正的重试：第二次 PUT 成功，采用服务端最新 tags，面板关闭。
    fireEvent.click(saveBtn);
    await waitFor(() => expect(puts).toBe(2));
    await waitFor(() => {
      expect(container.querySelector('[data-test="pe-tags-editor-31"]')).toBeNull();
    });
    expect(q(container, '[data-test="pe-item-31-tagline"]').textContent).toContain('server canonical name');
  });
});
