// The parent's course authoring tab (L2, Canvas Modules editor): the
// course list with lifecycle pills (draft/published/archived), the
// editor (unit tree + prerequisite/requirement/timed settings + the
// resource table with the four completion predicates), and View As
// Learner preview. OD prototype parent-courses.html is the UI contract.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { AdminCourseTree, Course, CourseOutcome, CourseUnit, UnitResource, UnitView, User } from '../../types';
import ui from './ui.module.css';
import styles from './CoursesTab.module.css';

/** One unit row in the editor's tree, with the admin resources joined. */
type AdminUnit = CourseUnit & { prerequisite_unit_ids: number[]; resources: UnitResource[] };

/** The seven resource types with their icons (OD's ric set). */
const RESOURCE_TYPES = [
  { id: 'page', icon: '📖' },
  { id: 'file', icon: '📄' },
  { id: 'audio', icon: '🔊' },
  { id: 'video', icon: '🎬' },
  { id: 'practice', icon: '✏️' },
  { id: 'link', icon: '🔗' },
  { id: 'task', icon: '📋' },
] as const;

/** The four Canvas completion predicates + display-only. */
const COMPLETION_TYPES = ['', 'must_view', 'min_minutes', 'must_submit', 'min_score'] as const;

/** The readable predicate for the resource table's predicate cell. */
function predicateText(t: (k: string, opts?: any) => string, r: UnitResource): string {
  switch (r.completion_type) {
    case 'must_view': return `must_view · ${t('admin.coursesTab.predMustView')}`;
    case 'min_minutes': return `min_minutes · ${t('admin.coursesTab.predMinMinutes', { minutes: r.min_minutes ?? 0 })}`;
    case 'must_submit': return `must_submit · ${t('admin.coursesTab.predMustSubmit')}`;
    case 'min_score': return `min_score · ${t('admin.coursesTab.predMinScore', { score: r.min_score ?? 0 })}`;
    default: return t('admin.coursesTab.predNone');
  }
}

export const CoursesTab: React.FC = () => {
  const { t } = useTranslation();

  // The course list (with progress), the people (for user_id picks), and
  // the editor's loaded tree.
  const [courses, setCourses] = useState<Course[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);

  // Editor state: the loaded tree + the selected unit + save feedback.
  const [tree, setTree] = useState<AdminCourseTree | null>(null);
  const [unitId, setUnitId] = useState<number | null>(null);
  const [msg, setMsg] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  // Course creation form.
  const [creating, setCreating] = useState(false);
  const [newTitle, setNewTitle] = useState('');
  const [newKid, setNewKid] = useState<number | null>(null);

  // View As Learner preview.
  const [preview, setPreview] = useState<UnitView[] | null>(null);

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);

  const load = useCallback(async () => {
    setLoading(true);
    setFailed(false);
    try {
      const [list, people] = await Promise.all([api.admin.listCourses(), api.users.list()]);
      setCourses(list);
      setUsers(people);
    } catch {
      setFailed(true);
    }
    setLoading(false);
  }, []);

  useEffect(() => { load(); }, [load]);

  // Default the creator's kid once people load.
  useEffect(() => {
    if (newKid === null && kids.length > 0) setNewKid(kids[0].id);
  }, [kids, newKid]);

  const openEditor = useCallback(async (id: number) => {
    setMsg('');
    setError('');
    setPreview(null);
    try {
      const tr = await api.admin.getCourse(id);
      setTree(tr);
      setUnitId(tr.units[0]?.id ?? null);
    } catch {
      setError(t('admin.coursesTab.loadFailed'));
    }
  }, [t]);

  const flash = (m: string) => { setMsg(m); setError(''); };

  // --- Course lifecycle actions ---

  const createCourse = async () => {
    if (!newTitle.trim() || !newKid) return;
    setBusy(true);
    setError('');
    try {
      const c = await api.admin.createCourse({ title: newTitle.trim(), user_id: newKid });
      setNewTitle('');
      setCreating(false);
      await load();
      flash(t('admin.coursesTab.created'));
      openEditor(c.id);
    } catch {
      setError(t('admin.coursesTab.createFailed'));
    }
    setBusy(false);
  };

  const publish = async (c: Course) => {
    setBusy(true);
    try {
      await api.admin.publishCourse(c.id);
      await load();
      if (tree?.course.id === c.id) await openEditor(c.id);
      flash(t('admin.coursesTab.published'));
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const archive = async (c: Course) => {
    setBusy(true);
    try {
      await api.admin.archiveCourse(c.id);
      await load();
      if (tree?.course.id === c.id) setTree(null);
      flash(t('admin.coursesTab.archived'));
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const clone = async (c: Course) => {
    if (kids.length === 0) return;
    setBusy(true);
    try {
      await api.admin.cloneCourse(c.id, kids[0].id, t('admin.coursesTab.cloneTitle', { title: c.title }));
      await load();
      flash(t('admin.coursesTab.cloned'));
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const previewAsLearner = async (c: Course) => {
    setBusy(true);
    setPreview(null);
    try {
      const p = await api.admin.previewCourse(c.id);
      setPreview(p.units);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  // --- Unit actions ---

  const addUnit = async () => {
    if (!tree) return;
    setBusy(true);
    try {
      const u = await api.admin.createUnit(tree.course.id, { title: t('admin.coursesTab.newUnitTitle'), published: true });
      await openEditor(tree.course.id);
      setUnitId(u.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const updateUnitSettings = async (
    u: AdminUnit,
    patch: Partial<CourseUnit> & { prerequisite_unit_ids?: number[] },
  ) => {
    setBusy(true);
    try {
      await api.admin.updateUnit(u.id, {
        title: u.title,
        published: u.published,
        prerequisite_unit_ids: u.prerequisite_unit_ids,
        ...patch,
      });
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const deleteUnit = async (u: AdminUnit) => {
    setBusy(true);
    try {
      await api.admin.deleteUnit(u.id);
      if (unitId === u.id) setUnitId(null);
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  // --- Resource actions ---

  const addResource = async (u: AdminUnit) => {
    setBusy(true);
    try {
      await api.admin.createResource(u.id, {
        title: t('admin.coursesTab.newResourceTitle'),
        resource_type: 'page',
        content_ref: '',
        completion_type: 'must_view',
      });
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const patchResource = async (r: UnitResource, patch: Partial<UnitResource>) => {
    setBusy(true);
    try {
      await api.admin.updateResource(r.id, {
        title: r.title,
        resource_type: r.resource_type,
        content_ref: r.content_ref,
        completion_type: r.completion_type,
        min_score: r.min_score ?? undefined,
        min_minutes: r.min_minutes ?? undefined,
        ...patch,
      });
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const deleteResource = async (r: UnitResource) => {
    setBusy(true);
    try {
      await api.admin.deleteResource(r.id);
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  // --- Outcome actions ---

  const addOutcome = async () => {
    if (!tree) return;
    setBusy(true);
    try {
      await api.admin.createOutcome(tree.course.id, {
        title: t('admin.coursesTab.newOutcomeTitle'),
        mastery_points: 80,
        calculation_method: 'latest',
      });
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const deleteOutcome = async (o: CourseOutcome) => {
    setBusy(true);
    try {
      await api.admin.deleteOutcome(o.id);
      await openEditor(tree!.course.id);
    } catch {
      setError(t('admin.coursesTab.actionFailed'));
    }
    setBusy(false);
  };

  const currentUnit = tree?.units.find(u => u.id === unitId) ?? null;

  return (
    <div className={styles.wrap} data-test="admin-courses-page">
      {/* Page head + lifecycle buttons. */}
      <div className={styles.ph} data-test="courses-ph">
        <div>
          <h1 className={ui.pageTitle}>{t('admin.coursesTab.heading')}</h1>
          <p className={ui.pageSub}>{t('admin.coursesTab.subtitle')}</p>
        </div>
        <div className={styles.phActions}>
          <button type="button" className={ui.btn} onClick={() => setCreating(v => !v)} data-test="pc-new-course">
            {t('admin.coursesTab.newCourse')}
          </button>
        </div>
      </div>

      {msg && <p className={ui.msg} role="status">{msg}</p>}
      {error && <p className={ui.msgError} role="alert">{error}</p>}

      {/* Create form. */}
      {creating && (
        <div className={ui.card} data-test="pc-create-form">
          <label className={ui.label}>{t('admin.coursesTab.courseTitle')}</label>
          <input className={ui.input} value={newTitle} onChange={e => setNewTitle(e.target.value)} data-test="pc-create-title" />
          <label className={ui.label}>{t('admin.coursesTab.assignTo')}</label>
          <select
            className={ui.input}
            value={newKid ?? ''}
            onChange={e => setNewKid(Number(e.target.value))}
            data-test="pc-create-kid"
          >
            {kids.map(k => <option key={k.id} value={k.id}>{k.name}</option>)}
          </select>
          <button type="button" className={ui.btnPrimary} disabled={busy || !newTitle.trim()} onClick={createCourse} data-test="pc-create-btn">
            {t('admin.coursesTab.createBtn')}
          </button>
        </div>
      )}

      {/* The course list: lifecycle pills + progress + per-course actions. */}
      <section data-test="pc-list">
        {loading && <p className={ui.msgWaiting}>{t('admin.coursesTab.loading')}</p>}
        {failed && <p className={ui.msgError} role="alert">{t('admin.coursesTab.loadFailed')}</p>}
        {!loading && !failed && courses.length === 0 && (
          <div className={ui.empty}>
            <p>{t('admin.coursesTab.empty')}</p>
            <p>{t('admin.coursesTab.emptyHint')}</p>
          </div>
        )}
        <div className={styles.courseGrid}>
          {courses.map(c => (
            <article
              key={c.id}
              className={clsx(styles.courseCard,
                c.status === 'draft' && styles.courseCardDraft,
                c.status === 'archived' && styles.courseCardArchived)}
              data-test={`pc-course-${c.id}`}
            >
              <div className={styles.courseHead}>
                <span className={styles.courseIc} aria-hidden>📖</span>
                <div className={styles.courseBd}>
                  <div className={styles.courseTitle}>
                    {c.title}
                    <span className={clsx(styles.pill,
                      c.status === 'published' && styles.pillGreen,
                      c.status === 'draft' && styles.pillYellow)}>
                      {t(`admin.coursesTab.status_${c.status}`)}
                    </span>
                  </div>
                  <div className={styles.courseMeta}>
                    {t('admin.coursesTab.unitsMeta', { units: c.units_total, done: c.units_completed })}
                  </div>
                </div>
              </div>
              <div className={styles.pbar} role="img" aria-label={t('kid.course.progressAria', { count: c.progress_percent })}>
                <i style={{ width: `${c.progress_percent}%` }} />
              </div>
              <div className={styles.courseMeta}>
                <b>{c.progress_percent}%</b>
              </div>
              <div className={styles.courseFoot}>
                <button type="button" className={ui.btn} onClick={() => openEditor(c.id)} data-test={`pc-edit-${c.id}`}>
                  {t('admin.coursesTab.edit')}
                </button>
                {c.status === 'draft' && (
                  <button type="button" className={ui.btnPrimary} disabled={busy} onClick={() => publish(c)} data-test={`pc-publish-${c.id}`}>
                    {t('admin.coursesTab.publish')}
                  </button>
                )}
                {c.status === 'published' && (
                  <>
                    <button type="button" className={ui.btn} disabled={busy} onClick={() => previewAsLearner(c)} data-test={`pc-preview-${c.id}`}>
                      {t('admin.coursesTab.viewAsLearner')}
                    </button>
                    <button type="button" className={ui.btn} disabled={busy} onClick={() => archive(c)} data-test={`pc-archive-${c.id}`}>
                      {t('admin.coursesTab.archive')}
                    </button>
                  </>
                )}
                {c.status === 'archived' && (
                  <button type="button" className={ui.btn} disabled={busy} onClick={() => clone(c)} data-test={`pc-clone-${c.id}`}>
                    {t('admin.coursesTab.clone')}
                  </button>
                )}
              </div>
            </article>
          ))}
        </div>
      </section>

      {/* View As Learner: the kid's exact tree. */}
      {preview && (
        <section className={ui.card} data-test="pc-preview">
          <h2 className={ui.sectionTitle}>{t('admin.coursesTab.previewTitle')}</h2>
          <div className={styles.previewUnits}>
            {preview.map(u => (
              <div key={u.id} className={clsx(styles.previewUnit, u.state === 'locked' && styles.previewUnitLocked)}
                data-test={`pc-preview-unit-${u.id}`}>
                <b>{u.title}</b>
                <span>{t(`admin.coursesTab.unitState_${u.state}`)}</span>
                {u.lock_explanation && <small>{u.lock_explanation}</small>}
              </div>
            ))}
          </div>
        </section>
      )}

      {/* The editor: unit tree + unit settings + resource table + outcomes. */}
      {tree && (
        <section className={ui.card} data-test="pc-editor">
          <h2 className={ui.sectionTitle}>
            {t('admin.coursesTab.editorTitle', { title: tree.course.title })}
          </h2>
          <p className={ui.sectionDesc}>{t('admin.coursesTab.editorDesc')}</p>
          <div className={styles.editor}>
            <div className={styles.unitTree} data-test="pc-unit-tree">
              {tree.units.map((u, i) => (
                <React.Fragment key={u.id}>
                  {i > 0 && <div className={styles.arrowDown} aria-hidden>↓ {t('admin.coursesTab.arrowPrereq')}</div>}
                  <button
                    type="button"
                    className={clsx(styles.unitNode, unitId === u.id && styles.unitNodeCur)}
                    data-test={`pc-unit-${u.id}`}
                    onClick={() => setUnitId(u.id)}
                  >
                    {u.title}
                    <span className={styles.unitSub}>
                      {t('admin.coursesTab.unitNodeMeta', {
                        count: u.resources.length,
                        prereq: u.prerequisite_unit_ids.length > 0
                          ? t('admin.coursesTab.hasPrereq')
                          : t('admin.coursesTab.noPrereq'),
                      })}
                    </span>
                  </button>
                </React.Fragment>
              ))}
              <button type="button" className={ui.btn} onClick={addUnit} disabled={busy} data-test="pc-add-unit">
                {t('admin.coursesTab.addUnit')}
              </button>
            </div>

            {currentUnit && (
              <div className={styles.unitDetail} data-test="pc-unit-detail">
                {/* Unit settings: prerequisites, requirement type, unlock time. */}
                <div className={styles.setLine} data-test="pc-unit-settings">
                  <div className={styles.setItem}>
                    <b>{t('admin.coursesTab.prereqLabel')}</b>
                    <select
                      className={ui.input}
                      value={currentUnit.prerequisite_unit_ids[0] ?? ''}
                      onChange={e => updateUnitSettings(currentUnit, {
                        prerequisite_unit_ids: e.target.value === '' ? [] : [Number(e.target.value)],
                      })}
                      data-test="pc-prereq-select"
                    >
                      <option value="">{t('admin.coursesTab.noPrereq')}</option>
                      {tree.units
                        .filter(u => u.id !== currentUnit.id)
                        .map(u => <option key={u.id} value={u.id}>{u.title}</option>)}
                    </select>
                  </div>
                  <div className={styles.setItem}>
                    <b>{t('admin.coursesTab.reqLabel')}</b>
                    <select
                      className={ui.input}
                      value={currentUnit.requirement_type}
                      onChange={e => updateUnitSettings(currentUnit, {
                        requirement_type: e.target.value as CourseUnit['requirement_type'],
                      })}
                      data-test="pc-complete-mode"
                    >
                      <option value="">{t('admin.coursesTab.reqAll')}</option>
                      <option value="one">{t('admin.coursesTab.reqOneOption')}</option>
                    </select>
                  </div>
                  <div className={styles.setItem}>
                    <b>{t('admin.coursesTab.unlockLabel')}</b>
                    <input
                      type="datetime-local"
                      className={ui.input}
                      value={currentUnit.unlock_at ? currentUnit.unlock_at.slice(0, 16) : ''}
                      onChange={e => updateUnitSettings(currentUnit, {
                        unlock_at: e.target.value ? new Date(e.target.value).toISOString() : undefined,
                      })}
                      data-test="pc-unlock-time"
                    />
                  </div>
                  <button type="button" className={ui.btnDanger} onClick={() => deleteUnit(currentUnit)} disabled={busy} data-test="pc-del-unit">
                    {t('admin.coursesTab.deleteUnit')}
                  </button>
                  <button
                    type="button"
                    className={ui.btnGhost}
                    disabled={busy}
                    onClick={() => {
                      const title = window.prompt(t('admin.coursesTab.unitNamePrompt'), currentUnit.title);
                      if (title && title.trim()) updateUnitSettings(currentUnit, { title: title.trim() });
                    }}
                    data-test="pc-rename-unit"
                  >
                    {t('admin.coursesTab.renameUnit')}
                  </button>
                </div>

                {/* The resource table: one row per resource with its predicate. */}
                <table className={styles.resTable} data-test="pc-resource-table">
                  <thead>
                    <tr>
                      <th>{t('admin.coursesTab.colResource')}</th>
                      <th>{t('admin.coursesTab.colPredicate')}</th>
                      <th>{t('admin.coursesTab.colActions')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {currentUnit.resources.map(r => (
                      <tr key={r.id} data-test={`pc-res-${r.id}`}>
                        <td>
                          <div className={styles.resTitle}>{r.title}</div>
                        </td>
                        <td>
                          <div className={styles.predCtl}>
                            <span className={styles.pred}>{predicateText(t, r)}</span>
                            <select
                              className={ui.input}
                              value={r.completion_type}
                              onChange={e => {
                                const ct = e.target.value;
                                patchResource(r, {
                                  completion_type: ct as UnitResource['completion_type'],
                                  min_score: ct === 'min_score' ? (r.min_score ?? 80) : null,
                                  min_minutes: ct === 'min_minutes' ? (r.min_minutes ?? 30) : null,
                                });
                              }}
                              data-test={`pc-res-pred-${r.id}`}
                            >
                              {COMPLETION_TYPES.map(ct => (
                                <option key={ct} value={ct}>{ct === '' ? t('admin.coursesTab.predNone') : ct}</option>
                              ))}
                            </select>
                            {r.completion_type === 'min_score' && (
                              <input
                                type="number"
                                className={ui.input}
                                value={r.min_score ?? 80}
                                min={0}
                                max={100}
                                onChange={e => patchResource(r, { min_score: Number(e.target.value) })}
                                data-test={`pc-res-minscore-${r.id}`}
                              />
                            )}
                            {r.completion_type === 'min_minutes' && (
                              <input
                                type="number"
                                className={ui.input}
                                value={r.min_minutes ?? 30}
                                min={1}
                                onChange={e => patchResource(r, { min_minutes: Number(e.target.value) })}
                                data-test={`pc-res-minminutes-${r.id}`}
                              />
                            )}
                          </div>
                        </td>
                        <td>
                          <div className={styles.resActions}>
                            <button
                              type="button"
                              className={ui.btnGhost}
                              onClick={() => {
                                const title = window.prompt(t('admin.coursesTab.resNamePrompt'), r.title);
                                if (title && title.trim()) patchResource(r, { title: title.trim() });
                              }}
                            >
                              {t('admin.coursesTab.rename')}
                            </button>
                            <select
                              className={ui.input}
                              value={r.resource_type}
                              onChange={e => patchResource(r, { resource_type: e.target.value as UnitResource['resource_type'] })}
                              data-test={`pc-res-type-${r.id}`}
                            >
                              {RESOURCE_TYPES.map(rt => (
                                <option key={rt.id} value={rt.id}>{rt.icon} {rt.id}</option>
                              ))}
                            </select>
                            <button type="button" className={ui.btnDanger} onClick={() => deleteResource(r)} disabled={busy} data-test={`pc-res-del-${r.id}`}>
                              {t('admin.coursesTab.delete')}
                            </button>
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                <div className={styles.resFoot}>
                  <button type="button" className={ui.btn} onClick={() => addResource(currentUnit)} disabled={busy} data-test="pc-add-resource">
                    {t('admin.coursesTab.addResource')}
                  </button>
                </div>

                {/* The mastery lines (Canvas Outcomes). */}
                <div className={styles.outcomes} data-test="pc-outcomes">
                  <b>{t('admin.coursesTab.outcomesLabel')}</b>
                  {tree.outcomes.map(o => (
                    <div key={o.id} className={styles.outcomeRow}>
                      <span>{o.title}</span>
                      <small>{t('admin.coursesTab.outcomeMeta', {
                        points: o.mastery_points,
                        method: o.calculation_method,
                      })}</small>
                      <button type="button" className={ui.btnDanger} onClick={() => deleteOutcome(o)} disabled={busy}>
                        {t('admin.coursesTab.delete')}
                      </button>
                    </div>
                  ))}
                  <button type="button" className={ui.btn} onClick={addOutcome} disabled={busy} data-test="pc-add-outcome">
                    {t('admin.coursesTab.addOutcome')}
                  </button>
                </div>
              </div>
            )}
          </div>
        </section>
      )}
    </div>
  );
};
