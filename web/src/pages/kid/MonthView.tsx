// 「月视图」展开区：原「历史」页的全部内容（月份步进条、月总结卡、
// 日历网格）收进主页的一块可展开区域。点「月视图」按钮展开/收起；点
// 网格里的日子回填到主页任务列表。月份步进、月总结、hist-days 网格的
// data-test 全部保留（kid-history-page 根节点落在展开区上）。
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ScheduledChore, User } from '../../types';
import { Icon } from '../../design';
import { monthKey, monthStart, shiftMonth } from './dates';
import { Notice } from './parts';
import s from './kid.module.css';

interface MonthViewProps {
  user: User;
  today: string;
  /** The day the month grid jumps the home list to. */
  onPickDay: (date: string) => void;
}

export function MonthView({ user, today, onPickDay }: MonthViewProps) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [month, setMonth] = useState(() => monthKey(today));
  const [chores, setChores] = useState<ScheduledChore[] | null>(null);
  const [failed, setFailed] = useState(false);
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

  const days = useMemo(
    () => [...new Set((chores ?? []).map(c => c.date))].sort(),
    [chores],
  );

  const monthLabel = new Date(`${month}-01T00:00:00`)
    .toLocaleDateString(lang, { year: 'numeric', month: 'long' });

  return (
    <section className={s.histScreen} data-test="kid-history-page" id="home-month-view">
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
        <div className={s.histDays} data-test="hist-days" role="group">
          {days.map(date => {
            const list = (chores ?? []).filter(c => c.date === date);
            const done = list.filter(c => c.completed).length;
            return (
              <button
                key={date}
                type="button"
                className={s.histDay}
                aria-pressed={false}
                onClick={() => onPickDay(date)}
              >
                <span className={s.histDayNum}>{Number(date.slice(8))}</span>
                <span className={s.histDayCount}>{done}/{list.length}</span>
              </button>
            );
          })}
        </div>
      )}
      <p className={s.ebFoot}>{t('kid.history.foot')}</p>
    </section>
  );
}
