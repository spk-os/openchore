// The parent's L1 paper analysis (OD prototype parent-analysis.html is the
// UI source of truth): from an approved/pending completion with image
// proof, start the async two-stage parse, poll it (cutting → judging →
// report), then read the diagnosis — error mix, weakest knowledge points,
// per-question verdicts with a one-tap correction task each, and the
// manual-fix queue for questions the AI could not judge.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { AnalysisQuestion, ErrorItem, PaperAnalysis } from '../../types';
import { subjectOptionLabel } from '../../subjects';
import { useSubjects } from '../../hooks/useSubjects';
import { MathText } from '../MathText';
import { Icon } from '../../design';
import Modal from '../Modal/Modal';
import ui from './ui.module.css';
import s from './PaperAnalysisCard.module.css';

const errorTypeLabel = (t: (k: string) => string, type?: string) => {
  switch (type) {
    case 'calculation': return t('admin.analysis.errorTypeCalculation');
    case 'concept': return t('admin.analysis.errorTypeConcept');
    case 'reading': return t('admin.analysis.errorTypeReading');
    case 'method': return t('admin.analysis.errorTypeMethod');
    default: return t('admin.analysis.errorTypeUnknown');
  }
};

/** One step of the three-step progress rail (cutting → judging → report). */
const StepRail: React.FC<{ analysis: PaperAnalysis }> = ({ analysis }) => {
  const { t } = useTranslation();
  const steps = [
    { key: 'segmenting', label: t('admin.analysis.stageSegmenting') },
    { key: 'judging', label: t('admin.analysis.stageJudging') },
    { key: 'done', label: t('admin.analysis.stageDone') },
  ];
  const activeIdx = analysis.stage === 'queued' ? 0
    : analysis.stage === 'segmenting' ? 0
    : analysis.stage === 'judging' ? 1 : 2;
  return (
    <ol className={s.steps} data-test="pa-steps">
      {steps.map((st, i) => (
        <li
          key={st.key}
          className={`${s.step} ${i < activeIdx ? s.stepDone : ''} ${i === activeIdx && analysis.status === 'running' ? s.stepActive : ''}`}
          aria-current={i === activeIdx && analysis.status === 'running' ? 'step' : undefined}
        >
          <span className={s.stepDot} />
          <span>{i + 1}. {st.label}</span>
        </li>
      ))}
    </ol>
  );
};

/** The OD pa-paper-thumb: a thumbnail of the page being parsed — rule
 *  lines for the printed questions plus three dashed cut boxes marking
 *  where the segmenter has been, where the judge is, and what is left. */
const PaperThumb: React.FC<{ analysis: PaperAnalysis }> = ({ analysis }) => {
  const { t } = useTranslation();
  // The three boxes' captions track the live stage: everything cut so
  // far, the judge's current position (progress-blended), and the rest.
  const total = analysis.total_questions > 0 ? analysis.total_questions : 0;
  const judged = Math.max(1, Math.round(analysis.progress / 100 * Math.max(total, 1)));
  const cutting = analysis.stage === 'segmenting' || analysis.stage === 'queued';
  return (
    <div
      className={s.paperThumb}
      data-test="pa-paper-thumb"
      role="img"
      aria-label={t('admin.analysis.thumbAria')}
    >
      <i className={s.pl} /><i className={`${s.pl} ${s.w9}`} /><i className={`${s.pl} ${s.w7}`} />
      <i className={`${s.pl} ${s.w9}`} /><i className={`${s.pl} ${s.w5}`} /><i className={`${s.pl} ${s.w9}`} />
      <i className={`${s.pl} ${s.w7}`} />
      <span className={s.bbox}>
        <b>{cutting ? t('admin.analysis.cut1Active') : t('admin.analysis.thumbBoxCut', { count: total })}</b>
      </span>
      <span className={s.bbox}>
        <b>{t('admin.analysis.thumbBoxJudging', { judged: cutting ? 0 : judged, total })}</b>
      </span>
      <span className={`${s.bbox} ${s.bboxWait}`}>
        <b>{t('admin.analysis.thumbBoxWait')}</b>
      </span>
    </div>
  );
};

/** One of the OD pa-cut-{1,2,3} mini cards: the stage's one-line state,
 *  colour-coded by left border (done / active / not-yet). */
const CutMini: React.FC<{ test: string; tone: 'done' | 'star' | 'line'; title: string; sub: string }> = ({ test, tone, title, sub }) => (
  <div className={`${s.mini} ${tone === 'done' ? s.miniDone : tone === 'star' ? s.miniStar : s.miniLine}`} data-test={test}>
    <div>
      <b>{title}</b>
      <p>{sub}</p>
    </div>
  </div>
);

/** The diagnosis report: stats, error mix, weak points, per-question rows. */
const ReportView: React.FC<{
  analysis: PaperAnalysis;
  onToast: (msg: string) => void;
  onClose: () => void;
}> = ({ analysis, onToast, onClose }) => {
  const { t } = useTranslation();
  const summary = analysis.summary;
  const [taskFor, setTaskFor] = useState<Record<number, 'busy' | 'done'>>({});
  const [bulkBusy, setBulkBusy] = useState(false);
  const [bulkDone, setBulkDone] = useState(false);
  const [distillBusy, setDistillBusy] = useState(false);
  const [distilled, setDistilled] = useState<number | null>(null);

  const makeTask = async (q: AnalysisQuestion) => {
    if (!q.error_item_id || taskFor[q.error_item_id]) return;
    setTaskFor(prev => ({ ...prev, [q.error_item_id!]: 'busy' }));
    try {
      await api.admin.createCorrectionTask(q.error_item_id);
      setTaskFor(prev => ({ ...prev, [q.error_item_id!]: 'done' }));
      onToast(t('admin.analysis.taskCreated'));
    } catch {
      setTaskFor(prev => { const n = { ...prev }; delete n[q.error_item_id!]; return n; });
      onToast(t('admin.analysis.taskCreateFailed'));
    }
  };

  // The wrong questions this report can turn into tasks, in order.
  const wrongs = (summary?.questions ?? []).filter(q => q.is_wrong && q.error_item_id);

  // OD pa-gen-tasks: every wrong question becomes a correction task in one
  // tap, instead of six taps on six rows.
  const makeAllTasks = async () => {
    if (bulkBusy || bulkDone) return;
    setBulkBusy(true);
    let ok = 0;
    for (const q of wrongs) {
      if (taskFor[q.error_item_id!] === 'done') continue;
      try {
        await api.admin.createCorrectionTask(q.error_item_id!);
        setTaskFor(prev => ({ ...prev, [q.error_item_id!]: 'done' }));
        ok++;
      } catch { /* the single-row button still shows for retries */ }
    }
    setBulkBusy(false);
    if (ok > 0) {
      setBulkDone(true);
      onToast(t('admin.analysis.genTasksDone', { count: ok }));
    } else if (wrongs.length > 0) {
      onToast(t('admin.analysis.genTasksFailed'));
    }
  };

  // The 034 engine: distill this report into at most a few learning
  // measures (origin=analysis) the parent then manages in the Measures tab.
  const distill = async () => {
    if (distillBusy) return;
    setDistillBusy(true);
    try {
      const res = await api.admin.distillMeasures(analysis.id);
      setDistilled(res.count);
      onToast(t('admin.analysis.distilled', { count: res.count }));
    } catch {
      onToast(t('admin.analysis.distillFailed'));
    }
    setDistillBusy(false);
  };

  // OD pa-export-btn: print/save the finished report as a one-off file —
  // the browser's print dialog handles PDF, the clipboard handles sharing.
  const exportReport = async () => {
    const lines: string[] = [
      t('admin.analysis.exportTitle'),
      `${t(`admin.analysis.subject_${analysis.subject}`, { defaultValue: analysis.subject })} · ${new Date(analysis.created_at).toLocaleString()}`,
      `${t('admin.analysis.totalStat')}: ${analysis.total_questions} · ${t('admin.analysis.wrongStat')}: ${analysis.wrong_count}`,
      '',
    ];
    for (const q of summary?.questions ?? []) {
      lines.push(`#${q.number} ${q.question_text}`);
      lines.push(q.is_wrong
        ? `  ${t('admin.analysis.qVerdictWrong')} · ${q.mistake_analysis ?? ''}`
        : `  ${t('admin.analysis.qRight')}`);
      lines.push('');
    }
    const text = lines.join('\n');
    try {
      await navigator.clipboard.writeText(text);
      onToast(t('admin.analysis.exportCopied'));
    } catch {
      // Clipboard may be denied (non-secure context): fall back to a
      // download so the export always produces something.
      const blob = new Blob([text], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `paper-analysis-${analysis.id}.txt`;
      a.click();
      URL.revokeObjectURL(url);
      onToast(t('admin.analysis.exportDownloaded'));
    }
  };

  if (!summary) return null;
  const typeTotal = Object.values(summary.error_type_counts).reduce((a, b) => a + b, 0);
  // OD pa-report-head: the one-line verdict of the whole paper — how many
  // questions, how many wrong, the subject and date, at a glance.
  const wrongPct = analysis.total_questions > 0
    ? Math.round(analysis.wrong_count / analysis.total_questions * 100)
    : 0;
  const dateLabel = new Date(analysis.created_at).toLocaleDateString();

  return (
    <div className={s.report} data-test="pa-report">
      <p className={s.reportNote}>{t('admin.analysis.reportAiNote')}</p>

      <div className={s.reportHead} data-test="pa-report-head">
        <div className={s.reportHeadBig}>
          {t('admin.analysis.headTotal', { total: analysis.total_questions })}
          {' '}
          <em>{t('admin.analysis.headWrong', { wrong: analysis.wrong_count })}</em>
        </div>
        <p className={s.reportHeadSub}>
          {t(`admin.analysis.subject_${analysis.subject}`, { defaultValue: analysis.subject })} · {dateLabel} · {t('admin.analysis.headWrongRate', { percent: wrongPct })}
        </p>
      </div>

      <div className={s.stats} data-test="pa-report-stats">
        {/* OD pa-report-stats is a four-cell grid: the wrong rate, the
            sample count, the corrected share and the totals — the paper's
            whole shape in one row. */}
        <div className={s.stat}>
          <b>{wrongPct}%</b>
          <span>{t('admin.analysis.statWrongRate')}</span>
        </div>
        <div className={s.stat}>
          <b>{analysis.total_questions}</b>
          <span>{t('admin.analysis.statSample')}</span>
        </div>
        <div className={s.stat}>
          <b>{analysis.wrong_count}</b>
          <span>{t('admin.analysis.wrongStat')}</span>
        </div>
        <div className={s.stat}>
          <b>{analysis.total_questions - analysis.wrong_count}</b>
          <span>{t('admin.analysis.statFixed')}</span>
        </div>
      </div>

      <h4 className={s.blockTitle}>{t('admin.analysis.typeDistTitle')} · {t('admin.analysis.typeDistTotal', { count: typeTotal })}</h4>
      {typeTotal > 0 ? (
        <div className={s.typeDist} data-test="pa-error-dist">
          {Object.entries(summary.error_type_counts).map(([type, count]) => (
            <div key={type} className={s.typeRow}>
              <span className={s.typeLabel}>{errorTypeLabel(t, type)}</span>
              <span className={s.typeBar}>
                <i style={{ width: `${typeTotal ? Math.round(count / typeTotal * 100) : 0}%` }} />
              </span>
              <b>{count}</b>
            </div>
          ))}
        </div>
      ) : (
        <p className={s.inlineEmpty}>{t('admin.analysis.typeDistTotal', { count: 0 })}</p>
      )}

      <h4 className={s.blockTitle}>{t('admin.analysis.weakTitle')}</h4>
      <p className={s.blockHint}>{t('admin.analysis.weakHint')}</p>
      {(summary.weak_tags?.length ?? 0) > 0 ? (
        <ul className={s.weakList} data-test="pa-weak-kc">
          {summary.weak_tags.map(w => (
            <li key={w.tag_id}>
              <span className={s.weakName}>{w.name}</span>
              <span className={s.weakMeta}>{w.subject} · {t('admin.analysis.wrongStat')} ×{w.wrong_count}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className={s.inlineEmpty}>{t('admin.analysis.weakEmpty')}</p>
      )}

      <h4 className={s.blockTitle}>{t('admin.analysis.questionsTitle')}</h4>
      <div className={s.qList}>
        {/* questions is null on pre-037 0-question rows (nil slice → JSON
            null — the crash that made the report unopenable). */}
        {(summary.questions ?? []).map((q, i) => (
          <div
            key={`${q.number}-${i}`}
            className={`${s.qRow} ${q.is_wrong ? s.qWrong : ''}`}
            data-test={`pa-q-${i + 1}`}
          >
            <div className={s.qHead}>
              <span className={s.qNumber}>{t('admin.analysis.qNumber', { number: q.number })}</span>
              <span className={s.qVerdict}>
                {q.is_wrong ? errorTypeLabel(t, q.error_type) : t('admin.analysis.qRight')}
              </span>
            </div>
            <MathText as="p" className={s.qText} text={q.question_text} />
            {q.wrong_answer_text && (
              <p className={s.qWrongAns}>{t('admin.analysis.manualWrong')}: <MathText text={q.wrong_answer_text} /></p>
            )}
            {q.mistake_analysis && (
              <p className={s.qMistake}><MathText text={q.mistake_analysis} /></p>
            )}
            {(q.knowledge_points ?? []).length > 0 && (
              <p className={s.qTags}>{t('admin.analysis.qTags', { tags: (q.knowledge_points ?? []).join(' · ') })}</p>
            )}
            {q.is_wrong && q.error_item_id && (
              <button
                type="button"
                className={taskFor[q.error_item_id] === 'done' ? ui.btnGhost : ui.btnPrimary}
                data-test="pa-gen-task"
                onClick={() => makeTask(q)}
                disabled={!!taskFor[q.error_item_id]}
              >
                {taskFor[q.error_item_id] === 'done'
                  ? <><Icon name="check" /> {t('admin.analysis.qTaskDone')}</>
                  : t('admin.analysis.qMakeTask')}
              </button>
            )}
          </div>
        ))}
      </div>

      {(summary.notes ?? []).length > 0 && (
        <>
          <h4 className={s.blockTitle}>{t('admin.analysis.notesTitle')}</h4>
          <ul className={s.notes}>
            {(summary.notes ?? []).map((n, i) => <li key={i}>{n}</li>)}
          </ul>
        </>
      )}

      {/* The report footer (OD pa-actions / pa-auto-queue / pa-loop-card):
          bulk task creation, the FSRS auto-queue note, one-tap measure
          distillation, and the L1 loop in one line. */}
      <div className={s.reportActions} data-test="pa-actions">
        {wrongs.length > 0 && (
          <button
            type="button"
            className={bulkDone ? ui.btnGhost : ui.btnPrimary}
            data-test="pa-gen-tasks"
            onClick={makeAllTasks}
            disabled={bulkBusy || bulkDone}
          >
            {bulkBusy
              ? t('admin.analysis.starting')
              : bulkDone
                ? <><Icon name="check" /> {t('admin.analysis.genTasksDone', { count: wrongs.length })}</>
                : t('admin.analysis.genTasksAll', { count: wrongs.length })}
          </button>
        )}
        <p className={s.autoQueueNote} data-test="pa-auto-queue">{t('admin.analysis.autoQueueNote')}</p>
        <button
          type="button"
          className={distilled != null ? ui.btnGhost : ui.btnGhost}
          data-test="pa-distill"
          onClick={distill}
          disabled={distillBusy || distilled != null}
        >
          {distilled != null
            ? <><Icon name="check" /> {t('admin.analysis.distilled', { count: distilled })}</>
            : distillBusy
              ? t('admin.analysis.distilling')
              : t('admin.analysis.distillBtn')}
        </button>
        {/* OD pa-export-btn: the report travels — a printable one-off file
            with the diagnosis, saved from the same place it was read. */}
        <button
          type="button"
          className={ui.btnGhost}
          data-test="pa-export-btn"
          onClick={exportReport}
        >
          {t('admin.analysis.exportBtn')}
        </button>
      </div>

      {/* OD pa-source-note: every generated task carries a traceability
          label back to this analysis, visible on the kid's task row and the
          approval card. */}
      <p className={s.sourceNote} data-test="pa-source-note">
        {t('admin.analysis.sourceNote', { source: t('admin.analysis.sourceName') })}
      </p>

      <div className={s.loopCard} data-test="pa-loop-card">
        <h4>{t('admin.analysis.loopTitle')}</h4>
        <p>{t('admin.analysis.loopSteps')}</p>
      </div>

      {/* OD pa-links-card 「接着去哪」: the report is not a dead end — the
          five follow-ups the parent most likely wants next. Hash-links to
          the dashboard tabs (the AdminDashboard hash protocol), the modal
          closes so the destination is visible. */}
      <nav className={s.linksCard} data-test="pa-links-card" aria-label={t('admin.analysis.linksTitle')}>
        <h4>{t('admin.analysis.linksTitle')}</h4>
        <a
          className={s.linkRow}
          data-test="pa-link-errorbook"
          href="#tab=errorbook"
          onClick={onClose}
        >
          📕 {t('admin.analysis.linkErrorbook')} →
        </a>
        <a
          className={s.linkRow}
          data-test="pa-link-review"
          href="#tab=profile"
          onClick={onClose}
        >
          📈 {t('admin.analysis.linkProfile')} →
        </a>
        <a
          className={s.linkRow}
          data-test="pa-link-courses"
          href="#tab=courses"
          onClick={onClose}
        >
          📚 {t('admin.analysis.linkCourses')} →
        </a>
        <a
          className={s.linkRow}
          data-test="pa-link-settings"
          href="#tab=settings"
          onClick={onClose}
        >
          ⚙️ {t('admin.analysis.linkSettings')} →
        </a>
      </nav>
    </div>
  );
};

/** The manual-fix form for one "unknown" item: the AI never guesses. */
const ManualFix: React.FC<{ item: ErrorItem; onSaved: () => void }> = ({ item, onSaved }) => {
  const { t } = useTranslation();
  const [question, setQuestion] = useState(item.question_text ?? '');
  const [answer, setAnswer] = useState(item.answer_text ?? '');
  const [wrong, setWrong] = useState(item.wrong_answer_text ?? '');
  const [mistake, setMistake] = useState(item.mistake_analysis ?? '');
  const [errorType, setErrorType] = useState(item.error_type ?? 'concept');
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');

  const save = async () => {
    setBusy(true);
    setErr('');
    try {
      await api.admin.updateErrorItem(item.id, {
        question_text: question,
        answer_text: answer,
        wrong_answer_text: wrong,
        mistake_analysis: mistake,
        error_type: errorType as ErrorItem['error_type'],
      });
      onSaved();
    } catch {
      setErr(t('admin.analysis.saveFailed'));
    }
    setBusy(false);
  };

  return (
    <div className={s.manualCard} data-test="pa-manual-card">
      <h4>{t('admin.analysis.manualTitle')}</h4>
      <p className={s.manualHint}>{t('admin.analysis.manualHint')}</p>
      {item.original_image_url && !item.original_image_url.startsWith('completion:') && (
        <a className={s.manualImgLink} href={item.original_image_url} target="_blank" rel="noreferrer">
          <img src={item.original_image_url} alt={t('admin.analysis.manualTitle')} loading="lazy" />
        </a>
      )}
      <label>
        <span>{t('admin.analysis.manualQuestion')}</span>
        <textarea value={question} rows={2} onChange={e => setQuestion(e.target.value)} />
      </label>
      <label>
        <span>{t('admin.analysis.manualAnswer')}</span>
        <textarea value={answer} rows={2} onChange={e => setAnswer(e.target.value)} />
      </label>
      <label>
        <span>{t('admin.analysis.manualWrong')}</span>
        <textarea value={wrong} rows={2} onChange={e => setWrong(e.target.value)} />
      </label>
      <label>
        <span>{t('admin.analysis.manualMistake')}</span>
        <textarea value={mistake} rows={2} onChange={e => setMistake(e.target.value)} />
      </label>
      <label>
        <span>{t('admin.analysis.manualErrorType')}</span>
        <select value={errorType} onChange={e => setErrorType(e.target.value as typeof errorType)}>
          <option value="calculation">{t('admin.analysis.errorTypeCalculation')}</option>
          <option value="concept">{t('admin.analysis.errorTypeConcept')}</option>
          <option value="reading">{t('admin.analysis.errorTypeReading')}</option>
          <option value="method">{t('admin.analysis.errorTypeMethod')}</option>
        </select>
      </label>
      {err && <p className={ui.msgError} role="alert">{err}</p>}
      <button type="button" className={ui.btnPrimary} data-test="pa-manual-btn" onClick={save} disabled={busy || !question}>
        <Icon name="check" /> {t('admin.analysis.manualSave')}
      </button>
    </div>
  );
};

interface PaperAnalysisModalProps {
  /** The completion whose image proof gets parsed. */
  completionId: number;
  /** Pre-open with a running analysis (e.g. reopened from the card). */
  initialAnalysis?: PaperAnalysis | null;
  onClose: () => void;
  /** A transient toast on the parent's screen. */
  onToast: (msg: string) => void;
}

/**
 * The full analysis flow in a modal: launch → poll → report. The parent
 * can close at any time; the job keeps running server-side and the card
 * offers to reopen it.
 */
export const PaperAnalysisModal: React.FC<PaperAnalysisModalProps> = ({ completionId, initialAnalysis, onClose, onToast }) => {
  const { t } = useTranslation();
  const [analysis, setAnalysis] = useState<PaperAnalysis | null>(initialAnalysis ?? null);
  // 服务端 subjects 表驱动（GET /api/taxonomy/subjects）：下拉顺序即服务端
  // sort_order 顺序，空串起手不默认 math——launch 前由家长自己选学科。
  // R2：消费 loading/error/retry——加载中/失败时禁用开始并显示可见状态与
  // 重试（与 subjects.length 解耦，stale 快照在场时错误同样可见）；成功
  // 空数组（服务端一个学科都没有）显示真空态且不可空 subject 开始。
  const { subjects, loading: subjectsLoading, error: subjectsError, retry: retrySubjects } = useSubjects();
  const [subject, setSubject] = useState<string>('');
  // The parent's free-text focus for this parse (问题③): rides the start
  // request and lands in both stage prompts server-side.
  const [instructions, setInstructions] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const timerRef = useRef<number | null>(null);

  // Poll a running analysis every 2s; stop on done/failed/unmount.
  useEffect(() => {
    if (!analysis || (analysis.status !== 'pending' && analysis.status !== 'running')) return;
    const id = window.setInterval(async () => {
      try {
        const next = await api.admin.getPaperAnalysis(analysis.id);
        setAnalysis(next);
      } catch { /* transient poll failure: keep the interval */ }
    }, 2000);
    timerRef.current = id;
    return () => { window.clearInterval(id); timerRef.current = null; };
  }, [analysis]);

  const start = useCallback(async () => {
    setBusy(true);
    setError('');
    try {
      const next = await api.admin.startPaperAnalysis(completionId, subject, instructions.trim() || undefined);
      setAnalysis(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  }, [completionId, subject, instructions]);

  // The manual-fix queue: unknown items of this completion's kid.
  const [unknownItems, setUnknownItems] = useState<ErrorItem[] | null>(null);
  useEffect(() => {
    if (!analysis || analysis.status !== 'done') return;
    let live = true;
    api.errorItems.list({ limit: 200 }).then(items => {
      if (live) setUnknownItems(items.filter(it => it.mistake_status === 'unknown'));
    }).catch(() => { if (live) setUnknownItems([]); });
    return () => { live = false; };
  }, [analysis]);

  const reloadUnknown = useCallback(async () => {
    try {
      const items = await api.errorItems.list({ limit: 200 });
      setUnknownItems(items.filter(it => it.mistake_status === 'unknown'));
    } catch { /* keep the old list */ }
  }, []);

  const running = analysis && (analysis.status === 'pending' || analysis.status === 'running');

  return (
    <Modal isOpen onClose={onClose} title={t('admin.analysis.launchTitle')} maxWidth="640px">
      <div data-test="pa-launch">
        {!analysis && (
          <>
            <p className={s.launchHint}>{t('admin.analysis.launchHint')}</p>
            {/* OD pa-upload / pa-upload-note: the launch view tells the
                parent what happens on a failed parse before they commit —
                「解析失败会标待人工，不会丢原图」. */}
            <div className={s.upload} data-test="pa-upload" role="group" aria-label={t('admin.analysis.uploadAria')}>
              <span className={s.uploadIcon} aria-hidden>📄</span>
              <div className={s.uploadBody}>
                <p className={s.uploadTitle}>{t('admin.analysis.uploadTitle')}</p>
                <p className={s.uploadDesc}>{t('admin.analysis.uploadDesc')}</p>
                <p className={s.uploadNote} data-test="pa-upload-note">{t('admin.analysis.uploadNote')}</p>
              </div>
            </div>
            <label className={s.subjectRow}>
              <span>{t('admin.analysis.subjectLabel')}</span>
              <select value={subject} onChange={e => setSubject(e.target.value)} data-test="pa-subject-select">
                {subjects.map(sub => (
                  <option key={sub.id} value={sub.id}>
                    {subjectOptionLabel(t, sub.id, sub.id, sub.name)}
                  </option>
                ))}
              </select>
            </label>
            {/* 问题③: the parent's focus for THIS parse (「重点分析第3题」),
                sent with the start request and injected into both stage
                prompts server-side. */}
            <label className={s.subjectRow}>
              <span>{t('admin.analysis.instructionsLabel')}</span>
              <textarea
                rows={2}
                maxLength={500}
                value={instructions}
                onChange={e => setInstructions(e.target.value)}
                placeholder={t('admin.analysis.instructionsPlaceholder')}
                data-test="pa-instructions"
              />
            </label>
            {/* R2：学科清单加载失败/加载中 → 可见状态 + 禁用开始（不静默
                呈现空清单让家长误以为无学科可选后失败）。成功空数组是真空态。
                词条复用 errorbookTab.loadFailed / kidsStatusTab.refreshLabel /
                common.loading / errorbookTab.empty，不新增 i18n。 */}
            {subjectsError != null && (
              <p className={ui.msgError} role="alert" data-test="pa-subjects-error">
                {t('admin.errorbookTab.loadFailed')}{' '}
                <button type="button" className={ui.btnGhost} onClick={retrySubjects} data-test="pa-subjects-retry">
                  {t('admin.kidsStatusTab.refreshLabel')}
                </button>
              </p>
            )}
            {subjectsError == null && subjectsLoading && subjects.length === 0 && (
              <p className={ui.emptyInline} data-test="pa-subjects-loading">{t('common.loading')}</p>
            )}
            {subjectsError == null && !subjectsLoading && subjects.length === 0 && (
              <p className={ui.emptyInline} data-test="pa-subjects-empty">{t('admin.errorbookTab.empty')}</p>
            )}
            {error && <p className={ui.msgError} role="alert">{error}</p>}
            <button
              type="button"
              className={ui.btnPrimary}
              data-test="pa-launch-btn"
              onClick={start}
              // R2：清单在途/失败（保 stale 快照时也算失败态）禁用开始——
              // 成功空数组无学科可选同样不可空 subject 开始。
              disabled={busy || subjectsLoading || subjectsError != null || subjects.length === 0}
            >
              <Icon name="spark" /> {busy ? t('admin.analysis.starting') : t('admin.analysis.startBtn')}
            </button>
          </>
        )}

        {running && (
          <div className={s.progress} data-test="pa-progress">
            <StepRail analysis={analysis} />
            <p className={s.progressText} data-test="pa-status-text">
              {t('admin.analysis.progressText', { total: analysis.total_questions || 0, current: Math.max(1, Math.round(analysis.progress / 100 * Math.max(analysis.total_questions, 1))) })}
            </p>
            <div className={s.progressBar}>
              <i style={{ width: `${analysis.progress}%` }} />
            </div>
            {/* OD pa-paper-thumb + pa-cut-1/2/3: the page being parsed and
                the three stage cards — what is cut, what is judged, what the
                report will hold. All from the live progress snapshot. */}
            <PaperThumb analysis={analysis} />
            <div className={s.cutRow}>
              <CutMini
                test="pa-cut-1"
                tone={analysis.stage === 'segmenting' || analysis.stage === 'queued' ? 'star' : 'done'}
                title={analysis.stage === 'segmenting' || analysis.stage === 'queued'
                  ? t('admin.analysis.cut1Active')
                  : t('admin.analysis.cut1Title', { count: analysis.total_questions || 0 })}
                sub={t('admin.analysis.cut1Sub')}
              />
              <CutMini
                test="pa-cut-2"
                tone={analysis.stage === 'judging' ? 'star' : analysis.stage === 'done' ? 'done' : 'line'}
                title={analysis.stage === 'judging'
                  ? t('admin.analysis.cut2Title', {
                      judged: Math.max(1, Math.round(analysis.progress / 100 * Math.max(analysis.total_questions, 1))),
                      total: analysis.total_questions || 0,
                    })
                  : analysis.stage === 'done' ? t('admin.analysis.cut2Done') : t('admin.analysis.cut2Sub')}
                sub={analysis.stage === 'judging' || analysis.stage === 'done' ? t('admin.analysis.cut2Sub') : ''}
              />
              <CutMini
                test="pa-cut-3"
                tone={analysis.stage === 'done' ? 'done' : 'line'}
                title={t('admin.analysis.cut3Title')}
                sub={t('admin.analysis.cut3Sub')}
              />
            </div>
          </div>
        )}

        {analysis?.status === 'failed' && (
          <p className={ui.msgError} role="alert">{t('admin.analysis.failed', { error: analysis.error ?? '' })}</p>
        )}

        {analysis?.status === 'done' && (
          <ReportView analysis={analysis} onToast={onToast} onClose={onClose} />
        )}

        {analysis?.status === 'done' && unknownItems && unknownItems.length > 0 && (
          <div className={s.manualQueue}>
            {unknownItems.map(it => (
              <ManualFix key={it.id} item={it} onSaved={() => { reloadUnknown(); onToast(t('admin.analysis.manualSaved')); }} />
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
};
