// 问题②: the kid's whole past. Week only ever showed seven days, so a
// finished month vanished the moment it ended. This screen is the full
// ledger instead: pick a month, see every day of it — what was planned,
// what got done, and the state of each chore — read-only, because the
// past can't be tapped into changing. One request per month through the
// view=month endpoint (the same rows the parent's history view reads).
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ScheduledChore, User } from '../../types';
import { Icon } from '../../design';
import { ChoreGroups, Notice } from './parts';
import type { ChoreView } from './choreView';
import s from './kid.module.css';

/** "2026-10" style month key of a YYYY-MM-DD date. */
const monthKey = (date: string) => date.slice(0, 7);

/** First day (YYYY-MM-DD) of the month holding `date`. */
const monthStart = (date: string) => `${monthKey(date)}-01`;

/** The month key before/after "YYYY-MM", stepping over year bounds. */
export function shiftMonth(key: string, by: number): string {
  const [y, m] = key.split('-').map(Number);
  const d = new Date(y, m - 1 + by, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}

interface HistoryScreenProps {
  user: User;
  topBar: React.ReactNode;
  today: string;
  viewOf: (c: ScheduledChore) => ChoreView;
  tts: boolean;
  onPhoto: (c: ScheduledChore) => void;
  onViewSubmission: (c: ScheduledChore) => void;
  onSpeak: (text: string, audioUrl?: string) => void;
}

export function HistoryScreen({ user, topBar, today, viewOf, tts, onPhoto, onViewSubmission, onSpeak }: HistoryScreenProps) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  // The month being browsed. This month is the default (its days clamp at
  // today server-side); 「上月」 walks back from there without bound.
  const [month, setMonth] = useState(() => monthKey(today));
  const [chores, setChores] = useState<ScheduledChore[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [selected, setSelected] = useState<string>(today);
  const [summary, setSummary] = useState<string | null>(null);

  const load = useCallback(async () => {
    setFailed(false);
    setChores(null);
    try {
      setChores(await api.users.getChoresSelf('month', monthStart(`${month}-01`)));
    } catch {
      setFailed(true);
      setChores([]);
    }
  }, [month]);

  useEffect(() => { load(); }, [load]);

  // The month's own summary card (问题①): one read-only fetch, never a
  // generation — "" (not yet written) shows the "not yet" line.
  useEffect(() => {
    let live = true;
    setSummary(null);
    api.users.getMySummary(user.id, 'month', monthStart(`${month}-15`))
      .then(res => { if (live) setSummary(res.summary); })
      .catch(() => { if (live) setSummary(''); });
    return () => { live = false; };
  }, [user.id, month]);

  const days = useMemo(() => {
    // Every date that came back, newest last — a day strip for browsing.
    const uniq = [...new Set((chores ?? []).map(c => c.date))].sort();
    return uniq;
  }, [chores]);

  const monthLabel = new Date(`${month}-01T00:00:00`)
    .toLocaleDateString(lang, { year: 'numeric', month: 'long' });

  const dayChores = useMemo(
    () => (chores ?? []).filter(c => c.date === selected),
    [chores, selected],
  );
  const dayLabel = new Date(`${selected}T00:00:00`)
    .toLocaleDateString(lang, { weekday: 'long', month: 'long', day: 'numeric' });

  return (
    <div className={s.histScreen} data-test="kid-history-page">
      {topBar}
      <h1 className={s.screenTitle}>{t('kid.history.title')}</h1>

      <div className={s.histMonthBar} role="group" aria-label={t('kid.history.monthLabel', { month: monthLabel })}>
        <button
          type="button"
          className={s.histMonthBtn}
          onClick={() => setMonth(m => shiftMonth(m, -1))}
          aria-label={t('kid.history.prevMonth')}
        >
          <Icon name="back" />
        </button>
        <span className={s.histMonth} aria-live="polite">{monthLabel}</span>
        <button
          type="button"
          className={s.histMonthBtn}
          onClick={() => setMonth(m => shiftMonth(m, 1))}
          disabled={month >= monthKey(today)}
          aria-label={t('kid.history.nextMonth')}
        >
          <Icon name="chev" />
        </button>
      </div>

      {/* The month's summary (问题①): the scheduled writer's own words about
          this whole month, read back. "" reads as "not yet written". */}
      <section className={s.histSummary} data-test="hist-summary" aria-label={t('kid.history.summaryTitle')}>
        <h2 className={s.histSummaryTitle}><Icon name="star" fill /> {t('kid.history.summaryTitle')}</h2>
        {summary === null && <p className={s.ebLoading}>{t('kid.history.loading')}</p>}
        {summary !== null && summary !== '' && <p className={s.histSummaryText} data-test="hist-summary-text">{summary}</p>}
        {summary === '' && <p className={s.histSummaryEmpty} data-test="hist-summary-empty">{t('kid.history.summaryEmpty')}</p>}
      </section>

      {failed && <p className={s.ebError} role="alert">{t('kid.history.loadFailed')}</p>}

      {chores === null && <p className={s.ebLoading}>{t('kid.history.loading')}</p>}

      {chores !== null && days.length === 0 && !failed && (
        <Notice icon="cal" title={t('kid.history.emptyTitle')}>{t('kid.history.emptyText')}</Notice>
      )}

      {chores !== null && days.length > 0 && (
        <>
          <div className={s.histDays} data-test="hist-days" role="group">
            {days.map(date => {
              const list = (chores ?? []).filter(c => c.date === date);
              const done = list.filter(c => c.completed).length;
              return (
                <button
                  key={date}
                  type="button"
                  className={`${s.histDay} ${date === selected ? s.histDayOn : ''}`}
                  aria-pressed={date === selected}
                  onClick={() => setSelected(date)}
                >
                  <span className={s.histDayNum}>{Number(date.slice(8))}</span>
                  <span className={s.histDayCount}>{done}/{list.length}</span>
                </button>
              );
            })}
          </div>

          <h2 className={s.dayTitle}>{dayLabel}</h2>
          {dayChores.length === 0 ? (
            <Notice icon="cal" title={t('kid.history.freeTitle')}>{t('kid.history.freeText')}</Notice>
          ) : (
            <ChoreGroups
              chores={dayChores}
              viewOf={viewOf}
              tts={tts}
              onPhoto={onPhoto}
              onViewSubmission={onViewSubmission}
              onSpeak={onSpeak}
              headingLevel="h3"
            />
          )}
        </>
      )}

      <p className={s.ebFoot}>{t('kid.history.foot')}</p>
    </div>
  );
}
