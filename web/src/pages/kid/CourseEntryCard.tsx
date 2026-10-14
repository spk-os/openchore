// The course entry card on Today (L2): the kid's most in-flight course
// with its progress bar and the door into the course screen. OD
// prototype kid-today.html's course-progress-card is the contract.
// Self-contained on purpose — it fetches its own course list (like
// ReviewEntryCard), so useKidData stays untouched; with no published
// course the card hides entirely.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { CourseTree, UnitView } from '../../types';
import s from './kid.module.css';

/** The unit the kid should open next: the first non-completed one in
 * path order that isn't locked (a locked unit has nothing to do). */
export function nextUnitOf(course: CourseTree): UnitView | null {
  for (const u of course.units) {
    if (u.state === 'completed') continue;
    if (u.state === 'locked') continue;
    return u;
  }
  return null;
}

export function CourseEntryCard() {
  const { t } = useTranslation();
  const [course, setCourse] = useState<CourseTree | null>(null);

  useEffect(() => {
    let alive = true;
    api.courses.list()
      .then(list => { if (alive) setCourse(list[0] ?? null); })
      .catch(() => { if (alive) setCourse(null); });
    return () => { alive = false; };
  }, []);

  // Loading or no published course: no card at all — Today stays lean.
  if (!course) return null;

  const next = nextUnitOf(course);

  return (
    <section className={s.courseEntry} data-test="course-progress-card">
      <span className={s.courseEntryIc} aria-hidden>📖</span>
      <div className={s.courseEntryBd}>
        <div className={s.courseEntryTitle}>
          {course.title}
          <span data-test="course-progress-pct">
            {t('kid.course.entryPct', { count: course.progress_percent })}
          </span>
        </div>
        <div className={s.courseEntryRow}>
          <span
            className={s.pbar}
            role="img"
            aria-label={t('kid.course.progressAria', { count: course.progress_percent })}
            data-test="course-progress-bar"
          >
            <i style={{ width: `${course.progress_percent}%` }} />
          </span>
          <b className={s.courseEntryCount}>
            {t('kid.course.entryResources', {
              done: course.units.reduce((n, u) => n + (u.state === 'completed' ? u.total_resources : u.completed_resources), 0),
              total: course.units.reduce((n, u) => n + u.total_resources, 0),
            })}
          </b>
        </div>
        {next && (
          <p className={s.courseEntryTip} data-test="course-next-tip">
            {t('kid.course.entryTip', {
              unit: next.title,
              done: next.completed_resources,
              total: next.total_resources,
            })}
          </p>
        )}
        <Link className={s.courseEntryBtn} to={`/course/${course.id}`} data-test="course-continue-btn">
          {t('kid.course.entryContinue')}
        </Link>
      </div>
    </section>
  );
}
