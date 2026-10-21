// The kid's wrong-question book (L1): every question the parent's paper
// analysis flagged, one card each — question, what the kid wrote, the AI
// explanation, and the "I fixed it" retry that moves the three-state
// mastery machine (New → Reviewing → Mastered). OD prototype
// kid-errorbook.html is the UI source of truth.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ErrorItem } from '../../types';
import { MathText } from '../../components/MathText';
import { Icon } from '../../design';
import s from './kid.module.css';

/** The four AI error types plus the manual-queue placeholder. */
const errorTypeLabel = (t: (k: string) => string, type?: string) => {
  switch (type) {
    case 'calculation': return t('kid.errorbook.errorTypeCalculation');
    case 'concept': return t('kid.errorbook.errorTypeConcept');
    case 'reading': return t('kid.errorbook.errorTypeReading');
    case 'method': return t('kid.errorbook.errorTypeMethod');
    default: return t('kid.errorbook.errorTypeUnknown');
  }
};

/** The OD eb-cause-{type} suffix: the cause chip's test hook names the
 *  error type (calc / concept / read / method / unknown). */
const causeSuffix = (type?: string) => {
  switch (type) {
    case 'calculation': return 'calc';
    case 'concept': return 'concept';
    case 'reading': return 'read';
    case 'method': return 'method';
    default: return 'unknown';
  }
};

/** A subject's display name: the known five translate, the rest stay as-is. */
const subjectLabel = (t: (k: string) => string, subject: string) => {
  const known = ['math', 'chinese', 'english', 'physics', 'chemistry'];
  return known.includes(subject) ? t(`admin.analysis.subject_${subject}`) : subject;
};

/** The mastery badge: 🆕 / 🔁 / ✅ — the card's most scannable signal. */
const masteryClass = (m: number) =>
  m === 2 ? 'mastered' : m === 1 ? 'reviewing' : 'new';

/** The mastery bar's fill: the OD prototype's three-state mapping
 *  (new ≈ just recorded, reviewing = the 45% midline, mastered = full). */
const masteryPercent = (m: number) => (m === 2 ? 100 : m === 1 ? 45 : 10);

/** "明天" / "3 天后" style relative label for the next review date. */
function reviewWhen(t: ReturnType<typeof useTranslation>['t'], iso?: string): string {
  if (!iso) return '';
  const days = Math.round((new Date(iso).getTime() - Date.now()) / 86400000);
  if (days <= 0) return t('kid.errorbook.today');
  return t('kid.errorbook.nextReview', { when: t('kid.errorbook.inDays', { count: days }) });
}

interface ErrorCardProps {
  item: ErrorItem;
  onDone: (item: ErrorItem) => void;
  /** The profile one-liner (「视觉型 + 鼓励语气」) shown above the ladder. */
  configSummary?: string;
}

/** One wrong question, expanded by default: the OD "eb-card-expanded". */
const ErrorCard: React.FC<ErrorCardProps> = ({ item, onDone, configSummary }) => {
  const { t } = useTranslation();
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState('');
  // Which rung of the scaffold ladder we're on (0 = not started; the
  // server clamps to 1..4 and always tells us the real stage back).
  const [attempt, setAttempt] = useState(0);

  // "我订正完了": report the retry and let the mastery machine move.
  const markDone = async () => {
    setBusy(true);
    setNote('');
    try {
      const updated = await api.errorItems.submitReviewResult(item.id, true);
      onDone(updated);
      setNote(updated.mastery_level === 2
        ? t('kid.errorbook.markedMastered')
        : t('kid.errorbook.markedReviewing'));
    } catch {
      setNote(t('kid.errorbook.markDoneFailed'));
    }
    setBusy(false);
  };

  // "还是不懂，再讲讲": walk the Khanmigo-style scaffold ladder — every
  // click asks the server for the next rung (hint → specific hint →
  // decomposition → worked answer). Server-side staging means a refresh
  // can't skip to the answer.
  const again = async () => {
    setBusy(true);
    try {
      const hint = await api.learner.scaffold(item.id, attempt + 1);
      setAttempt(hint.stage);
      setNote(t('kid.errorbook.scaffoldHint', { stage: hint.stage, hint: hint.hint }));
    } catch {
      setNote(t('kid.errorbook.keepGoing'));
    }
    setBusy(false);
  };

  const tags = (item.tags ?? []).map(x => x.name);
  return (
    <article
      className={s.ebCard}
      data-test={`eb-card-${item.id}`}
      data-mastery={masteryClass(item.mastery_level)}
      aria-label={t('kid.errorbook.title') + ' · ' + (item.question_text ?? '')}
    >
      <header className={s.ebCardHead}>
        <div className={s.ebCardMeta}>
          <span className={s.ebSubject}>{subjectLabel(t, item.subject)}</span>
          <span className={s.ebCause} data-test={`eb-cause-${causeSuffix(item.error_type)}`}>
            {errorTypeLabel(t, item.error_type)}
          </span>
          {/* 任务②：哪天、哪个任务错的——错题卡的「来历」行，让孩子（和
              家长）知道这道题为什么在这里。 */}
          {(item.errored_date || item.chore_title) && (
            <span className={s.ebTrace} data-test="eb-trace">
              {item.errored_date && t('kid.errorbook.erroredOn', { date: item.errored_date })}
              {item.errored_date && item.chore_title && ' · '}
              {item.chore_title && t('kid.errorbook.fromChore', { title: item.chore_title })}
            </span>
          )}
        </div>
        <span
          className={`${s.ebMastery} ${s['ebMastery' + masteryClass(item.mastery_level)] ?? ''}`}
          data-test={`eb-mastery-${masteryClass(item.mastery_level)}`}
        >
          {item.mastery_level === 2 ? t('kid.errorbook.masteryMastered')
            : item.mastery_level === 1 ? t('kid.errorbook.masteryReviewing')
            : t('kid.errorbook.masteryNew')}
        </span>
      </header>

      {/* The OD eb-mastery-bar: the three-state machine made visible as a
          fill line under the header — how far this question has travelled
          from "just got it wrong" to "mastered". */}
      <div
        className={s.ebMasteryBar}
        data-test={`eb-mastery-bar-${item.id}`}
        role="img"
        aria-label={t('kid.errorbook.masteryBarLabel', { percent: masteryPercent(item.mastery_level) })}
      >
        <i style={{ width: `${masteryPercent(item.mastery_level)}%` }} />
      </div>

      <MathText as="p" className={s.ebStem} text={item.question_text} />

      {item.wrong_answer_text && (
        <div className={s.ebWrote}>
          <span>{t('kid.errorbook.youWrote')}</span>
          <MathText as="p" text={item.wrong_answer_text} />
        </div>
      )}

      {item.answer_text && (
        <div className={s.ebAnswer}>
          <span>{t('kid.errorbook.referenceAnswer')}</span>
          <MathText as="p" text={item.answer_text} />
        </div>
      )}

      {item.mistake_analysis && (
        <div className={s.ebAiCard} data-test="eb-ai-card">
          <span className={s.ebAiTitle} data-test="eb-ai-card-title">{t('kid.errorbook.aiCard')}</span>
          <MathText as="p" className={s.ebAiNote} data-test="eb-ai-note" text={item.mistake_analysis} />
        </div>
      )}

      {configSummary && (
        <p className={s.ebProfileLine} data-test="eb-profile-line">
          {t('kid.errorbook.profileTuned', { profile: configSummary })}
        </p>
      )}

      {tags.length > 0 && (
        <p className={s.ebTags}>{t('kid.errorbook.tags', { tags: tags.join(' · ') })}</p>
      )}

      <div className={s.ebCardActions} data-test="eb-card-actions">
        <button
          type="button"
          className={s.ebDoneBtn}
          data-test="eb-done-btn"
          onClick={markDone}
          disabled={busy || item.mastery_level === 2}
        >
          <Icon name="check" /> {t('kid.errorbook.doneBtn')}
        </button>
        <button
          type="button"
          className={s.ebAgainBtn}
          data-test="eb-again-btn"
          onClick={again}
          disabled={busy}
        >
          {t('kid.errorbook.againBtn')}
        </button>
      </div>

      {item.mastery_level === 1 && item.next_review_at && (
        <p className={s.ebNext}>{reviewWhen(t, item.next_review_at)}</p>
      )}
      {note && <p className={s.ebNote} role="status">{note}</p>}
    </article>
  );
};

interface ErrorBookScreenProps {
  topBar: React.ReactNode;
}

/** The whole book: filters (all / per-subject), the card list, the empty state. */
export function ErrorBookScreen({ topBar }: ErrorBookScreenProps) {
  const { t } = useTranslation();
  const [items, setItems] = useState<ErrorItem[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [subject, setSubject] = useState('');
  // The profile one-liner from /kid/ai-feedback (config_summary) — fetched
  // once per mount so every card can show 「已按你的画像调成…」.
  const [configSummary, setConfigSummary] = useState('');

  const load = useCallback(async (subj: string) => {
    setFailed(false);
    try {
      const list = await api.errorItems.list(subj ? { subject: subj } : undefined);
      setItems(list);
    } catch {
      setFailed(true);
      setItems([]);
    }
  }, []);

  useEffect(() => { load(subject); }, [load, subject]);

  useEffect(() => {
    api.learner.aiFeedback().then(fb => {
      if (fb.config_summary) setConfigSummary(fb.config_summary);
    }).catch(() => {});
  }, []);

  const subjects = useMemo(() => {
    const counts = new Map<string, number>();
    for (const it of items ?? []) {
      counts.set(it.subject, (counts.get(it.subject) ?? 0) + 1);
    }
    return [...counts.entries()].sort((a, b) => b[1] - a[1]);
  }, [items]);

  const onDone = useCallback((updated: ErrorItem) => {
    setItems(prev => (prev ?? []).map(it => (it.id === updated.id ? updated : it)));
  }, []);

  return (
    <div className={s.ebScreen} data-test="kid-errorbook-page">
      {topBar}
      <h1 className={s.screenTitle}>{t('kid.errorbook.title')}</h1>
      <p className={s.ebScene} data-test="eb-scene">{t('kid.errorbook.scene')}</p>

      {(items === null) && <p className={s.ebLoading}>{t('kid.errorbook.loading')}</p>}

      {items !== null && (
        <>
          <div className={s.ebFilters} data-test="eb-filters" role="group">
            <button
              type="button"
              className={`${s.ebFilter} ${!subject ? s.ebFilterOn : ''}`}
              data-test="eb-filter-all"
              onClick={() => setSubject('')}
            >
              {t('kid.errorbook.filterAll')}
            </button>
            {subjects.map(([subj, count]) => (
              <button
                key={subj}
                type="button"
                className={`${s.ebFilter} ${subject === subj ? s.ebFilterOn : ''}`}
                data-test={`eb-filter-${subj}`}
                onClick={() => setSubject(subj)}
              >
                {subjectLabel(t, subj)} {count}
              </button>
            ))}
          </div>

          {failed && <p className={s.ebError} role="alert">{t('kid.errorbook.loadFailed')}</p>}

          {(items.length === 0 && !failed) && (
            <div className={s.ebEmpty} data-test="eb-empty">
              <Icon name="book" />
              <p className={s.ebEmptyTitle}>{t('kid.errorbook.emptyTitle')}</p>
              <p>{t('kid.errorbook.emptyText')}</p>
              <p className={s.ebEmptyCta} data-test="eb-empty-cta">{t('kid.errorbook.emptyCta')}</p>
            </div>
          )}

          {(items.length > 0) && (
            <div className={s.ebList} data-test="eb-list">
              {items.map(it => <ErrorCard key={it.id} item={it} onDone={onDone} configSummary={configSummary || undefined} />)}
            </div>
          )}

          <p className={s.ebFoot} data-test="eb-foot">{t('kid.errorbook.foot')}</p>
        </>
      )}
    </div>
  );
}
