import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { RefreshCw, Loader2 } from 'lucide-react';
import { api } from '../../api';
import type { User, ScheduledChore, UserStreakData, PointBalance, PendingCompletion, DailyCoachNote, Pet, PetResponse, PaperAnalysis, CompletionAttachment } from '../../types';
import { Avatar, CategoryMark, Icon, catFromCategory } from '../../design';
import { localDateStr } from '../../utils';
import { ApprovalList } from './ApprovalList';
import Modal from '../Modal/Modal';
import { PaperAnalysisModal } from './PaperAnalysisCard';
import { IconWell, personColor } from './pickers';
import { glanceFor, type TodayGlance } from './todayGlance';
import ui from './ui.module.css';
import styles from './KidsStatusTab.module.css';

interface KidStatus {
  user: User;
  chores: ScheduledChore[];
  balance: number;
  streak: number;
  pendingApprovals: number;
  coachNote: DailyCoachNote | null;
  /** The kid's pet when it has hatched (null hides the status line). */
  pet: Pet | null;
  /** The kid's freshest finished paper analysis (L1 quick-report). */
  latestAnalysis: PaperAnalysis | null;
  loadError: boolean;
}

interface Breakdown {
  requiredCompleted: number;
  requiredTotal: number;
  coreCompleted: number;
  coreTotal: number;
  bonusCompleted: number;
  bonusTotal: number;
  // overdue is restricted to required+core (bonus is never "overdue" for the
  // purpose of alerts — it's opt-in and doesn't block the bonus gate).
  overdue: number;
  pendingOnToday: number;
}

function breakdownFor(chores: ScheduledChore[]): Breakdown {
  let requiredCompleted = 0;
  let requiredTotal = 0;
  let coreCompleted = 0;
  let coreTotal = 0;
  let bonusCompleted = 0;
  let bonusTotal = 0;
  let overdue = 0;
  let pendingOnToday = 0;
  for (const c of chores) {
    if (c.category === 'required') {
      requiredTotal += 1;
      if (c.completed) requiredCompleted += 1;
    } else if (c.category === 'core') {
      coreTotal += 1;
      if (c.completed) coreCompleted += 1;
    } else {
      bonusTotal += 1;
      if (c.completed) bonusCompleted += 1;
    }
    if (!c.completed && c.expired && c.category !== 'bonus') overdue += 1;
    if (c.completion_status === 'pending') pendingOnToday += 1;
  }
  return {
    requiredCompleted,
    requiredTotal,
    coreCompleted,
    coreTotal,
    bonusCompleted,
    bonusTotal,
    overdue,
    pendingOnToday,
  };
}

/** The freshest paper analysis as one glance card (OD analysis-quick-card):
 *  what was parsed, how many wrong, where they concentrated. Tapping opens
 *  the diagnosis report. */
const QuickAnalysisCard: React.FC<{
  kid: KidStatus;
  onOpen: (analysis: PaperAnalysis) => void;
}> = ({ kid, onOpen }) => {
  const { t } = useTranslation();
  const a = kid.latestAnalysis!;
  // questions may be null on pre-037 rows (a 0-question parse used to
  // serialize nil as null) — the crash that took the dashboard down.
  const wrong = a.summary?.questions?.filter(q => q.is_wrong) ?? [];
  // The strongest error type by count — the quick card's one-line verdict.
  const byType = new Map<string, number>();
  for (const q of wrong) {
    const key = q.error_type || 'unknown';
    byType.set(key, (byType.get(key) ?? 0) + 1);
  }
  const top = [...byType.entries()].sort((x, y) => y[1] - x[1])[0];
  const topLabel = top
    ? t(`admin.analysis.errorType${top[0].charAt(0).toUpperCase()}${top[0].slice(1)}`, { defaultValue: top[0] })
    : null;
  return (
    <section className={styles.quickCard} data-test="analysis-quick-card" aria-label={t('admin.kidsStatusTab.quickTitle')}>
      <div className={styles.quickBody}>
        <p className={styles.quickTitle}>📸 {t('admin.kidsStatusTab.quickTitle')} · {kid.user.name}</p>
        <p className={styles.quickText} data-test="analysis-quick-text">
          {a.subject} · {a.total_questions} → {a.wrong_count}
          {topLabel ? ` · ${topLabel}${top![1] > 1 ? ` ×${top![1]}` : ''}` : ''}
        </p>
        {a.summary && (a.summary.weak_tags?.length ?? 0) > 0 && (
          <p className={styles.quickSub} data-test="analysis-quick-sub">
            {a.summary.weak_tags.slice(0, 3).map(w => w.name).join(' · ')}
          </p>
        )}
        <div className={styles.quickActions}>
          <button type="button" className={styles.quickLink} data-test="analysis-quick-link" onClick={() => onOpen(a)}>
            {t('admin.kidsStatusTab.quickLink')}
          </button>
          {/* OD analysis-quick-kid: the parent lands in the kid's own
              error book in one tap — the questions to fix, on the screen
              the kid sees. */}
          <button
            type="button"
            className={styles.quickKidLink}
            data-test="analysis-quick-kid"
            onClick={() => { window.location.hash = '#tab=errorbook'; }}
          >
            {t('admin.kidsStatusTab.quickKidLink')}
          </button>
        </div>
      </div>
    </section>
  );
};

/** OD daily-report-preview: today's coach note as the parent's first
 *  screen — the date, the note itself and the day's chore shape in one
 *  line (must-do / bonus / points today). */
const DailyReportPreview: React.FC<{ kid: KidStatus }> = ({ kid }) => {  const { t } = useTranslation();
  const b = breakdownFor(kid.chores);
  const note = kid.coachNote;
  const text = note?.parent_text || note?.child_text || '';
  const pointsToday = kid.chores
    .filter(c => c.completed && c.category !== 'bonus')
    .reduce((n, c) => n + (c.points_value ?? 0), 0);
  return (
    <section className={styles.dailyReport} data-test="daily-report-preview">
      <h3>
        {t('admin.kidsStatusTab.dailyTitle')}
        <span className={styles.dailyDate} data-test="daily-report-date">
          {new Date().toLocaleDateString()}
        </span>
      </h3>
      <p className={styles.dailyText} data-test="daily-report-text">{text}</p>
      <p className={styles.dailyStats} data-test="daily-report-stats">
        {t('admin.kidsStatusTab.dailyStats', {
          reqDone: b.requiredCompleted + b.coreCompleted,
          reqTotal: b.requiredTotal + b.coreTotal,
          bonusDone: b.bonusCompleted,
          bonusTotal: b.bonusTotal,
          points: pointsToday,
        })}
      </p>
    </section>
  );
};

/** 问题④ v2（用户纠正后）：孩子详情不再是「看历史」月历弹层，而是与
 *  学生端基本一致的日视图——周历条（点日切换）+「月视图」开关（整月
 *  日历在周条原位展开，opencore.png 参考图交互）+ 所选日的任务清单。
 *  与学生端同构：周内数据一次月请求缓存（首开拉一次该月全部行），
 *  点月历日收起月历、清单换成该日。 */
const KidDayView: React.FC<{
  kid: KidStatus;
  today: string;
  /** 2026-10-23 用户定稿：家长端只保留这一份清单（与学生端一致），
   *  请假/代完成不再在下方分类清单里重复出现，而是并入行内。 */
  onExcuse: (e: React.MouseEvent, chore: ScheduledChore) => void;
  onToggle: (e: React.MouseEvent, chore: ScheduledChore) => void;
  togglingKey: string | null;
  /** KIDS-REFRESH：三个写操作（代完成/撤销/请假）成功后 bump，驱动本视图重拉当月清单。 */
  refreshKey?: number;
}> = ({ kid, today, onExcuse, onToggle, togglingKey, refreshKey }) => {
  const { t, i18n } = useTranslation();
  const lang = i18n.language;
  const monthOf = (date: string) => date.slice(0, 7);
  const shift = (key: string, by: number) => {
    const [y, m] = key.split('-').map(Number);
    const d = new Date(y, m - 1 + by, 1);
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
  };
  const weekDates = (day: string) => {
    const d = new Date(`${day}T00:00:00`);
    d.setDate(d.getDate() - (d.getDay() + 6) % 7);
    return Array.from({ length: 7 }, (_, i) => {
      const x = new Date(d);
      x.setDate(d.getDate() + i);
      return `${x.getFullYear()}-${String(x.getMonth() + 1).padStart(2, '0')}-${String(x.getDate()).padStart(2, '0')}`;
    });
  };
  const week = useMemo(() => weekDates(today), [today]);
  const [selected, setSelected] = useState(today);
  const [monthOpen, setMonthOpen] = useState(false);
  // 2026-10-22 用户定稿交互：月视图入口改成周历条下方居中的小箭头
  // （点击或滑动），无「月视图」文字链接——与学生端同构。
  const touchStartY = useRef<number | null>(null);
  // 所选月的全部行：周条与月历共用（与学生端「一次月请求」同构）。
  const [month, setMonth] = useState(() => monthOf(today));
  const [chores, setChores] = useState<ScheduledChore[] | null>(null);
  const [failed, setFailed] = useState(false);

  // KIDS-REFRESH：家长代完成/撤销/请假后，本视图的月清单必须重拉——顶层
  // load() 只刷 kid 汇总（users/balances/pending），不触发展开区内部
  // effect，故由 refreshKey 驱动重拉。换人/换月才清空旧数据；同 scope
  // 重拉保留旧数据平滑覆盖（避免 refreshKey 触发时行清单闪空白）。ref
  // 只做比较与幂等赋值，React 严格模式/双执行不影响正确性。
  const lastScope = useRef('');

  useEffect(() => {
    let live = true;
    setFailed(false);
    const scope = `${kid.user.id}|${month}`;
    if (lastScope.current !== scope) {
      lastScope.current = scope;
      setChores(null);
    }
    api.users.getChores(kid.user.id, 'month', `${month}-01`)
      .then(rows => { if (live) setChores(rows); })
      .catch(() => { if (live) { setFailed(true); setChores([]); } });
    return () => { live = false; };
  }, [kid.user.id, month, refreshKey]);

  const byDate = useMemo(() => {
    const m = new Map<string, ScheduledChore[]>();
    for (const c of chores ?? []) {
      const list = m.get(c.date);
      if (list) list.push(c);
      else m.set(c.date, [c]);
    }
    return m;
  }, [chores]);

  // 点月历日：收起月历、清单换成该日（与学生端 pickDay 一致）。
  const pickDay = useCallback((date: string) => {
    setSelected(date);
    setMonthOpen(false);
  }, []);

  const monthLabel = new Date(`${month}-01T00:00:00`)
    .toLocaleDateString(lang, { year: 'numeric', month: 'long' });
  const weekdays = useMemo(() => {
    const monday = new Date(`${today}T00:00:00`);
    monday.setDate(monday.getDate() - (monday.getDay() + 6) % 7);
    return Array.from({ length: 7 }, (_, i) => {
      const d = new Date(monday);
      d.setDate(monday.getDate() + i);
      return d.toLocaleDateString(lang, { weekday: 'short' });
    });
  }, [lang, today]);
  const cells = useMemo(() => {
    const lead = (new Date(`${month}-01T00:00:00`).getDay() + 6) % 7;
    const [y, m] = month.split('-').map(Number);
    const n = new Date(y, m, 0).getDate();
    return [
      ...Array.from({ length: lead }, (_, i) => ({ blank: i })),
      ...Array.from({ length: n }, (_, i) => ({ day: i + 1 })),
    ];
  }, [month]);
  const dayList = byDate.get(selected) ?? [];

  // 问题⑤：家长端能力覆盖孩子端——任务行加「查看提交」入口，点开
  // Modal 看该次完成提交的附件（图文/视频/音频/PDF）与 AI 审查意见，
  // 与孩子端 SubmissionSheet 同源（api.chores.listAttachments）。
  const [viewing, setViewing] = useState<ScheduledChore | null>(null);

  const statusOf = (c: ScheduledChore) => {
    if (c.completion_status === 'excused') return t('admin.kidsStatusTab.statusExcused');
    if (c.completed && c.completion_status === 'pending') return t('admin.kidsStatusTab.statusPending');
    if (c.completed) return t('admin.kidsStatusTab.statusDone', { points: c.points_value });
    if (c.expired && c.category !== 'bonus') return t('admin.kidsStatusTab.statusOverdue');
    return t('admin.kidsStatusTab.statusIdle', { points: c.points_value });
  };

  const renderChores = (list: ScheduledChore[]) => (
    <ul className={styles.choreList}>
      {list.map(c => {
        const isExcused = c.completion_status === 'excused';
        const isOverdue = !c.completed && c.expired && c.category !== 'bonus';
        const isPending = c.completion_status === 'pending';
        const hasSubmission = !!(c.completion_id || c.photo_url);
        const key = `${c.schedule_id}-${c.date}`;
        const toggleLabel = c.completed
          ? t('admin.kidsStatusTab.markNotDone', { title: c.title, name: kid.user.name })
          : t('admin.kidsStatusTab.markDone', { title: c.title, name: kid.user.name });
        return (
          <li key={key} className={clsx(styles.chore, c.completed && styles.choreDone)}>
            {isExcused ? (
              <span className={clsx(styles.ring, styles.ringExcused)} aria-hidden>
                <Icon name="check" />
              </span>
            ) : (
              <button
                type="button"
                className={styles.check}
                onClick={e => onToggle(e, c)}
                disabled={togglingKey !== null}
                aria-label={toggleLabel}
                title={toggleLabel}
              >
                <span className={clsx(
                  styles.ring,
                  c.completed && !isPending && styles.ringDone,
                  isPending && styles.ringWaiting,
                )}>
                  {togglingKey === key
                    ? <Loader2 aria-hidden className={ui.spin} />
                    : c.completed && <Icon name="check" />}
                </span>
              </button>
            )}
            <IconWell icon={c.icon} cat={catFromCategory(c.category)} className={styles.choreWell} />
            <span className={styles.choreText}>
              <span className={styles.choreTitle}>{c.title}</span>
              <span className={clsx(
                styles.choreStatus,
                isOverdue && styles.statusUrgent,
                isPending && styles.statusWaiting,
              )}>
                {statusOf(c)}
              </span>
            </span>
            {hasSubmission && (
              <button
                type="button"
                className={styles.viewSubmissionBtn}
                data-test="view-submission"
                onClick={() => setViewing(c)}
              >
                <Icon name="camera" size={16} />
                <span>{t('kid.submission.viewBtn')}</span>
              </button>
            )}
            {!c.completed && !isExcused && (
              <button
                type="button"
                className={styles.excuse}
                onClick={e => onExcuse(e, c)}
                title={t('admin.kidsStatusTab.excuseButtonTooltip')}
              >
                {t('admin.kidsStatusTab.excuseButton')}
              </button>
            )}
          </li>
        );
      })}
    </ul>
  );

  return (
    <div className={styles.dayView} data-test="kid-day-view">
      {/* 周历条 / 月历：与学生端同款——「月视图」开关在原位展开整月日历。 */}
      {monthOpen ? (
        <div className={styles.historyView} data-test="kid-history-view">
          <div className={styles.historyBar}>
            <button
              type="button"
              className={styles.historyStep}
              onClick={() => setMonth(m => shift(m, -1))}
              aria-label={t('admin.kidsStatusTab.historyPrev')}
            >
              <Icon name="back" />
            </button>
            <span className={styles.historyMonth}>
              {monthLabel}
              <Icon name="chev" className={styles.historyMonthChev} />
            </span>
            <button
              type="button"
              className={styles.historyStep}
              onClick={() => setMonth(m => shift(m, 1))}
              disabled={month >= monthOf(today)}
              aria-label={t('admin.kidsStatusTab.historyNext')}
            >
              <Icon name="chev" />
            </button>
            <button
              type="button"
              className={styles.historyClose}
              onClick={() => setMonthOpen(false)}
            >
              {t('admin.kidsStatusTab.historyClose')}
            </button>
          </div>
          {chores === null && <p className={ui.emptyInline}>{t('admin.kidsStatusTab.historyLoading')}</p>}
          {chores !== null && (
            <div className={styles.historyCal} data-test="hist-days" role="grid" aria-label={monthLabel}>
              {weekdays.map(w => (
                <span key={w} className={styles.historyDow} aria-hidden>{w}</span>
              ))}
              {cells.map(c =>
                'blank' in c ? (
                  <span key={`b${c.blank}`} className={styles.historyDayBlank} aria-hidden />
                ) : (
                  (() => {
                    const date = `${month}-${String(c.day).padStart(2, '0')}`;
                    const list = byDate.get(date) ?? [];
                    const done = list.filter(x => x.completed).length;
                    return (
                      <button
                        key={date}
                        type="button"
                        className={styles.historyDay}
                        disabled={list.length === 0}
                        aria-pressed={selected === date}
                        aria-label={list.length
                          ? t('admin.kidsStatusTab.historyDayAria', { day: c.day, done, total: list.length })
                          : t('admin.kidsStatusTab.historyDayEmpty', { day: c.day })}
                        onClick={() => pickDay(date)}
                      >
                        <span
                          className={styles.historyRing}
                          data-today={date === today || undefined}
                          data-full={list.length > 0 && done >= list.length || undefined}
                        >
                          <span className={styles.historyNum}>{c.day}</span>
                        </span>
                        <span className={styles.historyCount}>{list.length ? `${done}/${list.length}` : ''}</span>
                      </button>
                    );
                  })()
                ),
              )}
            </div>
          )}
        </div>
      ) : (
        <div
          className={styles.weekStrip}
          role="group"
          aria-label={t('kid.week.days')}
          onTouchStart={e => { touchStartY.current = e.touches[0].clientY; }}
          onTouchEnd={e => {
            if (touchStartY.current == null) return;
            const dy = e.changedTouches[0].clientY - touchStartY.current;
            touchStartY.current = null;
            if (Math.abs(dy) < 24) return;
            if (dy < 0) setMonthOpen(true);
          }}
        >
          {week.map(date => {
            const d = new Date(`${date}T00:00:00`);
            const list = byDate.get(date) ?? [];
            const done = list.filter(x => x.completed).length;
            return (
              <button
                key={date}
                type="button"
                className={clsx(styles.weekDay, date === today && styles.weekDayToday, date === selected && styles.weekDaySelected)}
                aria-pressed={date === selected}
                aria-current={date === today ? 'date' : undefined}
                onClick={() => setSelected(date)}
              >
                <span className={styles.weekDayName} aria-hidden>{d.toLocaleDateString(lang, { weekday: 'short' })}</span>
                <span
                  className={styles.weekDayRing}
                  data-today={date === today || undefined}
                  data-full={list.length > 0 && done >= list.length || undefined}
                  data-partial={list.length > 0 && done < list.length || undefined}
                >
                  <span className={styles.weekDayNum}>{d.getDate()}</span>
                </span>
              </button>
            );
          })}
        </div>
      )}

      {/* 所选日标签一行；月视图入口是周历条下方居中的小箭头
          （点击或滑动，无文字链接，与学生端同构）。 */}
      <div className={styles.dayViewBar}>
        <span className={styles.dayViewLabel} data-test="history-day-label">
          {new Date(`${selected}T00:00:00`).toLocaleDateString(lang, { month: 'long', day: 'numeric', weekday: 'short' })}
          {dayList.length > 0 && ` · ${dayList.filter(c => c.completed).length}/${dayList.length}`}
        </span>
      </div>
      <button
        type="button"
        className={styles.monthArrow}
        data-test="month-arrow"
        aria-expanded={monthOpen}
        aria-label={monthOpen ? t('kid.history.collapse') : t('kid.history.expand')}
        onClick={() => setMonthOpen(v => !v)}
      >
        <Icon name="chev" className={monthOpen ? styles.monthArrowChevUp : styles.monthArrowChev} />
      </button>

      {failed && <p className={ui.msgError} role="alert">{t('admin.kidsStatusTab.loadError')}</p>}
      {chores !== null && dayList.length === 0 && !failed && (
        <p className={ui.emptyInline}>{t('admin.kidsStatusTab.historyDayEmptyList')}</p>
      )}
      {chores !== null && dayList.length > 0 && renderChores(dayList)}
      {viewing && <SubmissionViewModal chore={viewing} onClose={() => setViewing(null)} />}
    </div>
  );
};

/** 问题⑤：单个提交的查看弹层——附件（图文/视频/音频/PDF）+ AI 审查
 *  意见 + 完成时间，家长端覆盖孩子端 SubmissionSheet 的能力。 */
const SubmissionViewModal: React.FC<{ chore: ScheduledChore; onClose: () => void }> = ({ chore, onClose }) => {
  const { t } = useTranslation();
  const [attachments, setAttachments] = useState<CompletionAttachment[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [zoomed, setZoomed] = useState<string | null>(null);
  // 点开的非图片附件（视频/音频/PDF）在网格下方 inline 播放。
  const [active, setActive] = useState<string | null>(null);

  useEffect(() => {
    setActive(null);
    if (!chore.completion_id) {
      setAttachments([]);
      return;
    }
    let live = true;
    api.chores.listAttachments(chore.completion_id)
      .then(atts => { if (live) setAttachments(atts); })
      .catch(() => { if (live) { setFailed(true); setAttachments([]); } });
    return () => { live = false; };
  }, [chore.completion_id]);

  const previews = useMemo(() => (attachments ?? []).map(a => ({
    kind: a.mime_class,
    url: a.file_path,
    name: a.display_name,
    feedback: a.ai_feedback,
  })), [attachments]);
  const legacyPhoto = chore.photo_url && !previews.some(p => p.url === chore.photo_url)
    ? chore.photo_url
    : null;
  const doneAt = chore.completed_at ? new Date(chore.completed_at) : null;
  const doneAtLine = doneAt && !Number.isNaN(doneAt.getTime()) ? doneAt.toLocaleString() : null;

  return (
    <Modal
      isOpen
      onClose={onClose}
      title={t('kid.submission.title')}
      maxWidth="640px"
    >
      <div className={styles.subView} data-test="admin-submission-view">
        <p className={styles.subViewLead}>
          <strong>{chore.title}</strong>
          <span>
            {doneAtLine
              ? t('kid.submission.doneAt', { time: doneAtLine })
              : t('kid.submission.doneLabel')}
          </span>
        </p>

        {chore.ai_feedback && (
          <div className={styles.subAiNote} data-test="submission-ai-note">
            <span className={styles.subAiTitle}>{t('kid.submission.aiNote')}</span>
            <p>{chore.ai_feedback}</p>
          </div>
        )}

        <p className={styles.subCaption}>{t('kid.submission.attsCaption')}</p>

        {attachments === null && <p className={ui.emptyInline} role="status">{t('kid.submission.loading')}</p>}
        {attachments !== null && failed && <p className={ui.msgError} role="alert">{t('kid.submission.loadFailed')}</p>}
        {attachments !== null && previews.length === 0 && !legacyPhoto && (
          <p className={ui.emptyInline}>{t('kid.submission.emptyText')}</p>
        )}

        {attachments !== null && (previews.length > 0 || !!legacyPhoto) && (
          <div className={styles.subGrid} data-test="admin-submission-grid" role="group">
            {legacyPhoto && (
              <button
                type="button"
                className={styles.subTile}
                onClick={() => setZoomed(legacyPhoto)}
                aria-label={t('kid.submission.legacyPhoto')}
              >
                <span className={styles.subBadge}>{t('kid.photo.cls_image')}</span>
                <img src={legacyPhoto} alt="" loading="lazy" />
              </button>
            )}
            {previews.map((p, i) => (
              <button
                key={i}
                type="button"
                className={clsx(styles.subTile, active === p.url && styles.subTileActive)}
                onClick={() => {
                  if (p.kind === 'image') setZoomed(p.url);
                  else setActive(active === p.url ? null : p.url);
                }}
                aria-label={t('kid.photo.attAria', { index: i + 1, name: p.name, cls: t(`kid.photo.cls_${p.kind}`) })}
              >
                <span className={styles.subBadge}>{t(`kid.photo.cls_${p.kind}`)}</span>
                {p.kind === 'image' && <img src={p.url} alt="" loading="lazy" />}
                {p.kind === 'video' && <span className={styles.subDocIcon}><Icon name="film" /></span>}
                {p.kind === 'audio' && <span className={styles.subDocIcon}><Icon name="mic" /></span>}
                {p.kind === 'pdf' && <span className={styles.subDocIcon}><Icon name="book" /></span>}
                <span className={styles.subName}>{p.name}</span>
                {p.feedback && <span className={styles.subTileFeedback}>{p.feedback}</span>}
              </button>
            ))}
          </div>
        )}

        {/* 非图片附件 inline 播放：点网格里的视频/音频/PDF 才出现，再点收起。 */}
        {(() => {
          const current = previews.find(p => p.url === active);
          if (!current) return null;
          return (
            <div className={styles.subPlayer} data-test="admin-submission-player">
              {current.kind === 'video' && <video src={current.url} controls playsInline />}
              {current.kind === 'audio' && <audio src={current.url} controls />}
              {current.kind === 'pdf' && (
                <object data={current.url} type="application/pdf" aria-label={current.name}>
                  <a href={current.url} target="_blank" rel="noreferrer">{t('kid.submission.openPdf')}</a>
                </object>
              )}
              <p className={styles.subPlayerName}>{current.name}</p>
            </div>
          );
        })()}

        {zoomed && (
          <div
            className={styles.subZoom}
            role="dialog"
            aria-modal="true"
            aria-label={t('kid.submission.legacyPhoto')}
            onClick={() => setZoomed(null)}
          >
            <img src={zoomed} alt="" />
            <p>{t('kid.photo.referenceZoomHint')}</p>
          </div>
        )}
      </div>
    </Modal>
  );
};

export const KidsStatusTab: React.FC<{ onPendingChange?: (count: number) => void }> = ({ onPendingChange }) => {
  const { t, i18n } = useTranslation();
  const [kids, setKids] = useState<KidStatus[]>([]);
  const [allUsers, setAllUsers] = useState<User[]>([]);
  const [pending, setPending] = useState<PendingCompletion[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const [refreshing, setRefreshing] = useState(false);
  const [now, setNow] = useState(() => new Date());
  // KIDS-REFRESH：家长代完成/撤销/请假成功后 bump，让展开区 KidDayView
  // 重拉当月清单——顶层 load() 只刷 kid 汇总（users/balances/pending），
  // 不触发展开区内部的月数据 effect，行状态（completed/Undo）不会自己变。
  // 只在写操作成功路径 bump，不放进 load()（初始 mount 会多打一次请求）。
  const [dayRefresh, setDayRefresh] = useState(0);

  const today = localDateStr(new Date());

  // The glance bar reads the clock, so it re-evaluates while the tab is open
  // (a chore slipping past its due time turns yellow into red without a
  // reload — plan A computes everything client-side on open).
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 60_000);
    return () => clearInterval(id);
  }, []);

  const load = useCallback(async () => {
    setRefreshing(true);
    setError(null);
    try {
      const [users, balances, pendingList] = await Promise.all([
        api.users.list(),
        api.points.getAllBalances().catch(() => [] as PointBalance[]),
        api.chores.listPending().catch(() => [] as PendingCompletion[]),
      ]);
      setAllUsers(users);
      setPending(pendingList);
      onPendingChange?.(pendingList.length);

      // Kids first, then parents (who take part too and show up once they
      // have chores of their own).
      const children = users
        .filter((u: User) => !u.paused || u.role === 'child')
        .sort((a, b) => (a.role === b.role ? a.name.localeCompare(b.name) : a.role === 'child' ? -1 : 1));

      // Attribute pending approvals to the assignee (the kid the chore
      // belongs to), not the completer. Matching by name would collapse
      // duplicate names and would miss the sibling-completing case.
      const pendingByAssignee = new Map<number, number>();
      for (const p of pendingList) {
        pendingByAssignee.set(p.assigned_user_id, (pendingByAssignee.get(p.assigned_user_id) || 0) + 1);
      }

      const results = await Promise.all(
        children.map(async (kid): Promise<KidStatus> => {
          try {
            const [chores, streakData, coachNote, petState, analyses] = await Promise.all([
              api.users.getChores(kid.id, 'daily', today),
              api.streaks.getForUser(kid.id).catch<UserStreakData>(() => ({
                current_streak: 0,
                longest_streak: 0,
                earned_rewards: [],
              })),
              // Today's coach note (parent view: observation + the kid's line).
              api.coach.getNote(kid.id).catch<DailyCoachNote | null>(() => null),
              // Their pet's mood for the status card (P4-13 admin view).
              // 404 (feature off) or load errors just hide the line.
              api.pet.getForAdmin(kid.id).catch<PetResponse | null>(() => null),
              // The freshest finished paper analysis (L1 quick-report);
              // no analyses yet just hides the card.
              api.admin.listKidAnalyses(kid.id, 1).catch<PaperAnalysis[]>(() => []),
            ]);
            const bal = balances.find(b => b.user_id === kid.id)?.balance ?? 0;
            return {
              user: kid,
              chores,
              balance: bal,
              streak: streakData.current_streak,
              pendingApprovals: pendingByAssignee.get(kid.id) || 0,
              coachNote: coachNote && (coachNote.parent_text || coachNote.child_text) ? coachNote : null,
              pet: petState && 'name' in petState ? petState : null,
              latestAnalysis: analyses.length > 0 ? analyses[0] : null,
              loadError: false,
            };
          } catch (e) {
            console.error('Failed to load kid status', kid.id, e);
            return {
              user: kid,
              chores: [],
              balance: balances.find(b => b.user_id === kid.id)?.balance ?? 0,
              streak: 0,
              pendingApprovals: pendingByAssignee.get(kid.id) || 0,
              coachNote: null,
              pet: null,
              latestAnalysis: null,
              loadError: true,
            };
          }
        }),
      );

      setKids(results.filter(k => k.user.role === 'child' || k.chores.length > 0));
    } catch {
      setError(t('admin.kidsStatusTab.loadError'));
    } finally {
      setLoading(false);
      setRefreshing(false);
    }
  }, [today, t, onPendingChange]);

  const handleExcuse = async (e: React.MouseEvent, chore: ScheduledChore) => {
    e.stopPropagation();
    const promptMsg = t('admin.kidsStatusTab.excusePrompt', { title: chore.title }) || `Excuse "${chore.title}" for today? Enter optional reason:`;
    const reason = window.prompt(promptMsg);
    if (reason === null) return; // user cancelled prompt
    try {
      await api.chores.excuse(chore.schedule_id, chore.date, reason);
      await load();
      setDayRefresh(n => n + 1);
    } catch (err) {
      console.error('Failed to excuse chore:', err);
    }
  };

  // Parents can tick a chore off (or undo it) on someone's behalf — the
  // assignee is credited, and no photo is needed since the parent vouches.
  const [toggling, setToggling] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const handleToggle = async (e: React.MouseEvent, chore: ScheduledChore) => {
    e.stopPropagation();
    const key = `${chore.schedule_id}-${chore.date}`;
    if (toggling) return;
    if (chore.completed && !window.confirm(t('admin.kidsStatusTab.confirmUndo', { title: chore.title, name: kids.find(k => k.user.role === 'child')?.user.name ?? '' }))) {
      return;
    }
    setToggling(key);
    setActionError(null);
    try {
      if (chore.completed) {
        await api.chores.uncomplete(chore.schedule_id, chore.date);
      } else {
        await api.chores.complete(chore.schedule_id, chore.date);
      }
      await load();
      setDayRefresh(n => n + 1);
    } catch (err) {
      setActionError(t('admin.kidsStatusTab.toggleError', {
        title: chore.title,
        error: err instanceof Error ? err.message : String(err),
      }));
    } finally {
      setToggling(null);
    }
  };

  useEffect(() => { load(); }, [load]);

  // The three-colour risk bar: one row over everyone's day (red > yellow >
  // green). Excusing from the bar closes the loop — the red state is the
  // only one that carries a button.
  const glance: TodayGlance | null = kids.length > 0
    ? glanceFor(kids.map(k => ({ user: k.user, chores: k.chores })), now)
    : null;

  const handleExcuseAll = async () => {
    if (!glance) return;
    const names = Array.from(new Set(glance.overdue.map(o => o.user.name))).join('、');
    if (!window.confirm(t('admin.kidsStatusTab.glance.excuseAllConfirm', { count: glance.overdue.length, names }))) return;
    let excused = 0;
    for (const o of glance.overdue) {
      try {
        await api.chores.excuse(o.chore.schedule_id, o.chore.date, undefined);
        excused += 1;
      } catch (err) {
        console.error('Failed to excuse chore from the glance bar', o.chore.schedule_id, err);
      }
    }
    // 全部失败时不刷新也不 bump：数据没变，刷新只会闪一下。
    if (excused > 0) {
      await load();
      setDayRefresh(n => n + 1);
    }
  };

  const toggleExpand = (id: number) => {
    setExpanded(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  // The quick-report's "see the report" opens the diagnosis modal with the
  // stored analysis — no re-run, just the report as it finished.
  const [quickAnalysis, setQuickAnalysis] = useState<PaperAnalysis | null>(null);
  const [quickKidId, setQuickKidId] = useState<number | null>(null);

  if (loading) return <p className={ui.emptyInline}>{t('admin.kidsStatusTab.loading')}</p>;

  const dateLabel = new Date().toLocaleDateString(i18n.language, { weekday: 'long', month: 'long', day: 'numeric' });

  return (
    <div className={ui.page} data-test="admin-home-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>{t('admin.kidsStatusTab.heading')}</h2>
          <p className={ui.pageSub}>{t('admin.kidsStatusTab.subtitle', { date: dateLabel })}</p>
        </div>
        <button
          type="button"
          className={ui.btnGhost}
          onClick={load}
          disabled={refreshing}
          title={t('admin.kidsStatusTab.refreshTitle')}
        >
          <RefreshCw aria-hidden className={refreshing ? ui.spin : undefined} /> {t('admin.kidsStatusTab.refreshLabel')}
        </button>
      </div>

      {error && <p className={ui.msgError} role="alert">{error}</p>}

      {/* The three-colour "today at a glance" bar: green on track, yellow
          something is due within the hour, red a must-do missed its window
          and the bonus gate is at risk (P2-8, plan A — no backend). */}
      {glance && (
        <section
          className={clsx(
            styles.glance,
            glance.level === 'green' && styles.glanceGreen,
            glance.level === 'yellow' && styles.glanceYellow,
            glance.level === 'red' && styles.glanceRed,
          )}
          role="status"
          aria-label={t('admin.kidsStatusTab.glance.label')}
        >
          <span className={styles.glanceDot} aria-hidden />
          <div className={styles.glanceBody}>
            <p className={styles.glanceTitle}>
              {t('admin.kidsStatusTab.glance.progress', { done: glance.done, total: glance.total })}
              {glance.level === 'yellow' && glance.dueSoon && (
                <span className={styles.glanceSub}>
                  {t('admin.kidsStatusTab.glance.dueSoon', {
                    title: glance.dueSoon.chore.title,
                    name: glance.dueSoon.user.name,
                    count: glance.dueSoonCount,
                  })}
                </span>
              )}
              {glance.level === 'red' && (
                <span className={styles.glanceSub}>
                  {t('admin.kidsStatusTab.glance.missed', { count: glance.overdue.length })}
                </span>
              )}
            </p>
            <p className={styles.glanceDetail}>
              {glance.level === 'green' && t('admin.kidsStatusTab.glance.greenDetail')}
              {glance.level === 'yellow' && t('admin.kidsStatusTab.glance.yellowDetail')}
              {glance.level === 'red' && t('admin.kidsStatusTab.glance.redDetail')}
            </p>
          </div>
          {glance.level === 'red' && (
            <button type="button" className={styles.glanceExcuse} onClick={handleExcuseAll}>
              {t('admin.kidsStatusTab.glance.excuseButton')}
            </button>
          )}
          {glance.level === 'yellow' && (
            <button
              type="button"
              className={styles.glanceRemind}
              data-test="remind-now"
              // The prototype's "remind-now": a nudge that's a word from the
              // parent, not a push. Nothing to send — the tap is the reminder,
              // the note says so and the bar moves on with the next refresh.
              onClick={() => window.alert(t('admin.kidsStatusTab.glance.yellowDetail'))}
            >
              {t('admin.kidsStatusTab.glance.remindButton')}
            </button>
          )}
        </section>
      )}

      {pending.length > 0 && (
        <section className={ui.section} aria-labelledby="waiting-for-you">
          <div className={ui.sectionHead}>
            <h3 className={ui.sectionTitle} id="waiting-for-you">
              <Icon name="clock" />
              {t('admin.kidsStatusTab.waitingHeading')}
              <span className={ui.badgeWaiting}>{pending.length}</span>
            </h3>
          </div>
          <ApprovalList pending={pending} users={allUsers} onChanged={load} />
        </section>
      )}

      {/* The L1 quick-report: one card per kid with a finished analysis,
          right under the pending approvals — the parent's first screen. */}
      {kids.filter(k => k.latestAnalysis).map(kid => (
        <QuickAnalysisCard
          key={`quick-${kid.user.id}`}
          kid={kid}
          onOpen={a => { setQuickAnalysis(a); setQuickKidId(kid.user.id); }}
        />
      ))}
      {/* OD daily-report-preview: the freshest coach note of the day as a
          standalone card — the parent's summary before the per-kid rows. */}
      {kids.filter(k => k.coachNote).slice(0, 1).map(kid => (
        <DailyReportPreview key={`daily-${kid.user.id}`} kid={kid} />
      ))}
      {quickAnalysis && quickKidId != null && (
        <PaperAnalysisModal
          completionId={quickAnalysis.completion_id}
          initialAnalysis={quickAnalysis}
          onClose={() => { setQuickAnalysis(null); setQuickKidId(null); }}
          onToast={() => { /* toasts live on the modal's own surface */ }}
        />
      )}

      {kids.length === 0 && (
        <div className={ui.empty}>
          <Icon name="people" />
          <p>{t('admin.kidsStatusTab.noChildren')}</p>
        </div>
      )}

      <div className={styles.grid}>
        {kids.map(kid => {
          const b = breakdownFor(kid.chores);
          // The bar shows Must do + Every day only. Bonus points are gated on
          // those (CLAUDE.md), so bonus progress only shows once it's open.
          const gatedTotal = b.requiredTotal + b.coreTotal;
          const gatedCompleted = b.requiredCompleted + b.coreCompleted;
          const gatedPct = gatedTotal > 0 ? (gatedCompleted / gatedTotal) * 100 : 0;
          const allRequiredAndCoreDone = gatedTotal > 0 && gatedCompleted === gatedTotal;
          const bonusUnlocked = allRequiredAndCoreDone && b.bonusTotal > 0;
          const hasAlert = b.overdue > 0;
          const isExpanded = expanded.has(kid.user.id);
          const totalChores = gatedTotal + b.bonusTotal;
          const detailsId = `kid-${kid.user.id}-details`;
          const color = personColor(kid.user);
          const counts: { cat: 'required' | 'core' | 'bonus'; done: number; total: number }[] = [
            { cat: 'required', done: b.requiredCompleted, total: b.requiredTotal },
            { cat: 'core', done: b.coreCompleted, total: b.coreTotal },
            { cat: 'bonus', done: b.bonusCompleted, total: b.bonusTotal },
          ];

          return (
            <div
              key={kid.user.id}
              className={clsx(styles.card, kid.user.paused && styles.cardPaused)}
              data-person={color}
            >
              <button
                type="button"
                className={styles.header}
                onClick={() => toggleExpand(kid.user.id)}
                aria-expanded={isExpanded}
                aria-controls={detailsId}
              >
                <span className={styles.who}>
                  <Avatar name={kid.user.name} color={color} size="md" />
                  <span className={styles.nameBlock}>
                    <span className={styles.name}>{kid.user.name}</span>
                    <span className={styles.tags}>
                      {kid.user.paused && <span className={ui.badgeOutline}>{t('admin.kidsStatusTab.paused')}</span>}
                      {hasAlert && (
                        <span className={ui.badgeUrgent}>
                          <Icon name="clock" /> {t('admin.kidsStatusTab.overdue', { count: b.overdue })}
                        </span>
                      )}
                      {!hasAlert && allRequiredAndCoreDone && (
                        <span className={ui.badge}>
                          <Icon name="check" /> {t('admin.kidsStatusTab.allDone')}
                        </span>
                      )}
                    </span>
                  </span>
                  <Icon name="chev" className={clsx(styles.caret, isExpanded && styles.caretOpen)} />
                </span>

                {totalChores === 0 ? (
                  <span className={styles.noChores}>{t('admin.kidsStatusTab.noChoresScheduled')}</span>
                ) : (
                  <>
                    <span className={styles.progressRow}>
                      <span className={styles.bar}>
                        {gatedTotal > 0 && <i style={{ width: `${gatedPct}%` }} />}
                      </span>
                      <span className={styles.progressCount}>
                        {t('admin.kidsStatusTab.progress', { done: gatedCompleted, total: gatedTotal })}
                      </span>
                    </span>
                    <span className={styles.cats}>
                      {counts.filter(c => c.total > 0).map(c => (
                        <span key={c.cat} className={styles.catCount}>
                          <CategoryMark cat={catFromCategory(c.cat)} done={c.done === c.total} />
                          <span className={ui.srOnlyText}>{t(`design.category.${catFromCategory(c.cat)}`)}</span>
                          {c.done}/{c.total}
                        </span>
                      ))}
                    </span>
                    {b.bonusTotal > 0 && !bonusUnlocked && (
                      <span className={styles.hint}>
                        <Icon name="lock" /> {t('admin.kidsStatusTab.bonusHint')}
                      </span>
                    )}
                  </>
                )}

                <span className={styles.stats}>
                  <span><Icon name="flame" fill className={styles.flame} /> {t('admin.kidsStatusTab.streak', { count: kid.streak })}</span>
                  <span><Icon name="star" fill className={styles.star} /> {t('admin.kidsStatusTab.points', { count: kid.balance })}</span>
                  {kid.pet && (
                    <span className={styles.petStat} title={t('admin.kidsStatusTab.petStatLabel')}>
                      <Icon name="paw" />{' '}
                      {t('admin.kidsStatusTab.petStat', {
                        name: kid.pet.name,
                        mood: t(`kid.pet.mood${kid.pet.mood.charAt(0).toUpperCase()}${kid.pet.mood.slice(1)}`),
                      })}
                    </span>
                  )}
                  {kid.pendingApprovals > 0 && (
                    <span className={styles.waiting}>
                      <Icon name="clock" /> {t('admin.kidsStatusTab.awaitingApproval', { count: kid.pendingApprovals })}
                    </span>
                  )}
                  {b.pendingOnToday > 0 && b.pendingOnToday !== kid.pendingApprovals && (
                    <span className={styles.waiting}>
                      <Icon name="clock" /> {t('admin.kidsStatusTab.pendingToday', { count: b.pendingOnToday })}
                    </span>
                  )}
                </span>
              </button>

              {isExpanded && (
                <div id={detailsId} className={styles.details}>
                  {/* 2026-10-23 用户定稿（第三次反馈）：家长端展开区与学生端
                      完全一致——只保留 KidDayView 这一份日清单，功能差别
                      （请假/代完成/查看提交）全部并入行内；下方不再有重复的
                      分类清单。 */}
                  <KidDayView
                    kid={kid}
                    today={today}
                    onExcuse={handleExcuse}
                    onToggle={handleToggle}
                    togglingKey={toggling}
                    refreshKey={dayRefresh}
                  />
                  {kid.loadError && (
                    <p className={ui.msgError}>{t('admin.kidsStatusTab.childLoadError')}</p>
                  )}
                  {/* Today's coach note: the parent-side observation first, the
                      kid's line below it (what they saw on their screen). */}
                  {kid.coachNote && (
                    <div className={styles.coachNote}>
                      <p className={styles.coachKicker}>
                        <Icon name="star" fill /> {t('admin.kidsStatusTab.coachToday')}
                      </p>
                      {kid.coachNote.parent_text && <p className={styles.coachParent}>{kid.coachNote.parent_text}</p>}
                      {kid.coachNote.child_text && (
                        <p className={styles.coachChild}>{t('admin.kidsStatusTab.coachSaid', { text: kid.coachNote.child_text })}</p>
                      )}
                    </div>
                  )}
                  {!kid.loadError && kid.chores.length === 0 && (
                    <p className={ui.emptyInline}>{t('admin.kidsStatusTab.noChoresScheduledDetail')}</p>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>

      {actionError && (
        <p className={ui.msgError} role="alert">{actionError}</p>
      )}

      <p className={styles.tapHint}>{t('admin.kidsStatusTab.tapHint')}</p>
    </div>
  );
};
