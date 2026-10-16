// @vitest-environment jsdom
// The measures workbench (034): status groups, the AI-strengthen button
// (the user's 「家长也可以针对措施通过 AI 加强」), pause/resume/finish/delete,
// and the hand-written create form.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';

// The API client is mocked at the module level: the tab must only talk to
// users.list / admin.listMeasures / strengthenMeasure / setMeasureStatus /
// deleteMeasure / createMeasure.
const { api } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    admin: {
      listMeasures: vi.fn(),
      strengthenMeasure: vi.fn(),
      setMeasureStatus: vi.fn(),
      deleteMeasure: vi.fn(),
      createMeasure: vi.fn(),
    },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { LearningMeasure, User } from '../../types';
import { MeasuresTab } from './MeasuresTab';

const kid = (over: Partial<User> = {}): User => ({
  ...(over as User),
  id: 2,
  name: 'Mika',
  role: 'child',
} as User);

const measure = (over: Partial<LearningMeasure> = {}): LearningMeasure => ({
  id: 51,
  user_id: 2,
  subject: 'math',
  title: '每天 3 道判别式练习',
  detail: '连续一周，每天晚饭后练 3 道一元二次方程判别式题',
  error_types: ['concept'],
  origin: 'analysis',
  status: 'active',
  priority: 1,
  created_by: 1,
  created_at: '2026-10-15T00:00:00Z',
  updated_at: '2026-10-15T00:00:00Z',
  ...over,
} as LearningMeasure);

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('MeasuresTab', () => {
  it('groups the measures by status and shows every origin', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.listMeasures).mockResolvedValue([
      measure({ id: 51, origin: 'analysis', status: 'active' }),
      measure({ id: 52, origin: 'manual', status: 'paused', title: '口算 10 分钟' }),
      measure({ id: 53, origin: 'ai_strengthen', status: 'done', title: '错题周清' }),
    ]);

    const { container } = render(<MeasuresTab />);

    await waitFor(() => {
      const el = container.querySelector('[data-test="pm-measure-51"]');
      expect(el).toBeTruthy();
    });
    // Three cards across the three groups.
    expect(q(container, '[data-test="pm-measure-51"]')).toBeTruthy();
    expect(q(container, '[data-test="pm-measure-52"]')).toBeTruthy();
    expect(q(container, '[data-test="pm-measure-53"]')).toBeTruthy();
    // The origin chips read per origin (en: from paper analysis /
    // written by parent / AI strengthened).
    expect(q(container, '[data-test="pm-measure-51"]').textContent).toContain('paper analysis');
    expect(q(container, '[data-test="pm-measure-52"]').textContent).toContain('Written by parent');
    expect(q(container, '[data-test="pm-measure-53"]').textContent).toContain('AI strengthened');
  });

  it('AI-strengthens a measure', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.listMeasures).mockResolvedValue([measure({ id: 51 })]);
    vi.mocked(api.admin.strengthenMeasure).mockResolvedValue(measure({ id: 51, origin: 'ai_strengthen' }));

    const { container } = render(<MeasuresTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pm-strengthen-51"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    await waitFor(() => expect(api.admin.strengthenMeasure).toHaveBeenCalledWith(51));
  });

  it('pauses an active measure', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.listMeasures).mockResolvedValue([measure({ id: 51 })]);
    vi.mocked(api.admin.setMeasureStatus).mockResolvedValue(measure({ id: 51, status: 'paused' }));

    const { container } = render(<MeasuresTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pm-pause-51"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    await waitFor(() => expect(api.admin.setMeasureStatus).toHaveBeenCalledWith(51, 'paused'));
  });

  it('creates a hand-written measure from the form', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.listMeasures).mockResolvedValue([]);
    vi.mocked(api.admin.createMeasure).mockResolvedValue(measure({ id: 60, origin: 'manual' }));

    const { container } = render(<MeasuresTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pm-create-open"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    await waitFor(() => {
      const el = container.querySelector('[data-test="pm-create-form"]');
      expect(el).toBeTruthy();
    });
    // Fill title + detail, submit.
    const inputs = q(container, '[data-test="pm-create-form"]').querySelectorAll('input, textarea');
    fireEvent.change(inputs[0], { target: { value: '口算 10 分钟' } });
    fireEvent.change(inputs[1], { target: { value: '每天晚饭后口算 10 分钟' } });
    fireEvent.submit(q(container, '[data-test="pm-create-form"]'));

    await waitFor(() => expect(api.admin.createMeasure).toHaveBeenCalledWith(expect.objectContaining({
      user_id: 2,
      title: '口算 10 分钟',
      detail: '每天晚饭后口算 10 分钟',
    })));
  });

  it('shows the empty state', async () => {
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.listMeasures).mockResolvedValue([]);

    const { container } = render(<MeasuresTab />);
    await waitFor(() => {
      const el = container.querySelector('[data-test="pm-empty"]');
      expect(el).toBeTruthy();
    });
  });
});
