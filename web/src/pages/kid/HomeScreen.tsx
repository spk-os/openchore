// 主页 = 今天 + 本周 + 历史 三合一（OD spk-openchore kid-home.html 的契约）。
// Today 的全部（进度弧 / 入口卡 / 任务组 / 教练备注）保持原位；「本周」的
// 周历条与 streak 收进今日进度卡的底部一行；「历史」的月历在周历条的
// 原位置就地展开（周历条下方居中小箭头点击或滑动 → 整月日历替换周条，
// 点日期 → 周条回来、任务列表换成所选日——布局不变，只换内容，
// opencore.png 参考图交互）。周历条的日期数字在圆圈内（2026-10-22
// 用户定稿：日期在圈里，不要在圈下面）。
// 选中「今天」的任务可交互；过去日只读（choreView 的 canToggle 只对
// isToday 放开，天然安全）。
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import clsx from 'clsx';
import { useTranslation } from 'react-i18next';
import { DayProgress, Greeting, Icon, catFromCategory, salutationFor, type DayProgressItem } from '../../design';
import { api } from '../../api';
import type { ScheduledChore, User } from '../../types';
import { gates, isDone, type ChoreView } from './choreView';
import { weekDates } from './dates';
import { GoalNudge } from './GoalCard';
import { FamilyGoalCard } from './FamilyGoalCard';
import { PetCard } from './PetCard';
import { ReviewEntryCard } from './ReviewEntryCard';
import { CourseEntryCard } from './CourseEntryCard';
import { ErrorBookEntryCard } from './ErrorBookEntryCard';
import { CAT_ORDER, ChoreGroups, Notice, PausedNotice } from './parts';
import { MonthView } from './MonthView';
import type { KidData } from './useKidData';
import s from './kid.module.css';

/** The day runs 7 am – 9 pm on the sun arc. */
const DAY_FROM = 7;
const DAY_TO = 21;
export const dayFraction = (d: Date) =>
  Math.min(1, Math.max(0, (d.getHours() + d.getMinutes() / 60 - DAY_FROM) / (DAY_TO - DAY_FROM)));

/** The four marks a week-strip day can wear. */
type DayKind = 'full' | 'partial' | 'future' | 'none';

// 参考图 opencore.png 的形态：日期数字进圈里（不是圈下一个裸数字）。
// 四态语义保留——full 实心圈白字、partial 轨道圈+高亮弧+数字、
// future 虚线圈+数字、none 无任务小圆点（数字仍在旁侧小字）。
function DayRing({ num, kind, pct, isToday }: { num: number; kind: DayKind; pct: number; isToday: boolean }) {
  return (
    <span
      className={s.dayRing}
      data-kind={kind}
      data-today={isToday || undefined}
      data-full={kind === 'full' || undefined}
      aria-hidden
    >
      {kind === 'none' ? (
        <>
          <span className={s.dayRingNum}>{num}</span>
          <span className={s.dayRingDot} />
        </>
      ) : kind === 'partial' ? (
        <>
          {pct > 0 && <span className={s.dayRingArc} style={{ background: `conic-gradient(var(--highlight) ${pct}%, transparent 0)` }} />}
          <span className={s.dayRingNum}>{num}</span>
        </>
      ) : (
        <span className={s.dayRingNum}>{num}</span>
      )}
    </span>
  );
}

interface HomeProps {  user: User;
  data: KidData;
  now: Date;
  topBar: React.ReactNode;
  viewOf: (c: ScheduledChore) => ChoreView;
  tts: boolean;
  onToggle: (c: ScheduledChore, view: ChoreView) => void;
  onPhoto: (c: ScheduledChore) => void;
  /** Open the kid's own submission record (问题①). */
  onViewSubmission: (c: ScheduledChore) => void;
  onSpeak: (text: string, audioUrl?: string) => void;
}

export function HomeScreen({ user, data, now, topBar, viewOf, tts, onToggle, onPhoto, onViewSubmission, onSpeak }: HomeProps) {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const firstName = user.name.split(' ')[0];

  // --- The day the task list shows. The week strip drives it; today is
  // the default and month-grid picks land back on it too. ---
  const [selected, setSelected] = useState(data.today);
  useEffect(() => { setSelected(data.today); }, [data.today]);

  const [monthOpen, setMonthOpen] = useState(false);
  // 2026-10-22 用户定稿交互：月视图入口不再是「月视图」文字按钮，而是
  // 周历条下方居中的小箭头——点击或上下滑动切换（todo 清单下拉语义）。
  const touchStartY = useRef<number | null>(null);
  const openMonth = useCallback(() => setMonthOpen(true), []);
  const toggleMonth = useCallback(() => setMonthOpen(v => !v), []);
  const week = weekDates(data.today);
  const allWeek = data.weekChores ?? [];
  // 选中日可能落在当前周之外（月视图点选），任务数据源跟随选中日：
  // 周内的日子走 weekChores，其余日子走今天的 daily 缓存（同一天的数据）。
  const inWeek = week.includes(selected);
  const dayChores = useMemo(() => {
    if (inWeek) return allWeek.filter(c => c.date === selected);
    return (data.chores ?? []).filter(c => c.date === selected);
  }, [inWeek, allWeek, data.chores, selected]);

  /** A month-grid day lands on the home list (and folds the calendar
   *  back into the week strip — the layout stays, only the content
   *  swaps to the picked day). */
  const pickDay = useCallback((date: string) => {
    setSelected(date);
    setMonthOpen(false);
  }, []);

  // One mark per chore, in list order, so the shape row reads like the list.
  const todayChores = (data.chores ?? []).filter(c => c.date === data.today);
  const items: DayProgressItem[] = CAT_ORDER.flatMap(cat =>
    todayChores.filter(c => catFromCategory(c.category) === cat).map(c => {
      const done = isDone(c);
      const at = done && c.completed_at ? new Date(c.completed_at) : null;
      return { cat, done, at: at && !Number.isNaN(at.getTime()) ? dayFraction(at) : undefined };
    }));
  const doneToday = items.filter(i => i.done).length;
  const allDone = items.length > 0 && doneToday === items.length;

  // Points today, as the old stats showed them: Every day points wait until
  // every Must do is done.
  const { requiredDone } = gates(todayChores);
  let earned = 0;
  let pending = 0;
  for (const c of todayChores) {
    if (!c.completed) continue;
    if (c.category === 'core' && !requiredDone) pending += c.points_value || 0;
    else earned += c.points_value || 0;
  }

  const commitments = data.points?.active_commitments ?? [];
  const loading = data.chores == null;
  const streak = data.streak;

  // The week's own summary (问题①), read back from the scheduled writer's
  // cache — read-only, never an on-demand generation.
  const [weekSummary, setWeekSummary] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setWeekSummary(null);
    api.users.getMySummary(user.id, 'week', data.today)
      .then(res => { if (live) setWeekSummary(res.summary); })
      .catch(() => { if (live) setWeekSummary(''); });
    return () => { live = false; };
  }, [user.id, data.today]);

  return (
    <div className={s.today} data-test="kid-home-page">
      <section className={s.todayHero} aria-label={t('kid.today.overview')}>
        {topBar}
        <Greeting salutation={t(`design.greeting.${salutationFor(now)}`)} name={firstName} />

        {/* 今日进度卡：大数字 + 进度弧 + 周历条 + 本周小结行（三合一的
            核心——原 Today 进度区、Week 的周历条与 streak 收进一张卡）。 */}
        {!user.paused && items.length > 0 && (
          <div className={s.homeProgressCard}>
            <div role="group" aria-label={t('design.progress.label', { done: doneToday, total: items.length })} className={s.progress}>
              <DayProgress items={items} now={dayFraction(now)} />
            </div>
            {(allDone || earned > 0 || pending > 0) && (
              <p className={s.todayLine}>
                {allDone ? <strong>{t('kid.today.allDone')}</strong> : null}
                {earned > 0 && <span>{t('kid.today.earned', { count: earned })}</span>}
                {pending > 0 && <span>{t('kid.today.pending', { count: pending })}</span>}
              </p>
            )}
            {/* 参考图交互：周历条下方居中小箭头点击/上下滑动 → 整月日历
                在周历条的原位置展开（替换 .strip），点日期后月历收起、
                周条回来，任务列表换成所选日——布局不变，只换内容。 */}
            {monthOpen ? (
              <MonthView user={user} today={data.today} onPickDay={pickDay} />
            ) : (
              <div
                className={s.strip}
                role="group"
                aria-label={t('kid.week.days')}
                onTouchStart={e => { touchStartY.current = e.touches[0].clientY; }}
                onTouchEnd={e => {
                  if (touchStartY.current == null) return;
                  const dy = e.changedTouches[0].clientY - touchStartY.current;
                  touchStartY.current = null;
                  if (Math.abs(dy) < 24) return;
                  if (dy < 0) openMonth();
                }}
              >
                {week.map(date => {
                  const d = new Date(`${date}T00:00:00`);
                  const list = allWeek.filter(c => c.date === date);
                  const done = list.filter(isDone).length;
                  const total = list.length;
                  const isToday = date === data.today;
                  const kind: DayKind = total === 0 ? 'none'
                    : done === total ? 'full'
                    : date > data.today ? 'future' : 'partial';
                  const long = d.toLocaleDateString(lang, { weekday: 'long', day: 'numeric' });
                  return (
                    <button
                      key={date}
                      type="button"
                      className={clsx(s.day, isToday && s.dayToday, date === selected && s.daySelected)}
                      aria-pressed={date === selected}
                      aria-current={isToday ? 'date' : undefined}
                      aria-label={total ? t('kid.week.dayLabel', { day: long, done, total }) : t('kid.week.dayEmpty', { day: long })}
                      onClick={() => setSelected(date)}
                    >
                      <span className={s.dayName} aria-hidden>{d.toLocaleDateString(lang, { weekday: 'short' })}</span>
                      <DayRing num={d.getDate()} kind={kind} pct={total ? (done / total) * 100 : 0} isToday={isToday} />
                    </button>
                  );
                })}
              </div>
            )}
            {/* 本周小结行：🔥 streak · 周总结卡。月视图开关收进周历条
                下方的居中小箭头（点击或滑动展开，无文字链接）。 */}
            <div className={s.homeWeekRow}>
              <section className={s.streakInline} aria-label={t('kid.week.streak')}>
                <span className={s.streakIcon}><Icon name="flame" fill size={26} /></span>
                <span className={s.streakMain}>
                  <span className={s.streakNum}>{t('kid.week.streakDays', { count: streak?.current_streak ?? 0 })}</span>
                  <span className={s.streakSub}>{t('kid.week.best', { count: streak?.longest_streak ?? 0 })}</span>
                </span>
              </section>
            </div>
            <button
              type="button"
              className={s.monthArrow}
              data-test="month-arrow"
              aria-expanded={monthOpen}
              aria-controls="home-month-view"
              aria-label={monthOpen ? t('kid.history.collapse') : t('kid.history.expand')}
              onClick={toggleMonth}
            >
              <Icon name="chev" className={monthOpen ? s.monthArrowChevUp : s.monthArrowChev} />
            </button>
            {streak?.next_reward && (
              <p className={s.streakNext}>
                {t('kid.week.nextMilestone', {
                  count: streak.next_reward.days_remaining,
                  label: streak.next_reward.label,
                  points: streak.next_reward.bonus_points,
                })}
              </p>
            )}
            <section className={s.histSummary} data-test="week-summary" aria-label={t('kid.week.summaryTitle')}>
              <h2 className={s.histSummaryTitle}><Icon name="star" fill /> {t('kid.week.summaryTitle')}</h2>
              {weekSummary === null && <p className={s.ebLoading}>{t('kid.history.loading')}</p>}
              {weekSummary !== null && weekSummary !== '' && <p className={s.histSummaryText} data-test="week-summary-text">{weekSummary}</p>}
              {weekSummary === '' && <p className={s.histSummaryEmpty} data-test="week-summary-empty">{t('kid.week.summaryEmpty')}</p>}
            </section>
          </div>
        )}

        {/* The family's shared goal (P4-14) up front. */}
        {!user.paused && data.familyGoal && (
          <FamilyGoalCard
            c={data.familyGoal}
            me={user}
            people={data.people}
            grownUpsDone={data.grownUpsDone}
          />
        )}
        {/* The review entry (L4), the error-book entry (L1) and the course
            entry (L2): the doors off Today, each hiding itself when empty. */}
        {!user.paused && <ReviewEntryCard />}
        {!user.paused && <ErrorBookEntryCard />}
        {!user.paused && <CourseEntryCard />}
        {/* The kid's buddy (P4-13), when the household has it on. */}
        {!user.paused && data.petState && (
          <PetCard
            initial={data.petState}
            onToast={data.showToast}
            onFed={() => { data.loadExtras(); }}
          />
        )}
        {/* The coach's good-night note from yesterday. */}
        {!user.paused && data.coachNote && (
          <section className={s.coachCard} aria-label={t('kid.today.coachTitle')}>
            <div className={s.coachHead}>
              <span className={s.coachIcon}><Icon name="star" fill size={20} /></span>
              <span className={s.coachKicker}>{t('kid.today.coachTitle')}</span>
              <button
                type="button"
                className={s.coachPlay}
                aria-label={t('kid.today.coachPlay')}
                onClick={() => onSpeak(data.coachNote!.child_text, data.coachNote!.child_audio_url)}
              >
                <Icon name="sound" size={20} />
              </button>
            </div>
            <p className={s.coachText}>{data.coachNote.child_text}</p>
          </section>
        )}
      </section>

      {/* The day's list under whatever day the strip picked. */}
      <section className={s.todayLists} aria-label={t('kid.today.listLabel')}>
        <h2 className={s.dayTitle}>
          {new Date(`${selected}T00:00:00`).toLocaleDateString(lang, { weekday: 'long', month: 'long', day: 'numeric' })}
        </h2>
        {user.paused ? (
          <PausedNotice />
        ) : loading && inWeek ? null : dayChores.length === 0 ? (
          selected === data.today
            ? <Notice icon="sprout" title={t('kid.today.emptyTitle')}>{t('kid.today.emptyText')}</Notice>
            : <Notice icon="cal" title={t('kid.week.emptyTitle')}>{t('kid.week.emptyText')}</Notice>
        ) : (
          <ChoreGroups
            chores={dayChores}
            viewOf={viewOf}
            togglingIds={data.togglingIds}
            tts={tts}
            onToggle={onToggle}
            onPhoto={onPhoto}
            onViewSubmission={onViewSubmission}
            onSpeak={onSpeak}
          />
        )}
      </section>

      {/* Goals: under the list on a phone, under the hero on a tablet. */}
      {commitments.length > 0 && (
        <section className={s.nudges} aria-label={t('kid.goal.title')}>
          {commitments.map(c => <GoalNudge key={c.id} c={c} me={user} people={data.people} />)}
        </section>
      )}
    </div>
  );
}
