// @vitest-environment jsdom
// The parent's course authoring tab (L2): the course list with its
// lifecycle pills, draft → publish, the editor (unit tree + resource
// table + outcomes), and View As Learner.
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/react';
import i18n from '../../i18n';

// The API client is mocked at the module level: the tab must only talk
// to api.admin.listCourses / listUsers / getCourse / publishCourse /
// createUnit / previewCourse (+ users.list for the kid picker).
const { api } = vi.hoisted(() => ({
  api: {
    users: { list: vi.fn() },
    admin: {
      listCourses: vi.fn(),
      getCourse: vi.fn(),
      createCourse: vi.fn(),
      publishCourse: vi.fn(),
      archiveCourse: vi.fn(),
      cloneCourse: vi.fn(),
      previewCourse: vi.fn(),
      createUnit: vi.fn(),
      updateUnit: vi.fn(),
      deleteUnit: vi.fn(),
      createResource: vi.fn(),
      updateResource: vi.fn(),
      deleteResource: vi.fn(),
      createOutcome: vi.fn(),
      deleteOutcome: vi.fn(),
    },
  },
}));
vi.mock('../../api', () => ({ api }));

import type { AdminCourseTree, Course, CourseOutcome, UnitResource, User } from '../../types';
import { CoursesTab } from './CoursesTab';

const kid = (over: Partial<User> = {}): User => ({
  ...(over as User),
  id: 2,
  name: 'Mika',
  role: 'child',
} as User);

const course = (over: Partial<Course> = {}): Course => ({
  id: 9,
  user_id: 2,
  title: 'Science · Water',
  description: '',
  syllabus_md: '',
  status: 'draft',
  is_template: false,
  created_at: '2026-10-14T00:00:00Z',
  updated_at: '2026-10-14T00:00:00Z',
  progress_percent: 0,
  units_total: 2,
  units_completed: 0,
  ...over,
});

const resource = (over: Partial<UnitResource> = {}): UnitResource => ({
  id: 101,
  unit_id: 1,
  title: 'Read: The water cycle',
  position: 1,
  resource_type: 'page',
  content_ref: '',
  completion_type: 'must_view',
  ...over,
});

const adminTree = (over: Partial<AdminCourseTree> = {}): AdminCourseTree => ({
  course: course({ status: 'published' }),
  units: [
    {
      id: 1, course_id: 9, title: 'Unit 1 · Water', position: 1, requirement_type: '',
      require_sequential_progress: false, published: true,
      prerequisite_unit_ids: [], resources: [resource({})],
    },
  ],
  outcomes: [({ id: 31, course_id: 9, title: 'Explains evaporation', mastery_points: 80,
    calculation_method: 'latest', ratings_json: '' } as CourseOutcome)],
  ...over,
});

// The components use data-test (not data-testid): query through the container.
const q = (root: HTMLElement, sel: string) => root.querySelector(sel)!;

beforeAll(async () => { await i18n.changeLanguage('en'); });
afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('CoursesTab', () => {
  it('lists courses with their lifecycle pills and per-status actions', async () => {
    vi.mocked(api.admin.listCourses).mockResolvedValue([
      course({ id: 9, status: 'draft' }),
      course({ id: 10, status: 'published', progress_percent: 40, units_completed: 1 }),
      course({ id: 11, status: 'archived' }),
    ]);
    vi.mocked(api.users.list).mockResolvedValue([kid()]);

    const { container } = render(<CoursesTab />);

    // One card per course with the right pill and the right actions.
    await waitFor(() => {
      const el = container.querySelector('[data-test="pc-course-9"]');
      expect(el).toBeTruthy();
    });
    expect(q(container, '[data-test="pc-course-9"]').textContent).toContain('Draft');
    expect(q(container, '[data-test="pc-course-10"]').textContent).toContain('Published');
    expect(q(container, '[data-test="pc-course-11"]').textContent).toContain('Archived');
    expect(q(container, '[data-test="pc-publish-9"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-archive-10"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-preview-10"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-clone-11"]')).toBeTruthy();
    // The published course carries its progress.
    expect(q(container, '[data-test="pc-course-10"]').textContent).toContain('40%');
  });

  it('opens the editor: unit tree, settings, resource table, outcomes', async () => {
    vi.mocked(api.admin.listCourses).mockResolvedValue([course({ id: 9 })]);
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.getCourse).mockResolvedValue(adminTree());

    const { container } = render(<CoursesTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pc-edit-9"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    // The editor loads: unit tree, the selected unit's settings, the
    // resource table with its predicate, and the mastery line.
    await waitFor(() => {
      const el = container.querySelector('[data-test="pc-editor"]');
      expect(el).toBeTruthy();
    });
    expect(q(container, '[data-test="pc-unit-tree"]').textContent).toContain('Unit 1 · Water');
    expect(q(container, '[data-test="pc-unit-settings"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-prereq-select"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-complete-mode"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-resource-table"]').textContent).toContain('must_view');
    expect(q(container, '[data-test="pc-outcomes"]').textContent).toContain('Explains evaporation');
    expect(q(container, '[data-test="pc-add-unit"]')).toBeTruthy();
    expect(q(container, '[data-test="pc-add-resource"]')).toBeTruthy();
  });

  it('publishes a draft course', async () => {
    vi.mocked(api.admin.listCourses).mockResolvedValue([course({ id: 9, status: 'draft' })]);
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.publishCourse).mockResolvedValue(course({ status: 'published' }));

    const { container } = render(<CoursesTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pc-publish-9"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    await waitFor(() => expect(api.admin.publishCourse).toHaveBeenCalledWith(9));
  });

  it('adds a unit from the editor', async () => {
    vi.mocked(api.admin.listCourses).mockResolvedValue([course({ id: 9 })]);
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.getCourse).mockResolvedValue(adminTree());
    vi.mocked(api.admin.createUnit).mockResolvedValue({
      id: 2, course_id: 9, title: 'New unit', position: 2, requirement_type: '',
      require_sequential_progress: false, published: true,
    });

    const { container } = render(<CoursesTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pc-edit-9"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));
    await waitFor(() => expect(q(container, '[data-test="pc-add-unit"]')).toBeTruthy());
    fireEvent.click(q(container, '[data-test="pc-add-unit"]'));

    await waitFor(() => expect(api.admin.createUnit).toHaveBeenCalledWith(9, {
      title: 'New unit', published: true,
    }));
  });

  it('previews the course as the learner sees it', async () => {
    vi.mocked(api.admin.listCourses).mockResolvedValue([course({ id: 9, status: 'published' })]);
    vi.mocked(api.users.list).mockResolvedValue([kid()]);
    vi.mocked(api.admin.previewCourse).mockResolvedValue({
      id: 9, user_id: 2, title: 'Science · Water', description: '', syllabus_md: '',
      status: 'published', is_template: false, created_at: '2026-10-14T00:00:00Z',
      updated_at: '2026-10-14T00:00:00Z', progress_percent: 0, units_total: 2, units_completed: 0,
      units: [
        { id: 1, course_id: 9, title: 'Unit 1', position: 1, requirement_type: '',
          require_sequential_progress: false, published: true, prerequisite_unit_ids: [],
          state: 'started', resources: [], completed_resources: 0, total_resources: 1 },
        { id: 2, course_id: 9, title: 'Unit 2', position: 2, requirement_type: '',
          require_sequential_progress: false, published: true, prerequisite_unit_ids: [1],
          state: 'locked', lock_explanation: 'Finish "Unit 1" first',
          resources: [], completed_resources: 0, total_resources: 1 },
      ],
    });

    const { container } = render(<CoursesTab />);
    fireEvent.click(await waitFor(() => {
      const el = container.querySelector('[data-test="pc-preview-9"]');
      expect(el).toBeTruthy();
      return el as HTMLButtonElement;
    }));

    // The preview lists the kid's exact tree, locks included.
    await waitFor(() => {
      const el = container.querySelector('[data-test="pc-preview"]');
      expect(el).toBeTruthy();
    });
    expect(q(container, '[data-test="pc-preview-unit-1"]').textContent).toContain('Started');
    expect(q(container, '[data-test="pc-preview-unit-2"]').textContent).toContain('Locked');
  });
});
