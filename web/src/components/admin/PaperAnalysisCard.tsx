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
import { MathText } from '../MathText';
import { Icon } from '../../design';
import Modal from '../Modal/Modal';
import ui from './ui.module.css';
import s from './PaperAnalysisCard.module.css';

/** The subjects the analysis can be tagged with. */
const SUBJECTS = ['math', 'chinese', 'english', 'physics', 'chemistry'] as const;

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

/** The diagnosis report: stats, error mix, weak points, per-question rows. */
const ReportView: React.FC<{
  analysis: PaperAnalysis;
  onToast: (msg: string) => void;
}> = ({ analysis, onToast }) => {
  const { t } = useTranslation();
  const summary = analysis.summary;
  const [taskFor, setTaskFor] = useState<Record<number, 'busy' | 'done'>>({});

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

  if (!summary) return null;
  const typeTotal = Object.values(summary.error_type_counts).reduce((a, b) => a + b, 0);

  return (
    <div className={s.report} data-test="pa-report">
      <p className={s.reportNote}>{t('admin.analysis.reportAiNote')}</p>

      <div className={s.stats} data-test="pa-report-stats">
        <div className={s.stat}>
          <b>{analysis.total_questions}</b>
          <span>{t('admin.analysis.totalStat')}</span>
        </div>
        <div className={s.stat}>
          <b>{analysis.wrong_count}</b>
          <span>{t('admin.analysis.wrongStat')}</span>
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
      {summary.weak_tags.length > 0 ? (
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
        {summary.questions.map((q, i) => (
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
  const [subject, setSubject] = useState<string>('math');
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
      const next = await api.admin.startPaperAnalysis(completionId, subject);
      setAnalysis(next);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    setBusy(false);
  }, [completionId, subject]);

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
            <label className={s.subjectRow}>
              <span>{t('admin.analysis.subjectLabel')}</span>
              <select value={subject} onChange={e => setSubject(e.target.value)}>
                {SUBJECTS.map(sub => (
                  <option key={sub} value={sub}>{t(`admin.analysis.subject_${sub}`)}</option>
                ))}
              </select>
            </label>
            {error && <p className={ui.msgError} role="alert">{error}</p>}
            <button type="button" className={ui.btnPrimary} data-test="pa-launch-btn" onClick={start} disabled={busy}>
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
          </div>
        )}

        {analysis?.status === 'failed' && (
          <p className={ui.msgError} role="alert">{t('admin.analysis.failed', { error: analysis.error ?? '' })}</p>
        )}

        {analysis?.status === 'done' && (
          <ReportView analysis={analysis} onToast={onToast} />
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
