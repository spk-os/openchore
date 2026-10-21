// 「月视图」展开区：原「历史」页的全部内容（月总结卡、月历网格）收进
// 主页的一块可展开区域。点「月视图」按钮展开/收起；点网格里的日子回填
// 到主页任务列表。月历与周历条同款视觉：周一~周日表头 + 日期圆圈
// （有任务的圆圈里穿 done/total），月份切换就地下拉，不另起步进条。
// hist-summary / hist-days / kid-history-page 的 data-test 全部保留
// （kid-history-page 根节点落在展开区上）。
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ScheduledChore, User } from '../../types';
import { Icon } from '../../design';
import { monthKey, monthLead, monthLength, monthStart, shiftMonth } from './dates';
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
  const [monthOpen, setMonthOpen] = useState(false);
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

  // 每天的行数，供月历圆圈里穿 done/total。
  const byDate = useMemo(() => {
    const m = new Map<string, ScheduledChore[]>();
    for (const c of chores ?? []) {
      const list = m.get(c.date);
      if (list) list.push(c);
      else m.set(c.date, [c]);
    }
    return m;
  }, [chores]);
  const hasRows = byDate.size > 0;

  const monthLabel = new Date(`${month}-01T00:00:00`)
    .toLocaleDateString(lang, { year: 'numeric', month: 'long' });
  const weekdays = useMemo(() => {
    // 周历条同款：周一~周日的短名，任意一周的七天生成一次即可。
    const monday = new Date(`${today}T00:00:00`);
    monday.setDate(monday.getDate() - (monday.getDay() + 6) % 7);
    return Array.from({ length: 7 }, (_, i) => {
      const d = new Date(monday);
      d.setDate(monday.getDate() + i);
      return d.toLocaleDateString(lang, { weekday: 'short' });
    });
  }, [lang, today]);

  // 月历网格：前置空白 + 1..月末，与周条相同的七列节奏。
  const cells = useMemo(() => {
    const lead = monthLead(month);
    const n = monthLength(month);
    return [
      ...Array.from({ length: lead }, (_, i) => ({ blank: i as number })),
      ...Array.from({ length: n }, (_, i) => ({ day: i + 1 })),
    ];
  }, [month]);

  // 就地下拉的月份清单：当前月往前 11 个月 + 当前月（只看得到过去，
  // 未来月还没有数据），点击即切月并收起弹层。
  const monthOptions = useMemo(() => {
    const cur = monthKey(today);
    return Array.from({ length: 12 }, (_, i) => shiftMonth(cur, i - 11));
  }, [today]);
  const monthOptionLabel = (key: string) =>
    new Date(`${key}-01T00:00:00`).toLocaleDateString(lang, { year: 'numeric', month: 'long' });
  const pickMonth = (key: string) => {
    setMonth(key);
    setMonthOpen(false);
  };

  return (
    <section className={s.histScreen} data-test="kid-history-page" id="home-month-view">
      {/* 月份切换：todo清单式的就地下拉——点「月份 ▾」原地展开月份清单，
          选一个即切月；‹ › 步进钮保留。 */}
      <div className={s.histMonthPick}>
        <button
          type="button"
          className={s.histMonthBtn}
          onClick={() => setMonth(m => shiftMonth(m, -1))}
          aria-label={t('kid.history.prevMonth')}
        >
          <Icon name="back" />
        </button>
        <div className={s.histMonthWrap}>
          <button
            type="button"
            className={s.histMonthLabel}
            aria-haspopup="listbox"
            aria-expanded={monthOpen}
            onClick={() => setMonthOpen(o => !o)}
          >
            <span aria-live="polite">{monthLabel}</span>
            <Icon name="chev" className={monthOpen ? s.histMonthChevUp : s.histMonthChev} />
          </button>
          {monthOpen && (
            <ul className={s.histMonthList} role="listbox" data-test="hist-month-list">
              {monthOptions.map(key => (
                <li key={key}>
                  <button
                    type="button"
                    role="option"
                    aria-selected={key === month}
                    className={key === month ? s.histMonthOptOn : s.histMonthOpt}
                    onClick={() => pickMonth(key)}
                  >
                    {monthOptionLabel(key)}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
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
      {chores !== null && !hasRows && !failed && (
        <Notice icon="cal" title={t('kid.history.emptyTitle')}>{t('kid.history.emptyText')}</Notice>
      )}
      {/* 月历：与周历条同款七列网格，日期放圆圈里，有任务的日子圈里
          穿 done/total；点选回填主页任务列表。 */}
      {chores !== null && (
        <div className={s.histDays} data-test="hist-days" role="grid" aria-label={monthLabel}>
          {weekdays.map(w => (
            <span key={w} className={s.histDow} aria-hidden>{w}</span>
          ))}
          {cells.map(c =>
            'blank' in c ? (
              <span key={`b${c.blank}`} className={s.histDayBlank} aria-hidden />
            ) : (
              (() => {
                const date = `${month}-${String(c.day).padStart(2, '0')}`;
                const list = byDate.get(date) ?? [];
                const done = list.filter(x => x.completed).length;
                const isToday = date === today;
                return (
                  <button
                    key={date}
                    type="button"
                    className={s.histDay}
                    aria-pressed={false}
                    disabled={list.length === 0}
                    aria-label={list.length
                      ? t('kid.history.dayLabel', { day: c.day, done, total: list.length })
                      : t('kid.history.dayEmpty', { day: c.day })}
                    onClick={() => onPickDay(date)}
                  >
                    <span
                      className={s.histDayRing}
                      data-today={isToday || undefined}
                      data-full={list.length > 0 && done >= list.length || undefined}
                    >
                      <span className={s.histDayNum}>{c.day}</span>
                    </span>
                    <span className={s.histDayCount}>{list.length ? `${done}/${list.length}` : ''}</span>
                  </button>
                );
              })()
            ),
          )}
        </div>
      )}
      <p className={s.ebFoot}>{t('kid.history.foot')}</p>
    </section>
  );
}
