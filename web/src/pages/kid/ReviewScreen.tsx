// The kid's spaced-repetition screen (L4): today's due cards run through
// the FSRS scheduler one at a time — flip, then the honest two buttons
// ("忘了" = Again, "记住了" = Good). OD prototype kid-review.html is the
// UI source of truth. Anki's queue semantics live in the backend
// (api/review.go): the queue is computed live, a graded card is done for
// the day, and finishing the whole queue credits +6 points once.
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ReviewItem, ReviewToday } from '../../types';
// 042：学科词表由共享 subjects.ts 表驱动（十学科 + 未分类）。
import { subjectOptionLabel } from '../../subjects';
import { MathText } from '../../components/MathText';
import { Icon } from '../../design';
import s from './kid.module.css';

/** A subject's display name: the ten shared vocabulary translate, the rest stay as-is. */
const subjectLabel = (t: (k: string) => string, subject: string) =>
  subjectOptionLabel(t, subject, subject);

/** "今天" / "3 天后" style relative label for a card's due date. */
function dueWhen(t: ReturnType<typeof useTranslation>['t'], iso: string): string {
  const days = Math.round((new Date(iso).getTime() - Date.now()) / 86400000);
  if (days <= 0) return t('kid.review.today');
  return t('kid.review.inDays', { count: days });
}

interface ReviewCardProps {
  item: ReviewItem;
  index: number;
  total: number;
  onGrade: (item: ReviewItem, rating: 'Again' | 'Good') => void;
  busy: boolean;
}

/** One card mid-session: front → flip → back with the two buttons. */
const ReviewCard: React.FC<ReviewCardProps> = ({ item, index, total, onGrade, busy }) => {
  const { t } = useTranslation();
  const [flipped, setFlipped] = useState(false);

  // Moving to the next card resets the flip for it.
  useEffect(() => { setFlipped(false); }, [item.id]);

  // back_text is "answer\n\nanalysis" (the T+0 hook packs the error item's
  // two fields with a blank line); split them for the two stacked blocks.
  const [answer, analysis] = (item.back_text ?? '').split(/\n\n+/);

  return (
    <article
      className={s.rvCard}
      data-test="rv-card"
      aria-label={t('kid.review.cardAria', {
        subject: subjectLabel(t, item.subject ?? ''),
        front: item.front_text,
      })}
    >
      <header className={s.rvCardHead}>
        <span className={s.rvK} data-test="rv-k">{subjectLabel(t, item.subject ?? '')}</span>
        {item.source_label && (
          <span className={s.rvCardSrc} data-test="rv-card-src">
            {t('kid.review.fromSource', { source: item.source_label })}
          </span>
        )}
      </header>

      <div className={s.rvQ}>
        <p className={s.rvNo} data-test="rv-no">
          {t('kid.review.cardNo', { current: index + 1, total })}
        </p>
        <MathText as="p" className={s.rvF} data-test="rv-f" text={item.front_text} />
      </div>

      {!flipped ? (
        <button
          type="button"
          className={s.rvFlip}
          data-test="rv-flip"
          onClick={() => setFlipped(true)}
        >
          {t('kid.review.flip')}
        </button>
      ) : (
        <div className={s.rvBack} data-test="rv-back">
          {answer && (
            <div className={s.rvAns}>
              <span className={s.rvAnsLabel}>{t('kid.review.answerLabel')}</span>
              <MathText as="p" className={s.rvAnsText} text={answer} />
            </div>
          )}
          {analysis && (
            <div className={s.rvWhy}>
              <span className={s.rvAnsLabel}>{t('kid.review.analysisLabel')}</span>
              <MathText as="p" className={s.rvAnsText} text={analysis} />
            </div>
          )}
          <div className={s.rvKeys} data-test="rv-keys">
            <button
              type="button"
              className={`${s.rvAgain} ${s.rvKey}`}
              data-test="rv-again"
              onClick={() => onGrade(item, 'Again')}
              disabled={busy}
            >
              😅 {t('kid.review.forgot')}
            </button>
            <button
              type="button"
              className={`${s.rvGood} ${s.rvKey}`}
              data-test="rv-good"
              onClick={() => onGrade(item, 'Good')}
              disabled={busy}
            >
              😀 {t('kid.review.remembered')}
            </button>
          </div>
        </div>
      )}
    </article>
  );
};

interface ReviewScreenProps {
  topBar: React.ReactNode;
}

/** The whole session: progress header, the current card, then the queue
 * preview, the honest note and the completion banner. */
export function ReviewScreen({ topBar }: ReviewScreenProps) {
  const { t } = useTranslation();
  const [today, setToday] = useState<ReviewToday | null>(null);
  const [failed, setFailed] = useState(false);
  const [doneCount, setDoneCount] = useState(0);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setFailed(false);
    try {
      const out = await api.review.today();
      setToday(out);
    } catch {
      setFailed(true);
      setToday({ due: [], upcoming: [], streak: 0 });
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  // The queue is live: cards rated today are already excluded server-side,
  // so grading simply reloads and the next card is whatever comes back.
  const onGrade = useCallback(async (item: ReviewItem, rating: 'Again' | 'Good') => {
    setBusy(true);
    try {
      await api.review.grade(item.id, rating);
      setDoneCount(n => n + 1);
      const out = await api.review.today();
      setToday(out);
      if (out.due.length === 0) {
        // Finishing the whole day's queue credits +6 points once (the
        // backend's idempotency key), which is what the banner announces.
        setFailed(false);
      }
    } catch {
      setFailed(true);
    }
    setBusy(false);
  }, []);

  const total = doneCount + (today?.due.length ?? 0);
  const current = today?.due[0];
  const complete = today != null && today.due.length === 0 && doneCount > 0;

  return (
    <div className={s.rvScreen} data-test="kid-review-page">
      {topBar}
      <h1 className={s.screenTitle}>{t('kid.review.title')}</h1>

      {today === null && <p className={s.ebLoading}>{t('kid.review.loading')}</p>}

      {failed && <p className={s.ebError} role="alert">{t('kid.review.loadFailed')}</p>}

      {today !== null && (
        <>
          {/* Progress: done / total for the session, plus the honest ETA. */}
          {!complete && total > 0 && (
            <div className={s.rvProgress}>
              <div
                className={s.rvPb}
                role="img"
                aria-label={t('kid.review.progressAria', { done: doneCount, total })}
                data-test="rv-progress"
              >
                <span className={s.bar}>
                  <i style={{ width: total > 0 ? `${Math.round((doneCount / total) * 100)}%` : '0%' }} />
                </span>
                <span className={s.rvProgressNum} data-test="rv-progress-num">
                  {t('kid.review.progressCount', { done: doneCount, total })}
                </span>
              </div>
              <p className={s.rvEta} data-test="rv-eta">
                {t('kid.review.eta', {
                  minutes: Math.max(1, Math.ceil((today.due.length ?? 0) * 20 / 60)),
                })}
                {' · '}
                {t('kid.review.etaPoints')}
                {(today.streak ?? 0) > 0 && (
                  <> · {t('kid.review.etaStreak', { count: today.streak })}</>
                )}
              </p>
            </div>
          )}

          {/* Mid-session: the current card. */}
          {current && (
            <ReviewCard
              item={current}
              index={doneCount}
              total={total}
              onGrade={onGrade}
              busy={busy}
            />
          )}

          {/* The honest note: no punishment, just an earlier return. */}
          {!complete && current && (
            <p className={s.rvHonestNote} data-test="rv-honest-note">
              {t('kid.review.honestNote')}
            </p>
          )}

          {/* Nothing due at all: reassurance, not an empty stub. */}
          {today.due.length === 0 && doneCount === 0 && (
            <div className={s.ebEmpty} data-test="rv-empty">
              <Icon name="spark" />
              <p className={s.ebEmptyTitle}>{t('kid.review.emptyTitle')}</p>
              <p>{t('kid.review.emptyText')}</p>
            </div>
          )}

          {/* All done: the celebration banner. */}
          {complete && (
            <div className={s.rvDone} role="status" data-test="rv-done">
              <p className={s.rvDoneTitle} data-test="rv-done-title">
                {t('kid.review.doneTitle')}
              </p>
              <p className={s.rvDoneStats} data-test="rv-done-stats">
                {t('kid.review.doneStats', { count: doneCount })}
              </p>
              {(today.streak ?? 0) > 0 && (
                <p className={s.rvStreak} data-test="rv-streak">
                  🔥 {t('kid.review.streakLine', { count: today.streak })}
                </p>
              )}
            </div>
          )}

          {/* The upcoming preview: what the scheduler has waiting. */}
          {(today.upcoming?.length ?? 0) > 0 && (
            <section className={s.rvQueue} aria-label={t('kid.review.queueTitle')} data-test="rv-queue">
              <h2 className={s.rvQueueTitle}>{t('kid.review.queueTitle')}</h2>
              {today.upcoming.map(u => (
                <div key={u.id} className={s.rqRow}>
                  <span className={s.rqIc}><Icon name="book" size={18} /></span>
                  <span className={s.rqTt}>{u.front_text.slice(0, 24)}{(u.front_text.length > 24 ? '…' : '')}</span>
                  <span className={s.rqWhen}>{dueWhen(t, u.due)}</span>
                </div>
              ))}
            </section>
          )}

          <p className={s.rvFoot} data-test="rv-foot">{t('kid.review.foot')}</p>
        </>
      )}
    </div>
  );
}
