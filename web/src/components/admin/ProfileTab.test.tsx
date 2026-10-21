// @vitest-environment jsdom
// The parent's learner-profile tab (L3): the kid picker, the four hero
// stats, the KC matrix buckets (weak/mid/good + low-confidence grey), the
// 14-day trend, the Ranedeer config editor (save round-trip) and the
// cited plan feed — adopt goes through copilotApply, ignore is local.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';

// The whole API surface the tab touches, mocked at module level.
const { api } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    admin: {
      learnerProfile: vi.fn(),
      updateStudentConfig: vi.fn(),
      copilotPlan: vi.fn(),
      copilotApply: vi.fn(),
    },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { LearnerProfile, StudentConfig, User } from '../../types';
import { ProfileTab } from './ProfileTab';

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;
const qAll = (root: HTMLElement, sel: string) => [...root.querySelectorAll(sel)];

const user = (id: number, name: string): User => ({
  id, name, role: 'child', color: 'coral', avatar: undefined,
  pin: undefined, has_pin: false, tap_secret: undefined, points_balance: 0,
  created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
}) as unknown as User;

const config = (over: Partial<StudentConfig> = {}): StudentConfig => ({
  user_id: 3,
  depth: '初中',
  learning_style: 'Visual',
  communication: 'Textbook',
  tone: 'Encouraging',
  reasoning: 'Deductive',
  interests: '["恐龙","乐高"]',
  strengths: '',
  weaknesses: '',
  updated_at: '2026-10-12T00:00:00Z',
  ...over,
});

const profile = (over: Partial<LearnerProfile> = {}): LearnerProfile => ({
  kid_id: 3,
  kid_name: '小明',
  config: config(),
  kc_mastery: [
    { tag_id: 17, name: '一元二次方程判别式', subject: 'math', correct_count: 5, total_count: 12, mastery: 0.4583, low_confidence: false, last_event_at: '2026-10-11T00:00:00Z' },
    { tag_id: 18, name: '因式分解', subject: 'math', correct_count: 7, total_count: 12, mastery: 0.6153, low_confidence: false, last_event_at: '2026-10-11T00:00:00Z' },
    { tag_id: 19, name: '宾语从句', subject: 'english', correct_count: 11, total_count: 12, mastery: 0.9230, low_confidence: false, last_event_at: '2026-10-11T00:00:00Z' },
    { tag_id: 20, name: '化学式书写', subject: 'chemistry', correct_count: 1, total_count: 3, mastery: 0.4, low_confidence: true, last_event_at: '2026-10-11T00:00:00Z' },
  ],
  events: [],
  event_trend: Array.from({ length: 14 }, (_, i) => ({ day: `2026-10-${String(i + 1).padStart(2, '0')}`, events: i === 13 ? 5 : i % 3 })),
  hero: {
    chores_this_week: 23, answers_total: 45, fix_rate: 0.82, review_streak: 5,
    weak_kc_count: 3, mastered_items: 9, reviewed_today: 2,
  },
  ...over,
});

beforeAll(async () => { await i18n.changeLanguage('zh'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

// Standard render: one kid, a full profile.
const renderTab = async () => {
  vi.mocked(api.users.list).mockResolvedValue([user(1, '小明'), user(2, '爸爸') as unknown as User]);
  vi.mocked(api.admin.learnerProfile).mockResolvedValue(profile());
  const { container } = render(<ProfileTab />);
  await waitFor(() => expect(q(container, '[data-test="pp-hero"]')).toBeTruthy());
  return { container };
};

describe('ProfileTab', () => {
  it('renders the hero stats from the profile payload', async () => {
    const { container } = await renderTab();
    // The four OD pp-stat cells.
    expect(q(container, '[data-test="pp-stat-tasks"]')!.textContent).toContain('23');
    expect(q(container, '[data-test="pp-stat-fixrate"]')!.textContent).toContain('82%');
    expect(q(container, '[data-test="pp-stat-review"]')!.textContent).toContain('5');
    expect(q(container, '[data-test="pp-stat-kc"]')!.textContent).toContain('3');
    expect(api.admin.learnerProfile).toHaveBeenCalledWith(1);
  });

  it('buckets the KC matrix rows weak/mid/good and greys low-confidence', async () => {
    const { container } = await renderTab();
    const rows = qAll(container, '[data-test="pp-kc-row"]');
    expect(rows.length).toBe(4);
    const bucket = (i: number) => rows[i].getAttribute('data-mastery');
    expect(bucket(0)).toBe('weak');   // 46% 判别式
    expect(bucket(1)).toBe('mid');    // 62% 因式分解
    expect(bucket(2)).toBe('good');   // 92% 宾语从句
    // The low-confidence row stays greyed (kcLow class) and flagged.
    expect(rows[3].className).toContain('kcLow');
    expect(rows[3].textContent).toContain('样本不足');
    // Grouped by subject: 数学 group header shows the sample count.
    expect(container.textContent).toContain('近 24 题样本');
  });

  it('renders the 14-day trend with every day present', async () => {
    const { container } = await renderTab();
    expect(qAll(container, '[data-test="pp-trend-chart"] [data-test="pp-trend-col"], [data-test="pp-trend-chart"] > *').length).toBeGreaterThan(0);
    // 14 columns inside the chart region.
    const cols = q(container, '[data-test="pp-trend-chart"]').children.length;
    expect(cols).toBe(14);
  });

  it('edits and saves the config through the API round-trip', async () => {
    const { container } = await renderTab();
    vi.mocked(api.admin.updateStudentConfig).mockResolvedValue(config({ tone: 'Humorous' }));

    // Switch tone to 幽默 (the OD pcfg-tone chip).
    const toneRow = q(container, '[data-test="pcfg-tone"]');
    const humor = [...toneRow.querySelectorAll('button')].find(b => b.textContent === '幽默')!;
    fireEvent.click(humor);

    fireEvent.click(q(container, '[data-test="pp-config-save"]'));
    await waitFor(() => expect(api.admin.updateStudentConfig).toHaveBeenCalled());
    const [id, payload] = vi.mocked(api.admin.updateStudentConfig).mock.calls[0];
    expect(id).toBe(1);
    expect((payload as StudentConfig).tone).toBe('Humorous');
    // The saved confirmation shows.
    await waitFor(() => expect(container.textContent).toContain('已保存'));
  });

  it('adds and removes interest tags before saving', async () => {
    const { container } = await renderTab();
    vi.mocked(api.admin.updateStudentConfig).mockResolvedValue(config());

    const row = q(container, '[data-test="pcfg-interests"]');
    // Existing tags render from the JSON string.
    expect(row.textContent).toContain('恐龙');
    // Remove 恐龙 (the remove button is the ✕ inside the tag chip).
    fireEvent.click(row.querySelector('button[aria-label*="恐龙"]')!);
    // The tag chip is gone (the hint text below may still mention 恐龙 generically,
    // so assert on the chips themselves, not the whole row).
    const chips = [...row.querySelectorAll('button[aria-label*="删除标签"]')];
    expect(chips.some(b => b.getAttribute('aria-label')!.includes('恐龙'))).toBe(false);
    // Add 航天.
    const input = row.querySelector('input')!;
    fireEvent.change(input, { target: { value: '航天' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    const chips2 = [...row.querySelectorAll('button[aria-label*="删除标签"]')];
    expect(chips2.some(b => b.getAttribute('aria-label')!.includes('航天'))).toBe(true);

    fireEvent.click(q(container, '[data-test="pp-config-save"]'));
    await waitFor(() => expect(api.admin.updateStudentConfig).toHaveBeenCalled());
    const payload = vi.mocked(api.admin.updateStudentConfig).mock.calls[0][1] as StudentConfig;
    expect(JSON.parse(payload.interests)).toEqual(['乐高', '航天']);
  });

  it('renders the plan feed with cited refs and adopts through copilotApply', async () => {
    const { container } = await renderTab();
    vi.mocked(api.admin.copilotPlan).mockResolvedValue({
      mode: 'learner_plan',
      assigned_to: 1,
      plan: {
        source_refs: [
          { type: 'kc_mastery', id: 17, fact: '判别式正确率 45%（近 12 题）' },
        ],
        proposal: {
          new_chores: [{
            title: '判别式专项 5 题', description: '针对薄弱知识点出 5 道判别式练习',
            category: 'required', icon: 'book', points_value: 10, missed_penalty: 0,
            estimated_minutes: 25, days: ['mon', 'tue'], date: '2026-10-16', due_by: '20:00',
            available_at: '15:00', confidence: 0.85, rationale: '判别式正确率仅 45%',
          }],
          review_boost: [],
          notes_to_parent: '本周末建议家长陪练一次',
        },
      },
    });
    vi.mocked(api.admin.copilotApply).mockResolvedValue({ created: 1 } as never);

    // Generate the plan.
    fireEvent.click([...container.querySelectorAll('button')].find(b => b.textContent === '生成建议')!);
    await waitFor(() => expect(q(container, '[data-test="pp-plan-1"]')).toBeTruthy());

    // The cited ref line renders with its source.
    expect(q(container, '[data-test="pp-plan-1-refs"]').textContent).toContain('判别式正确率 45%');
    expect(q(container, '[data-test="pp-plan-1-refs"]').textContent).toContain('kc_mastery #17');
    // The disclaimer note is present (parent final say).
    expect(q(container, '[data-test="pp-plan-note"]')).toBeTruthy();

    // Adopt → copilotApply with expiry 'block' (the QuickAssign mapping).
    fireEvent.click(q(container, '[data-test="pp-plan-1-adopt"]'));
    await waitFor(() => expect(api.admin.copilotApply).toHaveBeenCalled());
    const apply = vi.mocked(api.admin.copilotApply).mock.calls[0][0];
    expect(apply.assigned_to).toBe(1);
    expect(apply.source).toBe('planner');
    expect(apply.tasks[0].title).toBe('判别式专项 5 题');
    expect(apply.tasks[0].expiry).toBe('block');
    // The card flips to adopted.
    await waitFor(() => expect(q(container, '[data-test="pp-plan-1"]').getAttribute('data-state')).toBe('adopted'));
  });

  it('ignores a plan card locally and keeps the disclaimer', async () => {
    const { container } = await renderTab();
    vi.mocked(api.admin.copilotPlan).mockResolvedValue({
      mode: 'learner_plan',
      assigned_to: 1,
      plan: {
        source_refs: [],
        proposal: {
          new_chores: [{
            title: '英语听力 10 分钟', description: '每天 10 分钟磨耳朵',
            category: 'bonus', icon: 'sound', points_value: 5, missed_penalty: 0,
            estimated_minutes: 10, days: ['mon'], date: '2026-10-16', due_by: '21:00',
            available_at: '18:00', confidence: 0.4, rationale: '兴趣延伸',
          }],
          review_boost: [],
          notes_to_parent: '',
        },
      },
    });
    fireEvent.click([...container.querySelectorAll('button')].find(b => b.textContent === '生成建议')!);
    await waitFor(() => expect(q(container, '[data-test="pp-plan-1"]')).toBeTruthy());

    fireEvent.click(q(container, '[data-test="pp-plan-1-ignore"]'));
    await waitFor(() => expect(q(container, '[data-test="pp-plan-1"]').getAttribute('data-state')).toBe('ignored'));
    expect(api.admin.copilotApply).not.toHaveBeenCalled();
  });

  it('surfaces a plan failure without crashing', async () => {
    const { container } = await renderTab();
    // The backend rejects with its own message; the tab shows it as the alert.
    vi.mocked(api.admin.copilotPlan).mockRejectedValue(new Error('AI 渠道不可用'));
    fireEvent.click([...container.querySelectorAll('button')].find(b => b.textContent === '生成建议')!);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('AI 渠道不可用');
  });

  it('shows the load failure when the profile fetch rejects', async () => {
    vi.mocked(api.users.list).mockResolvedValue([user(1, '小明')]);
    vi.mocked(api.admin.learnerProfile).mockRejectedValue(new Error('offline'));
    const { container } = render(<ProfileTab />);
    await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
    expect(container.textContent).toContain('加载失败');
  });
});
