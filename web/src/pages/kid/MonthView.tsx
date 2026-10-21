// 「月视图」就地展开区（opencore.png 参考图交互）：点今日进度卡的
// 「月视图」，整月日历在周历条的原位置展开（替换 .strip）；点日期后
// 月历收起、周条回来，主页任务列表换成所选日——布局不变，只换内容。
// 月历与周历条同款视觉：周一~周日表头 + 日期圆圈（有任务的圆圈里穿
// done/total），月份就地下拉切换。kid-history-page / hist-days /
// hist-month-list 的 data-test 契约全部保留。
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ScheduledChore, User } from '../../types';
import { Icon } from '../../design';
import { monthKey, monthLead, monthLength, monthStart, shiftMonth } from './dates';
import s from './kid.module.css';

interface MonthViewProps {
  user: User;
  today: string;
  /** The day the month grid jumps the home list to (and folds itself). */
  onPickDay: (date: string) => void;
}

export function MonthView({ user, today, onPickDay }: MonthViewProps) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const [month, setMonth] = useState(() => monthKey(today));
  const [monthOpen, setMonthOpen] = useState(false);
  const [chores, setChores] = useState<ScheduledChore[] | null>(null);

  // The month's own summary (问题①): one read-only fetch, never a
  // generation — "" (not yet written) shows the "not yet" line. Fetched
  // once per month, rendered below the grid so the calendar keeps the
  // strip's slot.
  const [summary, setSummary] = useState<string | null>(null);

  const load = useCallback(async () => {
    setChores(null);
    try {
      setChores(await api.users.getChoresSelf('month', monthStart(`${month}-01`)));    } catch {
      setChores([]);
    }
  }, [month]);

  useEffect(() => { load(); }, [load]);

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
    <div className={s.histScreen} data-test="kid-history-page" id="home-month-view">
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

      {chores === null && <p className={s.ebLoading}>{t('kid.history.loading')}</p>}

      {/* 月历：与周历条同款七列网格，日期放圆圈里，有任务的日子圈里
          穿 done/total；点选后收起月历、任务列表换成该日。 */}
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

      {/* 本月总结：日历下方一张小卡——月份的总结随月历一起看，
          「为什么有这块」由标题与脚注交代。 */}
      <section className={s.histSummary} data-test="hist-summary" aria-label={t('kid.history.summaryTitle')}>
        <h2 className={s.histSummaryTitle}><Icon name="star" fill /> {t('kid.history.summaryTitle')}</h2>
        {summary === null && <p className={s.ebLoading}>{t('kid.history.loading')}</p>}
        {summary !== null && summary !== '' && <p className={s.histSummaryText} data-test="hist-summary-text">{summary}</p>}
        {summary === '' && <p className={s.histSummaryEmpty} data-test="hist-summary-empty">{t('kid.history.summaryEmpty')}</p>}
      </section>
    </div>
  );
}
