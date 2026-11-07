// The kid's course screen (L2): the in-flight course card up top, the
// unit path map (done → current → locked with the lock explanation), and
// progress reporting on each resource — the unlock engine's write path.
// OD prototype kid-course.html is the UI source of truth; Canvas
// Modules' lock_explanation shows as the "为什么锁着？" bubble.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { CourseTree, OutcomeView, ResourceView, UnitView } from '../../types';
import s from './kid.module.css';

/** One resource type's icon, matching the OD prototype's rows. */
const resourceIcon = (type: string) => {
  switch (type) {
    case 'audio': return '🔊';
    case 'video': return '🎬';
    case 'file': return '📄';
    case 'page': return '📖';
    case 'practice': return '✏️';
    case 'link': return '🔗';
    case 'task': return '📋';
    default: return '📚';
  }
};

/** Stable taxonomy external_ref (cnm_p001 / cnk_p001 etc.), never a local DB integer id. */
const TOPIC_EXTERNAL_REF_RE = /^[A-Za-z][A-Za-z0-9_.:-]*$/;
function isTaxonomyExternalRef(value: string): boolean {
  return TOPIC_EXTERNAL_REF_RE.test(value);
}

/** The completion predicate the kid can read: what "done" means here. */
function predicateLabel(
  t: ReturnType<typeof useTranslation>['t'],
  r: ResourceView,
): string {
  switch (r.completion_type) {
    case 'must_view': return t('kid.course.predMustView');
    case 'must_submit': return t('kid.course.predMustSubmit');
    case 'min_score': return t('kid.course.predMinScore', { score: r.min_score ?? 0 });
    case 'min_minutes': return t('kid.course.predMinMinutes', { minutes: r.min_minutes ?? 0 });
    default: return t('kid.course.predNone');
  }
}

interface ResourceRowProps {
  resource: ResourceView;
  courseId: number;
  onReport: (r: ResourceView, report: { viewed_seconds?: number; score?: number; submitted_ref?: string }) => void;
  busy: boolean;
}

/** One resource row: icon, title, the readable predicate, the action.
 * Task-type resources route to the chore pipeline (photo check-in), the
 * rest report progress straight to the unlock engine. */
const ResourceRow: React.FC<ResourceRowProps> = ({ resource: r, courseId, onReport, busy }) => {
  const { t } = useTranslation();
  const [score, setScore] = useState('');
  // A generated practice resource points at a taxonomy knowledge tag by its
  // stable external_ref. Send the kid straight into that topic with the course
  // progress context; numeric content_ref values are local DB ids, never links.
  const topicRef = (r.content_ref ?? '').trim();
  const deepLink = r.resource_type === 'practice'
    && isTaxonomyExternalRef(topicRef)
    && Number.isSafeInteger(r.id) && r.id > 0
    && Number.isSafeInteger(courseId) && courseId > 0
    ? `/learn?topic=${encodeURIComponent(topicRef)}&resourceId=${r.id}&courseId=${courseId}`
    : null;

  // must_view / min_minutes resources complete on open (one tap).
  const open = () => onReport(r, {
    viewed_seconds: r.completion_type === 'min_minutes' ? (r.min_minutes ?? 1) * 60 : 5,
  });

  if (r.resource_type === 'task') {
    return (
      <div className={s.kcResource} data-test={`kc-resource-${r.resource_type}`}>
        <span className={s.kcResourceIc} aria-hidden>{resourceIcon(r.resource_type)}</span>
        <div className={s.kcResourceBd}>
          <div className={s.kcResourceTt}>{r.title}</div>
          <div className={s.kcResourceSub}>{t('kid.course.taskHint')}</div>
        </div>
        <Link className={s.kcResourceSt} to="/">{t('kid.course.taskGo')}</Link>
      </div>
    );
  }

  return (
    <div className={s.kcResource} data-test={`kc-resource-${r.id}`}>
      <span className={s.kcResourceIc} aria-hidden>{resourceIcon(r.resource_type)}</span>
      <div className={s.kcResourceBd}>
        <div className={s.kcResourceTt}>{r.title}</div>
        <div className={s.kcResourceSub}>
          {predicateLabel(t, r)}
          {r.done ? ` · ${t('kid.course.resDone')}` : ''}
        </div>
      </div>
      {deepLink ? (
        <Link
          className={s.kcResourceSt}
          to={deepLink}
          data-test="kc-practice-link"
          aria-label={t('kid.course.submitBtn')}
        >
          {t('kid.course.submitBtn')}
        </Link>
      ) : r.done ? (
        <span className={`${s.kcResourceSt} ${s.kcResourceStOk}`}>✓ {t('kid.course.resDoneShort')}</span>
      ) : r.completion_type === 'min_score' ? (
          <span className={s.kcScoreBox}>
            <input
              type="number"
              inputMode="numeric"
              min={0}
              max={100}
              className={s.kcScoreInput}
              data-test="kc-score-input"
              value={score}
              onChange={e => setScore(e.target.value)}
              aria-label={t('kid.course.scoreAria')}
            />
            <button
              type="button"
              className={s.kcScoreBtn}
              data-test="kc-score-submit"
              disabled={busy || score === ''}
              onClick={() => onReport(r, { score: Number(score) })}
            >
              {t('kid.course.scoreSubmit')}
            </button>
          </span>
      ) : r.completion_type === 'must_submit' ? (
        <button
          type="button"
          className={s.kcResourceSt}
          data-test="kc-submit-btn"
          disabled={busy}
          onClick={() => onReport(r, { submitted_ref: 'kid' })}
        >
          {t('kid.course.submitBtn')}
        </button>
      ) : (
        <button
          type="button"
          className={s.kcResourceSt}
          data-test="kc-open-btn"
          disabled={busy}
          onClick={open}
        >
          {t('kid.course.openBtn')}
        </button>
      )}
    </div>
  );
};

interface UnitNodeProps {
  unit: UnitView;
  courseId: number;
  onReport: (r: ResourceView, report: { viewed_seconds?: number; score?: number; submitted_ref?: string }) => void;
  busy: boolean;
}

/** One unit in the path: done / current (with the resource rows) /
 * locked (with the lock explanation bubble). */
const UnitNode: React.FC<UnitNodeProps> = ({ unit: u, courseId, onReport, busy }) => {
  const { t } = useTranslation();
  const cls = u.state === 'completed' ? s.kcUnitDone
    : u.state === 'locked' ? s.kcUnitLocked
    : u.state === 'started' ? `${s.kcUnitCurrent} ${s.kcUnitCur}` : s.kcUnitCurrent;
  const badge = u.state === 'completed'
    ? <span className={`${s.kcBadge} ${s.kcBadgeOk}`}>{t('kid.course.unitDone')}</span>
    : u.state === 'locked'
      ? <span className={s.kcBadge}>{t('kid.course.unitLocked')}</span>
      : u.state === 'started'
        ? <span className={`${s.kcBadge} ${s.kcBadgeCur}`}>{t('kid.course.unitProgress', { done: u.completed_resources, total: u.total_resources })}</span>
        : <span className={s.kcBadge}>{t('kid.course.unitUnlocked')}</span>;
  const icon = u.state === 'completed' ? '✅' : u.state === 'locked' ? '🔒' : '▶️';

  return (
    <article className={cls} data-test="kc-unit" data-state={u.state} aria-label={u.title}>
      <div className={s.kcUnitHead}>
        <span className={s.kcUnitTitle}>{icon} {u.title}</span>
        {badge}
      </div>
      <div className={s.kcUnitMeta}>
        {t('kid.course.unitMeta', { count: u.total_resources })}
        {u.requirement_type === 'one' ? ` · ${t('kid.course.reqOne')}` : ''}
      </div>
      {u.state === 'locked' && u.lock_explanation && (
        <div className={s.kcLockBubble} data-test="kc-lock-explanation" role="note">
          <b>{t('kid.course.whyLocked')}</b>{u.lock_explanation}
        </div>
      )}
      {(u.state === 'unlocked' || u.state === 'started') && (
        <div className={s.kcResources} data-test="kc-resources">
          {u.resources.map(r => (
            <ResourceRow key={r.id} resource={r} courseId={courseId} onReport={onReport} busy={busy} />
          ))}
          {u.resources.length === 0 && (
            <p className={s.kcUnitEmpty}>{t('kid.course.unitNoResources')}</p>
          )}
        </div>
      )}
    </article>
  );
};

interface CourseScreenProps {
  topBar: React.ReactNode;
}

/** The whole course: the golden course card (progress + continue hint),
 * the unit path map, the mastery (outcomes) lines and the footer. */
export function CourseScreen({ topBar }: CourseScreenProps) {
  const { t } = useTranslation();
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [course, setCourse] = useState<CourseTree | null>(null);
  const [outcomes, setOutcomes] = useState<OutcomeView[]>([]);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');

  const load = useCallback(async () => {
    setFailed(false);
    try {
      const tree = await api.courses.get(Number(id));
      setCourse(tree);
      try {
        setOutcomes(await api.courses.outcomes(Number(id)));
      } catch { setOutcomes([]); }
    } catch {
      setFailed(true);
      setCourse(null);
    }
  }, [id]);

  useEffect(() => { load(); }, [load]);

  // The unlock engine's write path: report, then re-read the tree so
  // the next unit's unlock lands in front of the kid immediately.
  const onReport = useCallback(async (
    r: ResourceView,
    report: { viewed_seconds?: number; score?: number; submitted_ref?: string },
  ) => {
    setBusy(true);
    setNote('');
    try {
      await api.courses.reportProgress(r.id, report);
      await load();
      setNote(t('kid.course.reported'));
    } catch {
      setNote(t('kid.course.reportFailed'));
    }
    setBusy(false);
  }, [load, t]);

  // The unit to continue in: first non-completed, non-locked in path order.
  const next = useMemo(
    () => course ? (course.units.find(u => u.state !== 'completed' && u.state !== 'locked') ?? null) : null,
    [course],
  );

  if (!course) {
    return (
      <div className={s.kcScreen} data-test="kid-course-page">
        {topBar}
        <h1 className={s.screenTitle}>{t('kid.course.title')}</h1>
        {failed
          ? <p className={s.ebError} role="alert">{t('kid.course.loadFailed')}</p>
          : <p className={s.ebLoading}>{t('kid.course.loading')}</p>}
      </div>
    );
  }

  return (
    <div className={s.kcScreen} data-test="kid-course-page">
      {topBar}
      <h1 className={s.screenTitle}>{t('kid.course.title')}</h1>

      {/* The golden course card: progress and the continue door. */}
      <section className={s.kcCourseCard} data-test="kc-course-card" aria-label={course.title}>
        <div className={s.kcCourseHead}>
          <span className={s.kcCourseIc} aria-hidden>📖</span>
          <div className={s.kcCourseBd}>
            <div className={s.kcCourseTitle}>{course.title}</div>
            <div className={s.kcCourseMeta}>
              <span>{t('kid.course.unitsCount', { count: course.units_total })}</span>
              <span>·</span>
              <span>{t('kid.course.byParents')}</span>
            </div>
          </div>
        </div>
        <div className={s.kcCourseProgress} data-test="kc-progress" role="img"
          aria-label={t('kid.course.progressAria', { count: course.progress_percent })}>
          <span className={s.pbar}><i style={{ width: `${course.progress_percent}%` }} /></span>
          <b>{course.progress_percent}%</b>
        </div>
        {next && (
          <div className={s.kcCurrentUnit} data-test="kc-current-unit">
            {t('kid.course.currentUnit', { unit: next.title })}
            <span>
              {t('kid.course.currentUnitSub', {
                done: next.completed_resources,
                total: next.total_resources,
              })}
            </span>
          </div>
        )}
        <div className={s.kcCourseActions}>
          {next ? (
            <button
              type="button"
              className={s.kcContinueBtn}
              data-test="kc-continue-btn"
              onClick={() => {
                const el = document.querySelector('[data-test="kc-unit"][data-state="started"], [data-test="kc-unit"][data-state="unlocked"]');
                el?.scrollIntoView({ block: 'center', behavior: 'smooth' });
              }}
            >
              {t('kid.course.continue')} →
            </button>
          ) : (
            <button
              type="button"
              className={s.kcContinueBtn}
              data-test="kc-continue-btn"
              onClick={() => navigate('/')}
            >
              {t('kid.course.backToday')}
            </button>
          )}
        </div>
      </section>

      {/* The unit path map: done → current → locked. */}
      <section className={s.kcPathSection}>
        <div className={s.kcSectionHead}>{t('kid.course.pathTitle')}</div>
        <div className={s.kcPathMap} data-test="kc-path-map">
          {course.units.map(u => (
            <UnitNode key={u.id} unit={u} courseId={course.id} onReport={onReport} busy={busy} />
          ))}
          {course.units.length === 0 && (
            <p className={s.kcUnitEmpty}>{t('kid.course.emptyUnits')}</p>
          )}
        </div>
      </section>

      {/* The mastery lines (Canvas Outcomes): the rolled-up score each. */}
      {outcomes.length > 0 && (
        <section className={s.kcPathSection}>
          <div className={s.kcSectionHead}>{t('kid.course.outcomesTitle')}</div>
          <div className={s.kcOutcomes} data-test="kc-outcomes">
            {outcomes.map(o => (
              <div key={o.id} className={s.kcOutcome} data-test={`kc-outcome-${o.id}`}>
                <span className={s.kcOutcomeTitle}>{o.title}</span>
                <span className={s.kcOutcomeScore}>
                  {o.score != null
                    ? (o.mastered ? `✓ ${o.score}${t('kid.course.masteredTag')}` : `${o.score}${t('kid.course.ptsUnit')}`)
                    : t('kid.course.noAssessment')}
                </span>
              </div>
            ))}
          </div>
        </section>
      )}

      <p className={s.kcFoot} data-test="kc-note">{t('kid.course.foot')}</p>
      {note && <p className={s.kcNote} role="status">{note}</p>}
    </div>
  );
}
