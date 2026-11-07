// @vitest-environment jsdom
// The kid's course screen (L2): the tree renders the three unit states,
// the lock explanation bubble, the per-predicate report controls, and a
// report re-reads the tree. The Today entry card shows progress and
// hides itself with no published course.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

// The API client is mocked at the module level: the screen must only
// talk to api.courses.get / api.courses.outcomes / api.courses.reportProgress.
const { api } = vi.hoisted(() => ({
  api: {
    courses: {
      list: vi.fn(),
      get: vi.fn(),
      outcomes: vi.fn(),
      reportProgress: vi.fn(),
    },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { CourseTree, OutcomeView, ProgressResponse, ResourceView, UnitView } from '../../types';
import { CourseScreen } from './CourseScreen';
import { CourseEntryCard } from './CourseEntryCard';

// A resource as the unlock engine reports it.
const res = (over: Partial<ResourceView>): ResourceView => ({
  id: 101,
  unit_id: 1,
  title: 'Read: The water cycle',
  position: 1,
  resource_type: 'page',
  content_ref: '',
  completion_type: 'must_view',
  done: false,
  ...over,
});

// A unit with its computed state from the unlock engine.
const unit = (over: Partial<UnitView>): UnitView => ({
  id: 1,
  course_id: 9,
  title: 'Unit 1 · Water',
  position: 1,
  requirement_type: '',
  require_sequential_progress: false,
  published: true,
  prerequisite_unit_ids: [],
  state: 'unlocked',
  resources: [],
  completed_resources: 0,
  total_resources: 0,
  ...over,
});

// The kid's course tree (GET /courses/{id}).
const tree = (over: Partial<CourseTree>): CourseTree => ({
  id: 9,
  user_id: 2,
  title: 'Science · Water',
  description: '',
  syllabus_md: '',
  status: 'published',
  is_template: false,
  created_at: '2026-10-14T00:00:00Z',
  updated_at: '2026-10-14T00:00:00Z',
  progress_percent: 25,
  units_total: 4,
  units_completed: 1,
  units: [],
  ...over,
});

// One mastery line rolled up.
const outcome = (over: Partial<OutcomeView>): OutcomeView => ({
  id: 31,
  course_id: 9,
  title: 'Explains evaporation',
  mastery_points: 80,
  calculation_method: 'latest',
  ratings_json: '',
  score: 85,
  mastered: true,
  assessments: 2,
  ...over,
});

const topBar = <div data-test="topbar" />;

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

// The real host route is /course/:id (KidApp); here the screen reads :id so the
// deep-link tests below carry the course id the URL says.
// The URL is the "id" input (kid CourseScreen reads only :id, never the tree),
// so the string can carry non-safe / negative shapes tsc would reject as number.
const renderAt = (courseId: string | number) => render(
  <MemoryRouter key={String(courseId)} initialEntries={[`/course/${courseId}`]}>
    <Routes>
      <Route path="/course/:id" element={<CourseScreen topBar={topBar} />} />
    </Routes>
  </MemoryRouter>,
);

beforeAll(async () => { await i18n.changeLanguage('en'); });
// pending 的 mockResolvedValue 响应会在用例间重放（同一 tree 被下一用例读到），
// 因此每个用例结束必须 reset 实现，而不只是清调用记录。
afterEach(() => {
  cleanup();
  vi.resetAllMocks();
  vi.mocked(api.courses.list).mockResolvedValue([]);
});

describe('CourseScreen', () => {
  it('renders the course card, the unit path with its three states, and the lock bubble', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(tree({
      units: [
        unit({ id: 1, title: 'Done unit', state: 'completed', completed_resources: 2, total_resources: 2 }),
        unit({
          id: 2, title: 'Open unit', state: 'started', completed_resources: 1, total_resources: 2,
          resources: [res({ id: 111, unit_id: 2 })],
        }),
        unit({
          id: 3, title: 'Locked unit', state: 'locked',
          prerequisite_unit_ids: [2],
          lock_explanation: 'Finish "Open unit" first',
        }),
      ],
    }));
    vi.mocked(api.courses.outcomes).mockResolvedValue([outcome({})]);

    const { container } = render(
      <MemoryRouter><CourseScreen topBar={topBar} /></MemoryRouter>,
    );

    // The course card and progress render.
    const card = await waitFor(() => {
      const el = container.querySelector('[data-test="kc-course-card"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(q(card, '[data-test="kc-progress"]').textContent).toContain('25%');

    // All three unit states render on the path map.
    const states = Array.from(container.querySelectorAll('[data-test="kc-unit"]'))
      .map((el) => el.getAttribute('data-state'));
    expect(states).toEqual(['completed', 'started', 'locked']);

    // The locked unit explains itself and shows no resources.
    const lock = q(container, '[data-test="kc-lock-explanation"]');
    expect(lock.textContent).toContain('Why is it still locked?');
    expect(lock.textContent).toContain('Finish "Open unit" first');

    // The mastery line rolled up to mastered.
    expect(q(container, '[data-test="kc-outcome-31"]').textContent).toContain('85');
    expect(q(container, '[data-test="kc-outcome-31"]').textContent).toContain('mastered');
  });

  it('reports a must_view open as 5 viewed seconds and re-reads the tree', async () => {
    const openRes = res({ id: 111, completion_type: 'must_view' });
    vi.mocked(api.courses.get)
      .mockResolvedValueOnce(tree({
        units: [unit({ id: 2, state: 'unlocked', resources: [openRes], total_resources: 1 })],
      }))
      .mockResolvedValueOnce(tree({
        units: [unit({
          id: 2, state: 'started', completed_resources: 1, total_resources: 1,
          resources: [res({ ...openRes, done: true })],
        })],
      }));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    vi.mocked(api.courses.reportProgress).mockResolvedValue({} as ProgressResponse);

    const { container } = render(
      <MemoryRouter><CourseScreen topBar={topBar} /></MemoryRouter>,
    );

    const openBtn = await waitFor(() => {
      const el = container.querySelector('[data-test="kc-open-btn"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    });
    fireEvent.click(openBtn);

    // The report carries the 5-second viewed marker (must_view).
    await waitFor(() => expect(api.courses.reportProgress).toHaveBeenCalledWith(111, { viewed_seconds: 5 }));
    // The tree re-reads after the report so the done state lands.
    await waitFor(() => expect(api.courses.get).toHaveBeenCalledTimes(2));
  });

  it('submits a min_score practice through the score input', async () => {
    const scored = res({ id: 112, completion_type: 'min_score', min_score: 80, resource_type: 'practice' });
    vi.mocked(api.courses.get).mockResolvedValue(tree({
      units: [unit({ id: 2, state: 'unlocked', resources: [scored], total_resources: 1 })],
    }));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    vi.mocked(api.courses.reportProgress).mockResolvedValue({} as ProgressResponse);

    const { container } = render(
      <MemoryRouter><CourseScreen topBar={topBar} /></MemoryRouter>,
    );

    await waitFor(() => {
      const el = container.querySelector('[data-test="kc-score-input"]');
      expect(el).toBeTruthy();
    });
    fireEvent.change(q(container, '[data-test="kc-score-input"]'), { target: { value: '88' } });
    fireEvent.click(q(container, '[data-test="kc-score-submit"]'));

    await waitFor(() => expect(api.courses.reportProgress).toHaveBeenCalledWith(112, { score: 88 }));
  });

  it('routes task resources to the chore pipeline instead of reporting', async () => {
    const task = res({ id: 113, resource_type: 'task', completion_type: '' });
    vi.mocked(api.courses.get).mockResolvedValue(tree({
      units: [unit({ id: 2, state: 'unlocked', resources: [task], total_resources: 1 })],
    }));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);

    const { container } = render(
      <MemoryRouter><CourseScreen topBar={topBar} /></MemoryRouter>,
    );

    const link = await waitFor(() => {
      const el = container.querySelector('[data-test="kc-resource-task"]');
      expect(el).toBeTruthy();
      return el as HTMLElement;
    });
    expect(link.querySelector('a')!.getAttribute('href')).toBe('/');
    expect(api.courses.reportProgress).not.toHaveBeenCalled();
  });

  it('shows the load failure state', async () => {
    vi.mocked(api.courses.get).mockRejectedValue(new Error('offline'));
    const { container } = render(
      <MemoryRouter><CourseScreen topBar={topBar} /></MemoryRouter>,
    );
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(container.querySelector('[data-test="kid-course-page"]')).toBeTruthy();
  });
});

describe('course practice deep link (TX-COURSE-DEEPLINK)', () => {
  // A generated practice resource pointing at a taxonomy knowledge tag by its
  // stable external_ref: the kid goes straight into that topic's practice with
  // the course progress context.
  const practiceRes = (over: Partial<ResourceView> = {}) => res({
    id: 501, resource_type: 'practice', completion_type: 'min_score', min_score: 80,
    content_ref: 'cnm_p001', ...over,
  });
  const withRes = (r: ResourceView) => tree({
    units: [unit({ id: 2, state: 'started', resources: [r], total_resources: 1 })],
  });

  const linkHref = async (container: HTMLElement): Promise<string | null> => {
    const el = await waitFor(() => {
      const found = container.querySelector('[data-test="kc-practice-link"]');
      expect(found).toBeTruthy();
      return found as HTMLAnchorElement;
    });
    return el.getAttribute('href');
  };

  it('practice resource with an external_ref links to /learn with topic + resourceId + courseId', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(practiceRes()));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt(9);

    const href = await linkHref(container);
    expect(href).toBe('/learn?topic=cnm_p001&resourceId=501&courseId=9');
    // The link is a router Link, not a button that reports progress on click.
    fireEvent.click(q(container, '[data-test="kc-practice-link"]'));
    expect(api.courses.reportProgress).not.toHaveBeenCalled();
  });

  // 生产契约：courseId 取自已加载的课程树（course.id），而不是 URL 的 :id；
  // 这里让 URL(:id) 与树 id 不同，断言链接带的是树的 id（即服务端确认过的课程）。
  it('the courseId in the link is the loaded course id (server-confirmed), not the raw URL :id', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(practiceRes()));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt(42);
    expect(await screen.findByText('Science · Water')).toBeTruthy();
    expect(api.courses.get).toHaveBeenCalledWith(42);
    const href = await linkHref(container);
    expect(href).toBe('/learn?topic=cnm_p001&resourceId=501&courseId=9');
    expect(href).not.toContain('courseId=42');
  });

  // external_ref 走 encodeURIComponent；'.' 是允许出现在 topic 里的字符，原样保留。
  it('the external_ref is URI-encoded in the link', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(practiceRes({ content_ref: 'cnm:p.001' })));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt(9);
    const href = await linkHref(container);
    expect(href).toBe('/learn?topic=cnm%3Ap.001&resourceId=501&courseId=9');
  });

  it.each([
    ['numeric local id', { content_ref: '123' }],
    ['empty content_ref', { content_ref: '' }],
    ['whitespace-only content_ref', { content_ref: '   ' }],
    ['null content_ref', { content_ref: null as unknown as string }],
    ['ref starting with a digit', { content_ref: '1cnm_p001' }],
    ['ref with a space', { content_ref: 'cnm p001' }],
    ['non-safe resource id', { id: Number.MAX_SAFE_INTEGER + 1 }],
    ['zero resource id', { id: 0 }],
    ['negative resource id', { id: -1 }],
    ['non-integer resource id', { id: 1.5 }],
  ])('no deep link for %s: falls back to the score control, never a /learn link', async (_label, over) => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(practiceRes(over)));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt(9);

    await waitFor(() => {
      expect(container.querySelector('[data-test="kc-score-input"]')).toBeTruthy();
    });
    expect(container.querySelector('[data-test="kc-practice-link"]')).toBeNull();
    // No link anywhere pointing at /learn.
    expect(Array.from(container.querySelectorAll('a')).some((a) => (a.getAttribute('href') ?? '').startsWith('/learn'))).toBe(false);
  });

  // URL 里的 :id 形状不安全时，屏幕仍只信任服务端返回的课程树 id，
  // 因此链接依旧只带 tree.id（URL 的非法值不会渗进深链）。
  it('a non-safe / non-positive course id in the URL never leaks into the /learn link', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(practiceRes()));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt('9007199254740993');

    const href = await linkHref(container);
    expect(href).toBe('/learn?topic=cnm_p001&resourceId=501&courseId=9');
    expect(href).not.toContain('9007199254740993');
  });

  it('a non-practice resource with an external_ref content_ref stays a plain row', async () => {
    vi.mocked(api.courses.get).mockResolvedValue(withRes(res({ resource_type: 'page', content_ref: 'cnm_p001' })));
    vi.mocked(api.courses.outcomes).mockResolvedValue([]);
    const { container } = renderAt(9);

    await waitFor(() => {
      expect(container.querySelector('[data-test="kc-open-btn"]')).toBeTruthy();
    });
    expect(container.querySelector('[data-test="kc-practice-link"]')).toBeNull();
  });
});

describe('CourseEntryCard', () => {
  it('shows the course progress and the door into the course', async () => {
    vi.mocked(api.courses.list).mockResolvedValue([tree({
      units: [
        unit({ id: 1, state: 'completed', completed_resources: 2, total_resources: 2 }),
        unit({ id: 2, title: 'Next unit', state: 'started', completed_resources: 1, total_resources: 2 }),
      ],
    })]);
    const { container } = render(<MemoryRouter><CourseEntryCard /></MemoryRouter>);

    await waitFor(() => {
      const el = container.querySelector('[data-test="course-progress-card"]');
      expect(el).toBeTruthy();
    });
    expect(q(container, '[data-test="course-progress-pct"]').textContent).toBe('25%');
    expect(q(container, '[data-test="course-continue-btn"]').getAttribute('href')).toBe('/course/9');
    expect(q(container, '[data-test="course-next-tip"]').textContent).toContain('Next unit');
  });

  it('hides entirely with no published course', async () => {
    vi.mocked(api.courses.list).mockResolvedValue([]);
    const { container } = render(<MemoryRouter><CourseEntryCard /></MemoryRouter>);
    await waitFor(() => expect(api.courses.list).toHaveBeenCalledOnce());
    await new Promise((r) => setTimeout(r, 0));
    expect(container.querySelector('[data-test="course-progress-card"]')).toBeNull();
  });
});
