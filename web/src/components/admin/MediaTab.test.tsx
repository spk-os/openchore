// @vitest-environment jsdom
// 042: the parent-side media library tab — subject/class chips with live
// counts, the kid filter, the clear button, the empty state. The API
// clients are mocked at the module level: the tab must only talk to
// users.list / chores.listMedia, and useSubjects only to
// taxonomyApi.subjects (GET /api/taxonomy/subjects, subjects 表驱动).
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { SubjectRow } from '../../taxonomy/types';

const { api, taxonomyApi } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    chores: { listMedia: vi.fn() },
  },
  taxonomyApi: { subjects: vi.fn() },
}));
vi.mock('../../api', () => ({ api }));
// useSubjects 走 ../taxonomy/client（相对 hook 模块），这里按组件目录同样解析到
// web/src/taxonomy/client；返回真实 SubjectRow 形状，避免 jsdom 真实 fetch。
vi.mock('../../taxonomy/client', () => ({ taxonomyApi }));

import type { MediaLibraryResponse, MediaItem, User } from '../../types';

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

const subjectRow = (over: Partial<SubjectRow> = {}): SubjectRow => ({
  id: 'math',
  name: '数学',
  sb_code: 'MATH',
  topic_count: 3108,
  question_count: 9000,
  grades: [1, 2, 3],
  ...over,
});

/** 服务端真实响应形状：sort_order 已体现在数组顺序里，前端原样渲染。 */
const serverSubjects = (): SubjectRow[] => [
  subjectRow({ id: 'physics', name: '物理', sb_code: 'PHYS', topic_count: 42, question_count: 300, grades: [7, 8, 9] }),
  subjectRow({ id: 'math', name: '数学', sb_code: 'MATH' }),
  subjectRow({ id: 'chinese', name: '语文', sb_code: 'CHN', topic_count: 120, question_count: 800, grades: [1, 2] }),
];

// useSubjects 的 cached/cachedAt/inflight 是模块级共享状态。逐用例
// vi.resetModules() + 动态 import 同一份模块图（RTL / i18n / 组件），
// 保证新 hook 副本与渲染器、i18n 实例一致，且成功快照/失败状态不串味。
// RTL 的 fireEvent/waitFor/act 也必须来自同一份动态模块图，避免 React 副本串味。
type RTL = typeof import('@testing-library/react');
let rtl: RTL;
let fireEvent: RTL['fireEvent'];
let waitFor: RTL['waitFor'];
let renderMediaTab: () => ReturnType<RTL['render']>;
let renderBoth: () => ReturnType<RTL['render']>;
let MediaTabCmp: React.FC;
let SubjectPickerCmp: React.FC<{ value: string; onChange: (s: string) => void; label: string }>;

// 排除容器自身的 data-test="media-subject-filter"，只取真正的学科 chip。
const subjectChipSelector = '[data-test^="media-subject-"]:not([data-test="media-subject-filter"])';

const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeEach(async () => {
  vi.clearAllMocks();
  taxonomyApi.subjects.mockReset();
  vi.mocked(api.users.list).mockReset();
  vi.mocked(api.chores.listMedia).mockReset();
  vi.mocked(api.users.list).mockResolvedValue([kid()]);
  taxonomyApi.subjects.mockResolvedValue({ subjects: serverSubjects() });

  vi.resetModules();
  vi.doMock('../../api', () => ({ api }));
  vi.doMock('../../taxonomy/client', () => ({ taxonomyApi }));
  rtl = await import('@testing-library/react');
  ({ fireEvent, waitFor } = rtl);
  const { default: freshI18n } = await import('../../i18n');
  await freshI18n.changeLanguage('en');
  const { MediaTab } = await import('./MediaTab');
  const { SubjectPicker } = await import('./pickers');
  MediaTabCmp = MediaTab;
  SubjectPickerCmp = SubjectPicker;
  renderMediaTab = () => rtl.render(<MediaTab />);
  // 两个真实消费者同屏：验证模块级 store 的 snapshot 在同一轮广播给所有人。
  renderBoth = () => rtl.render(
    <div>
      <SubjectPickerCmp value="" onChange={() => {}} label="Subject" />
      <MediaTabCmp />
    </div>,
  );
});

afterEach(() => {
  rtl.cleanup();
  vi.doUnmock('../../api');
  vi.doUnmock('../../taxonomy/client');
});

describe('MediaTab', () => {
  it('renders the grid with counts on every chip', async () => {
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

    const { container } = renderMediaTab();

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
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      subjects: [{ subject: 'math', count: 2 }],
      classes: [{ mime_class: 'audio', count: 1 }],
      total: 2,
    }));

    const { container } = renderMediaTab();
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
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item()],
      subjects: [{ subject: 'math', count: 1 }],
      total: 1,
    }));

    const { container } = renderMediaTab();
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
    vi.mocked(api.chores.listMedia).mockResolvedValue(library());

    const { container } = renderMediaTab();
    await waitFor(() => {
      expect(container.querySelector('[data-test="media-grid"]')).toBeNull();
    });
    // The i18n empty copy is up (en).
    expect(container.textContent).toContain('No photos, videos or recordings yet');
  });

  it('marks reviewed items with the AI badge', async () => {
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item({ ai_complete: true, ai_confidence: 0.92, ai_feedback: 'Read the whole poem.' })],
      total: 1,
    }));

    const { container } = renderMediaTab();
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1);
    });
    const badge = container.querySelector('[class*="aiBadge"]');
    expect(badge).toBeTruthy();
    expect(badge!.textContent).toContain('92%');
  });

  it('renders server subjects in their real order plus the unclassified chip, no static vocabulary', async () => {
    // 服务端只返 physics / math / chinese 且 physics 在前：chips 顺序必须原样，
    // 不恢复静态词表，也不因 count=0 漏渲染。
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item()],
      subjects: [{ subject: 'math', count: 1 }, { subject: '', count: 1 }],
      total: 2,
    }));

    const { container } = renderMediaTab();
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1);
    });
    const chips = [...container.querySelectorAll(subjectChipSelector)];
    expect(chips.map(el => el.getAttribute('data-test'))).toEqual([
      'media-subject-physics', 'media-subject-math', 'media-subject-chinese',
    ]);
    expect(q(container, '[data-test="media-subject-physics"]').textContent).toContain('0');
    expect(q(container, '[data-test="media-subject-math"]').textContent).toContain('1');
    expect(q(container, '[data-test="media-subject-chinese"]').textContent).toContain('0');
    // 空学科 chip 仍恒在（无 data-test，按 en 文案定位）且展示 '' 桶的 live count。
    const unclassified = [...container.querySelectorAll('[data-test="media-subject-filter"] button')]
      .find(el => el.textContent?.includes('Unclassified'));
    expect(unclassified).toBeTruthy();
    expect(unclassified!.textContent).toContain('1');
  });

  it('renders the unclassified chip even when the server subject list is empty', async () => {
    // 空学科成功响应不是错误态：'' 未分类 chip 仍渲染，且不回退硬编码学科。
    taxonomyApi.subjects.mockResolvedValue({ subjects: [] });
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item({ subject: '' })],
      subjects: [{ subject: '', count: 1 }],
      total: 1,
    }));

    const { container } = renderMediaTab();
    await waitFor(() => {
      expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1);
    });
    expect(container.querySelectorAll(subjectChipSelector).length).toBe(0);
    expect(container.querySelector('[data-test="media-subjects-error"]')).toBeNull();
    const unclassified = [...container.querySelectorAll('[data-test="media-subject-filter"] button')]
      .find(el => el.textContent?.includes('Unclassified'));
    expect(unclassified).toBeTruthy();
    expect(unclassified!.textContent).toContain('1');
    // 「未分类」= '__unclassified__'（COALESCE 空串桶），不再与「全部」('')
    // 共用同值：点 chip 后只能按 '__unclassified__' 上送，不发额外 subjects 请求。
    fireEvent.click(unclassified as HTMLButtonElement);
    await waitFor(() => expect(api.chores.listMedia).toHaveBeenLastCalledWith(
      expect.objectContaining({ subject: '__unclassified__' })));
  });

  it('surfaces a subjects failure and retries it with an explicitly controlled promise', async () => {
    // 显式控制 rejected/resolved：失败只影响学科清单，媒体库本身照常加载。
    let rejectSubjects: (e: Error) => void = () => {};
    const failed = new Promise<{ subjects: SubjectRow[] | null }>((_, reject) => { rejectSubjects = reject; });
    taxonomyApi.subjects
      .mockReturnValueOnce(failed)
      .mockResolvedValueOnce({ subjects: [subjectRow({ id: 'physics', name: '物理' })] });
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({ items: [item()], total: 1 }));

    const { container } = renderMediaTab();
    await rtl.act(async () => { rejectSubjects(new Error('subject service down')); await Promise.resolve(); });
    expect(await waitFor(() => q(container, '[data-test="media-subjects-error"]'))).toBeTruthy();
    expect(container.querySelector(subjectChipSelector)).toBeNull();
    // 媒体库不受学科请求失败影响。
    await waitFor(() => expect(container.querySelectorAll('[data-test="media-item"]').length).toBe(1));

    fireEvent.click(q(container, '[data-test="media-subjects-retry"]'));
    await waitFor(() => expect(container.querySelector('[data-test="media-subject-physics"]')).toBeTruthy());
    expect(container.querySelector('[data-test="media-subjects-error"]')).toBeNull();
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(2);
  });

  it('opens the in-page preview instead of navigating on cell click', async () => {
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      // A video: the old bare <a href> opened the raw file in a new tab
      // (the "点视频直接报错" report); the cell is now a button that opens
      // the in-page player.
      items: [item({ mime_class: 'video', mime_type: 'video/mp4', file_path: '/uploads/a2.mp4', display_name: 'demo.mp4' })],
      total: 1,
    }));

    const { container } = renderMediaTab();
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

  it('survives null stats from an empty filter (the audio white screen)', async () => {
    // Tapping a class chip the library has nothing under made the server
    // answer with null subjects/classes (Go nil slices) — the chips then
    // crashed on .find and the whole dashboard went blank
    // ("点录音分类白屏"). The tab must treat null as empty.
    vi.mocked(api.chores.listMedia).mockResolvedValue({
      items: null,
      subjects: null,
      classes: null,
      total: 0,
    } as unknown as MediaLibraryResponse);

    const { container } = renderMediaTab();
    // Rendered without throwing: every class chip shows count 0.
    const audioChip = await waitFor(() => {
      const el = container.querySelector('[data-test="media-class-audio"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(audioChip.textContent).toContain('0');
    expect(container.querySelector('[data-test="media-grid"]')).toBeNull();
    expect(container.textContent).toContain('No photos, videos or recordings yet');
  });

  it('broadcasts one snapshot to every mounted consumer (picker + media tab)', async () => {
    // 两个消费者同屏：pick/r 与 MediaTab 共用同一模块级 store。成功响应只发
    // 一次请求，两块 UI 同一轮拿到同样的学科顺序。
    let resolveSubjects: (v: { subjects: SubjectRow[] }) => void = () => {};
    taxonomyApi.subjects.mockReturnValue(new Promise(r => { resolveSubjects = r; }));
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({ items: [item()], total: 1 }));

    const { container } = renderBoth();
    // 两个消费者都在等待：select 禁用，chips 只有「全部/未分类」。
    const select = q(container, '[data-test="chore-subject-select"]') as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(container.querySelector(subjectChipSelector)).toBeNull();

    await rtl.act(async () => {
      resolveSubjects({ subjects: serverSubjects() });
      await Promise.resolve();
    });
    await waitFor(() => expect(select.disabled).toBe(false));
    const pickerOpts = [...select.options].map(o => o.value);
    const chipIds = [...container.querySelectorAll(subjectChipSelector)]
      .map(el => (el.getAttribute('data-test')!).replace('media-subject-', ''));
    // 服务端顺序原样贯通到两个消费者（'' 始终在 select 首位）。
    expect(pickerOpts).toEqual(['', 'physics', 'math', 'chinese']);
    expect(chipIds).toEqual(['physics', 'math', 'chinese']);
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(1);
  });

  it('stale + error broadcasts the failure to every consumer; one retry syncs all', async () => {
    // 第一次请求挂起（可控）：两个消费者同时处于 loading（select 禁用），
    // 然后一次失败 → 两个消费者同一轮看到 error；从 MediaTab 一侧 retry 成功
    // → 两个消费者同一轮换成新学科，error 消失。in-flight 去重保证只发 2 次。
    let rejectSubjects: (e: Error) => void = () => {};
    const failed = new Promise<{ subjects: SubjectRow[] }>((_, reject) => { rejectSubjects = reject; });
    taxonomyApi.subjects
      .mockReturnValueOnce(failed)
      .mockResolvedValueOnce({ subjects: serverSubjects() });
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({ items: [item()], total: 1 }));

    const { container } = renderBoth();
    const select = q(container, '[data-test="chore-subject-select"]') as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(1);

    await rtl.act(async () => { rejectSubjects(new Error('subject service down')); await Promise.resolve(); });
    await waitFor(() => expect(container.querySelector('[data-test="chore-subject-error"]')).toBeTruthy());
    expect(container.querySelector('[data-test="media-subjects-error"]')).toBeTruthy();
    expect(select.disabled).toBe(true);
    expect(container.querySelector(subjectChipSelector)).toBeNull();
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(1);

    // 从 MediaTab 一侧 retry：两个消费者同一轮刷新。
    fireEvent.click(q(container, '[data-test="media-subjects-retry"]'));
    await waitFor(() => expect(container.querySelector('[data-test="media-subjects-error"]')).toBeNull());
    expect(container.querySelector('[data-test="chore-subject-error"]')).toBeNull();
    expect(select.disabled).toBe(false);
    await waitFor(() => expect(container.querySelector('[data-test="media-subject-chinese"]')).toBeTruthy());
    expect([...select.options].map(o => o.value)).toEqual(['', 'physics', 'math', 'chinese']);
    expect(taxonomyApi.subjects).toHaveBeenCalledTimes(2);
  });

  it('unclassified chip labels from the server name and never falls back to a static list', async () => {
    // 服务端只返一个自定义学科（不在任何前端词表里）：chip 文案与 select 文案
    // 都用服务端 SubjectRow.name 原文，而不是 admin.analysis.subject_* 词条或 key。
    taxonomyApi.subjects.mockResolvedValue({
      subjects: [subjectRow({ id: 'astronomy', name: 'Astronomy Club', sb_code: 'AST' })],
    });
    vi.mocked(api.chores.listMedia).mockResolvedValue(library({
      items: [item({ subject: 'astronomy' })],
      subjects: [{ subject: 'astronomy', count: 1 }, { subject: '', count: 0 }],
      total: 1,
    }));

    const { container } = renderBoth();
    const chip = await waitFor(() => {
      const el = container.querySelector('[data-test="media-subject-astronomy"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(chip.textContent).toContain('Astronomy Club');
    const select = q(container, '[data-test="chore-subject-select"]') as HTMLSelectElement;
    await waitFor(() => expect([...select.options].map(o => o.value)).toEqual(['', 'astronomy']));
    expect(select.options[1].textContent).toBe('Astronomy Club');
    // 只有服务端那一项 + 「未分类」，没有 math/english 等静态回退。
    expect(container.querySelector(subjectChipSelector)).toBe(chip);
  });
});
