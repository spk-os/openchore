// 诊断工作区（卡 TX-FE-CODER）：发起 → 答题流 → 结果页。
// 答题流语义与上游 diagnose/app.js 一致（choice 点选、numeric 输入、
// self/peek 自评回退、不会、看参考答案），但会话状态在服务端
//（POST /answers /undo /finish；孩子中途退出可续答）。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  CorrectionVerdict,
  CreateDiagnosticRequest,
  DiagnosticAttempt,
  DiagnosticDetail,
  DiagnosticQuestion,
  DiagnosticSession,
  DiagnosticSummary,
  TopicRow,
} from './types';
import { taxonomyApi } from './client';
import { attemptResult, normalizeDetail, normalizeQuestion } from './normalize';
import {
  difficultyLabel,
  formatDateTime,
  gradeLabel,
  resultHeadline,
  resultLabel,
  sessionModeLabel,
  sessionStatusLabel,
  sortSessions,
  subjectName,
  typeLabel,
} from './labels';
import { ChapterBar, EmptyRow, ErrorRow, LoadingRow, MathPrompt, StatusTag } from './parts';
import { SafeFigure } from './SafeFigure';
import { ShareCard } from './ShareCard';
import s from './workspace.module.css';

export interface DiagnosticWorkspaceProps {
  /** 发起诊断的孩子（服务端 user_id）。家长视角传选中的孩子；孩子视角传自己。 */
  userId?: number | null;
  /** 视角文案：kid 用「挑战」，parent 用「诊断」。 */
  audience: 'kid' | 'parent';
  /** 发起入口预填的目标（从知识点详情「诊断这个知识点」进入）。 */
  presetTargets?: TopicRow[];
  /** 预填学科。 */
  presetSubject?: string;
  /** 历史会话 id（从历史列表恢复续答）。 */
  resumeSessionId?: number | null;
  /** 结果页的动作入口（生成课程 = 家长专属）。 */
  onGenerateCourse?: (sessionId: number) => void;
  /** 「查看学科知识」入口（根因点跳详情）。 */
  onOpenTopic?: (topicRef: string) => void;
}

type Phase =
  | { kind: 'start' }
  | { kind: 'session'; detail: DiagnosticDetail }
  | { kind: 'result'; detail: DiagnosticDetail };

/**
 * 答题记录里孩子提交的原文展示（与后端 decodeAttemptText 同语义）：
 *  - 042 契约 answer_json 存 JSON string（提交 "999" 落库为 "\"999\""），
 *    解出 string 就展示解码后的裸文本（数字/选项答案不带引号）；
 *  - 合法但非 string 的 JSON（数字/对象等）按 JSON.stringify 安全展示，
 *    绝不让 JSON.parse 抛错把结果页炸白屏；
 *  - 旧裸文本行（pre-contract 落库）解析失败时原样展示，不改写孩子的答案。
 */
function attemptAnswerText(a: DiagnosticAttempt): string {
  const raw = a.answer_json ?? a.answer ?? '';
  if (!raw) return '';
  try {
    const decoded = JSON.parse(raw) as unknown;
    if (typeof decoded === 'string') return decoded;
    // 合法非 string JSON：序列化回可读文本（数字 999 → "999"）。
    return JSON.stringify(decoded);
  } catch {
    // 解析失败：旧裸文本（本树早前 build 的落库形状），原样展示。
    return raw;
  }
}

/** 发起面板：目标知识点（preset）+ 参数（题量/确认数）。 */
const StartPanel: React.FC<{
  userId?: number | null;
  audience: 'kid' | 'parent';
  presetTargets: TopicRow[];
  presetSubject: string;
  onStarted: (detail: DiagnosticDetail) => void;
}> = ({ userId, audience, presetTargets, presetSubject, onStarted }) => {
  const [limit, setLimit] = useState('16');
  const [confirm, setConfirm] = useState('3');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const kidTone = audience === 'kid';

  const start = async (mode: 'target' | 'screen') => {
    // 后端 createDiagnostic 要求 targets 或 scope 至少一个；本面板的
    // 筛查入口复用当前目标集合作为范围（引擎 screen 模式 scope = 目标
    // 集合本身，e2e 后端测试同用法），无目标时两个入口都不可用。
    if (presetTargets.length === 0) return;
    setBusy(true);
    setError(null);
    try {
      const body: CreateDiagnosticRequest = {
        user_id: userId ?? undefined,
        mode,
        subject: presetSubject,
        // 后端按 external_ref 解析 targets（TagIDByExternalRef）：
        // 本地整数 id 会 404，一律传 external_ref。
        targets: presetTargets.map((t) => t.external_ref),
        limit: mode === 'screen' ? 40 : parseInt(limit, 10) || 16,
        confirm: parseInt(confirm, 10) || 3,
      };
      onStarted(await taxonomyApi.createDiagnostic(body));
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className={s.screen} data-test="tx-diag-start">
      <article className={s.detailCard}>
        <h3 className={s.sectionTitle}>
          {kidTone ? '挑战目标' : '诊断目标'}（{subjectName(presetSubject)}）
        </h3>
        {presetTargets.length === 0 ? (
          <EmptyRow>请先从学科知识页选择要诊断的知识点</EmptyRow>
        ) : (
          <ul className={s.critList}>
            {presetTargets.map((t) => (
              <li key={t.id}>
                {t.name}
                <span className={s.muted}>（{gradeLabel(t.grade, presetSubject)}）</span>
              </li>
            ))}
          </ul>
        )}
        <div className={s.filters}>
          <label className={s.muted} htmlFor="tx-limit">
            题量上限（8–30）
          </label>
          <input
            id="tx-limit"
            className={s.filterInput}
            type="number"
            min={8}
            max={30}
            value={limit}
            onChange={(e) => setLimit(e.target.value)}
            style={{ flex: '0 0 100px' }}
          />
          <label className={s.muted} htmlFor="tx-confirm">
            每点确认题数
          </label>
          <select
            id="tx-confirm"
            className={s.filterSelect}
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
          >
            <option value="2">2</option>
            <option value="3">3</option>
            <option value="5">5</option>
          </select>
        </div>
        <p className={s.muted}>连续答对这么多题才判掌握；中途答错会再出一道确认题。</p>
        <div className={s.actionRow}>
          <button
            type="button"
            className={s.btnPrimary}
            data-test="tx-diag-begin"
            disabled={busy || presetTargets.length === 0}
            onClick={() => start('target')}
          >
            {busy ? '创建中…' : kidTone ? '开始挑战' : '开始诊断'}
          </button>
          <button
            type="button"
            className={s.btn}
            data-test="tx-diag-screen"
            disabled={busy || presetTargets.length === 0}
            title={presetTargets.length === 0 ? '先从学科知识页选择筛查范围' : undefined}
            onClick={() => start('screen')}
          >
            {kidTone ? '整组挑战（40 题）' : '整体筛查（40 题）'}
          </button>
        </div>
        {error ? <ErrorRow error={error} /> : null}
      </article>
    </div>
  );
};

/** 答题流：一题一提交（服务端推进状态机）。 */
const QuestionPanel: React.FC<{
  sessionId: number;
  question: DiagnosticQuestion;
  index: number;
  limit: number;
  audience: 'kid' | 'parent';
  onAnswered: (detail: DiagnosticDetail) => void;
  onUndo: () => void;
  onFinish: () => void;
  onExit: () => void;
  canUndo: boolean;
}> = ({ sessionId, question, index, limit, audience, onAnswered, onUndo, onFinish, onExit, canUndo }) => {
  const [answer, setAnswer] = useState('');
  const [picked, setPicked] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const submittedRef = useRef(false);

  // 题目变化时清空输入（同题重试除外）。
  useEffect(() => {
    setAnswer('');
    setPicked(null);
    submittedRef.current = false;
  }, [question.id]);

  const submit = async (value: string) => {
    if (busy || submittedRef.current || !value.trim()) return;
    submittedRef.current = true;
    setBusy(true);
    setError(null);
    try {
      const detail = await taxonomyApi.answerDiagnostic(sessionId, {
        question_id: question.id,
        answer: value,
      });
      onAnswered(detail);
    } catch (err) {
      submittedRef.current = false;
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const choices = question.choices ?? [];

  return (
    <article className={s.questionCard} data-test="tx-diag-question">
      <div className={s.questionMeta}>
        <span>
          第 {index} 题 / 上限 {limit}
        </span>
        {question.topic_name && <span className={s.tag}>{question.topic_name}</span>}
        <span className={s.tag}>{difficultyLabel(question.difficulty)}</span>
        <span className={s.tag}>{typeLabel(question.type)}</span>
      </div>
      <div className={s.progressTrack}>
        <div className={s.progressFill} style={{ width: `${Math.min((index / Math.max(limit, 1)) * 100, 100)}%` }} />
      </div>
      {question.reason && <p className={s.reasonLine}>{question.reason}</p>}
      <MathPrompt text={question.prompt} />
      <SafeFigure svg={question.figure} label="题目配图" />
      {question.type === 'choice' ? (
        <div className={s.choices}>
          {choices.map((c) => {
            const hideText = question.hide_choice_text && c.figure;
            return (
              <button
                key={c.key}
                type="button"
                className={`${s.choice} ${picked === c.key ? s.choicePicked : ''}`}
                data-test={`tx-dchoice-${c.key}`}
                aria-pressed={picked === c.key}
                disabled={busy}
                onClick={() => {
                  setPicked(c.key);
                  setAnswer(c.key);
                  submit(c.key);
                }}
              >
                <span className={s.choiceKey}>{hideText ? c.key : `${c.key}.`}</span>
                {!hideText && <span>{c.text}</span>}
                <SafeFigure svg={c.figure} label={`选项 ${c.key} 配图`} />
              </button>
            );
          })}
        </div>
      ) : question.type === 'self' ? (
        <textarea
          className={s.answerTextarea}
          data-test="tx-danswer-input"
          rows={3}
          placeholder="开放题：写下自己的思路，再点「看参考答案并自评」"
          value={answer}
          disabled={busy}
          onChange={(e) => setAnswer(e.target.value)}
        />
      ) : (
        <input
          className={s.answerInput}
          data-test="tx-danswer-input"
          type="text"
          inputMode={question.type === 'numeric' ? 'decimal' : 'text'}
          autoComplete="off"
          placeholder="输入答案（数字、分数、多个答案用逗号或“和”分开）"
          value={answer}
          disabled={busy}
          onChange={(e) => setAnswer(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.nativeEvent.isComposing) submit(answer);
          }}
        />
      )}
      <div className={s.actionRow}>
        {question.type !== 'choice' && question.type !== 'self' && (
          <button type="button" className={s.btnPrimary} data-test="tx-dsubmit" disabled={busy || !answer.trim()} onClick={() => submit(answer)}>
            {busy ? '提交中…' : '提交答案'}
          </button>
        )}
        <button type="button" className={s.btnGhost} data-test="tx-dskip" disabled={busy} onClick={() => submit('不会')}>
          不会
        </button>
        {question.type === 'self' && (
          <button type="button" className={s.btn} data-test="tx-dpeek" disabled={busy} onClick={() => submit('不会')}>
            看参考答案并自评
          </button>
        )}
        {canUndo && (
          <button type="button" className={s.btnSmall} data-test="tx-dundo" disabled={busy} onClick={onUndo}>
            撤销上一题
          </button>
        )}
        <button type="button" className={s.btnSmall} data-test="tx-diag-finish-btn" disabled={busy} onClick={onFinish}>
          {audience === 'kid' ? '提前收尾，看我的结果' : '提前结束看结果'}
        </button>
        <button type="button" className={s.btnSmall} disabled={busy} onClick={onExit}>
          {audience === 'kid' ? '暂时退出（下次继续）' : '退出'}
        </button>
      </div>
      {error ? <ErrorRow error={error} test="tx-diag-error" /> : null}
      <p className={s.muted}>自动判题只是近似的，一切以参考答案为准；答错了可以撤销或让家长更正。</p>
    </article>
  );
};

/** 结果页：headline + stats + 根因 + 学习路径 + 按章汇总 + 答题记录。 */
export const ResultView: React.FC<{
  detail: DiagnosticDetail;
  audience: 'kid' | 'parent';
  onRetest?: (detail: DiagnosticDetail) => void;
  onGenerateCourse?: (sessionId: number) => void;
  onShare?: (sessionId: number) => void;
  onCorrect?: (sessionId: number, attemptId: number, verdict: CorrectionVerdict) => void;
  onUndo?: (sessionId: number) => void;
  onOpenTopic?: (topicRef: string) => void;
}> = ({ detail, audience, onRetest, onGenerateCourse, onShare, onCorrect, onUndo, onOpenTopic }) => {
  const session = detail.session;
  const summary: DiagnosticSummary = detail.summary ?? {};
  const attempts = detail.attempts ?? [];
  const roots = summary.roots ?? [];
  const path = summary.path ?? [];
  const chapters = summary.chapters ?? [];
  const weak = summary.weak ?? [];
  const shaky = summary.shaky ?? [];
  const mastered = summary.mastered ?? [];
  const untested = summary.untested ?? [];
  const headline = useMemo(() => resultHeadline(summary), [summary]);
  const limitReached = summary.limit_reached ?? false;

  if (!session) return <EmptyRow>会话数据缺失</EmptyRow>;

  return (
    <div className={s.screen} data-test="tx-diag-result">
      <article className={s.detailCard}>
        <h3 className={s.sectionTitle}>
          {audience === 'kid' ? '挑战结果' : '诊断结果'}
          {session.label ? `：${session.label}` : ''}
        </h3>
        <p>{headline}</p>
        <div className={s.statsRow}>
          <div className={s.stat} data-test="tx-stat-asked">
            <b>{summary.asked ?? attempts.length}</b>
            <span>已答题数</span>
          </div>
          <div className={`${s.stat} ${s.statWeak}`}>
            <b>{weak.length}</b>
            <span>薄弱</span>
          </div>
          <div className={`${s.stat} ${s.statShaky}`}>
            <b>{shaky.length}</b>
            <span>不稳</span>
          </div>
          <div className={`${s.stat} ${s.statOk}`}>
            <b>{mastered.length}</b>
            <span>已掌握</span>
          </div>
          <div className={s.stat}>
            <b>{untested.length}</b>
            <span>未测</span>
          </div>
        </div>
        {(summary.unconfirmed ?? []).length > 0 && (
          <div className={s.notice}>
            有 {(summary.unconfirmed ?? []).length} 个知识点答错过但没来得及复测，暂按薄弱处理。
          </div>
        )}
        {limitReached && (
          <div className={s.notice}>
            已达到题量上限（{summary.limit ?? session.limit ?? '—'}），剩余没测到的知识点标为「未测」。
          </div>
        )}
        <div className={s.actionRow}>
          {onRetest && (weak.length > 0 || shaky.length > 0) && (
            <button type="button" className={s.btnPrimary} data-test="tx-result-retest" onClick={() => onRetest(detail)}>
              {audience === 'kid' ? '再挑战薄弱点' : '盲区再测'}
            </button>
          )}
          {onGenerateCourse && path.length > 0 && (
            <button type="button" className={s.btn} data-test="tx-result-course" onClick={() => onGenerateCourse(session.id)}>
              按学习路径生成课程
            </button>
          )}
          {onShare && (
            <button type="button" className={s.btn} data-test="tx-result-share" onClick={() => onShare(session.id)}>
              生成分享链接
            </button>
          )}
          {onUndo && attempts.length > 0 && (
            <button type="button" className={s.btnSmall} data-test="tx-result-undo" onClick={() => onUndo(session.id)}>
              撤销最后一题
            </button>
          )}
        </div>
      </article>

      {chapters.length > 0 && (
        <article className={s.detailCard} data-test="tx-chapters">
          <h3 className={s.sectionTitle}>按章汇总</h3>
          {chapters.map((c) => (
            <div key={c.key ?? c.label} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 0' }}>
              <span className={s.topicName}>{c.label}</span>
              <ChapterBar row={c} />
              <span className={s.muted}>
                掌握 {c.mastered} · 不稳 {c.shaky} · 薄弱 {c.weak} · 未测 {c.untested}
              </span>
            </div>
          ))}
        </article>
      )}

      <article className={s.detailCard} data-test="tx-roots">
        <h3 className={s.sectionTitle}>薄弱的先修知识点（根因，{roots.length}）</h3>
        <p className={s.muted}>根因 = 被判薄弱、且它的直接先修里没有薄弱点的知识点。</p>
        {roots.length === 0 ? (
          <p>没有发现根因。</p>
        ) : (
          roots.map((r) => (
            <div key={r.id} style={{ padding: '6px 0', borderBottom: '1px solid var(--hairline, #e5e7eb)' }}>
              <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                <StatusTag status="weak" />
                {onOpenTopic ? (
                  <button type="button" className={s.relRow} onClick={() => onOpenTopic(r.id)}>
                    {r.name ?? r.id}
                  </button>
                ) : (
                  <span>{r.name ?? r.id}</span>
                )}
              </div>
              {(r.pending_prereqs ?? []).length > 0 && (
                <p className={s.muted}>它的先修「{(r.pending_prereqs ?? []).join('、')}」没来得及测，真正的根因可能更靠前。</p>
              )}
              {(r.out_of_scope_prereqs ?? []).length > 0 && (
                <p className={s.muted}>前置知识「{(r.out_of_scope_prereqs ?? []).join('、')}」不在所选范围内，没有检查。</p>
              )}
              {(r.external_prereqs ?? []).length > 0 && (
                <p className={s.muted}>外部先修（无诊断题，未测）：{(r.external_prereqs ?? []).join('、')}</p>
              )}
            </div>
          ))
        )}
      </article>

      {shaky.length > 0 && (
        <article className={s.detailCard} data-test="tx-shaky">
          <h3 className={s.sectionTitle}>不稳的知识点（{shaky.length}）</h3>
          <p className={s.muted}>中途答错过，但确认题答对了：可能是粗心，也可能掌握得不牢，建议巩固。</p>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {shaky.map((id) => (
              <StatusTag key={id} status="shaky" />
            ))}
          </div>
        </article>
      )}

      <article className={s.detailCard} data-test="tx-path">
        <h3 className={s.sectionTitle}>推荐学习路径（{path.length}）</h3>
        <p className={s.muted}>从根因（和「不稳」点）出发，按年级从低到高排列；从上往下学。</p>
        {path.length === 0 ? (
          <p>不需要补课。</p>
        ) : (
          <ol className={s.pathList}>
            {path.map((p, i) => (
              <li key={`${p.id}-${i}`} style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
                <span className={s.muted}>{i + 1}.</span>
                {onOpenTopic ? (
                  <button type="button" className={s.relRow} onClick={() => onOpenTopic(p.id)}>
                    {p.name ?? p.id}
                  </button>
                ) : (
                  <span>{p.name ?? p.id}</span>
                )}
                {p.is_root && <span className={`${s.tag} ${s.tagRoot}`}>根因</span>}
                {p.is_target && <span className={`${s.tag} ${s.tagTarget}`}>目标</span>}
                <StatusTag status={p.status} />
              </li>
            ))}
          </ol>
        )}
      </article>

      {attempts.length > 0 && audience === 'parent' && (
        <article className={s.detailCard} data-test="tx-log">
          <h3 className={s.sectionTitle}>答题记录（可更正）</h3>
          <p className={s.muted}>自动判题可能判错。在这里改某一题的结果，诊断结论会重新计算。</p>
          <table className={s.logTable}>
            <thead>
              <tr>
                <th>#</th>
                <th>知识点 / 题目</th>
                <th>孩子的答案</th>
                <th>参考答案</th>
                <th>状态</th>
                <th>结果</th>
              </tr>
            </thead>
            <tbody>
              {attempts.map((a, i) => (
                <tr key={a.id} data-test={`tx-log-row-${i}`}>
                  <td>{(a.seq ?? i + 1)}</td>
                  <td>
                    {a.topic_id ?? '—'}
                    {a.question_id && <div className={s.muted}>{a.question_id}</div>}
                    {a.role && <div className={s.muted}>{a.role === 'confirm' ? '确认题' : '探查题'}</div>}
                  </td>
                  <td>{attemptAnswerText(a)}</td>
                  <td>
                    {/* 参考答案只读提交反馈真实下发的字段（attempt.reference_answer）；
                        服务端 attempts 行不带参考答案（题库答案不随记录回传），没数据就显示「未提供」。
                        绝不从孩子提交前的 question.answer/accept/explanation 泄露答案。 */}
                    {a.reference_answer ? <MathPrompt text={a.reference_answer} /> : '未提供'}
                  </td>
                  <td>{a.status && a.status !== 'answered' ? <span className={s.muted}>{a.status === 'voided' ? '已作废' : '已更正'}</span> : null}</td>
                  <td>
                    {onCorrect ? (
                      <select
                        data-test={`tx-log-result-${i}`}
                        value={attemptResult(a)}
                        onChange={(e) => onCorrect(detail.session?.id ?? 0, a.id, e.target.value as CorrectionVerdict)}
                      >
                        <option value="correct">对</option>
                        <option value="wrong">错</option>
                        <option value="skip">不会</option>
                      </select>
                    ) : (
                      <span>{resultLabel(attemptResult(a))}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </article>
      )}
      <p className={s.muted}>生成时间：{formatDateTime(new Date().toISOString())}</p>
    </div>
  );
};

/**
 * 诊断工作区（双端复用）。孩子端 audience='kid'（文案「挑战检测」）；
 * 家长端 audience='parent'（诊断管理 + 更正 + 生成课程 + 分享）。
 * resumeSessionId 传入时直接拉详情续答（服务端 state_json 恢复）。
 */
export const DiagnosticWorkspace: React.FC<DiagnosticWorkspaceProps> = ({
  userId,
  audience,
  presetTargets = [],
  presetSubject = '',
  resumeSessionId = null,
  onGenerateCourse,
  onOpenTopic,
}) => {
  const [phase, setPhase] = useState<Phase>({ kind: 'start' });
  const [resumeError, setResumeError] = useState<unknown>(null);
  const [shareResult, setShareResult] = useState<import('./types').ShareResult | null>(null);

  const loadDetail = useCallback(async (sessionId: number): Promise<DiagnosticDetail | null> => {
    try {
      return normalizeDetail(await taxonomyApi.diagnosticDetail(sessionId));
    } catch (err) {
      setResumeError(err);
      return null;
    }
  }, []);

  // 恢复续答。
  useEffect(() => {
    if (resumeSessionId == null) return;
    let cancelled = false;
    loadDetail(resumeSessionId).then((detail) => {
      if (cancelled || !detail) return;
      const st = detail.session?.status;
      if (st === 'completed' || (detail.question == null && detail.summary)) {
        setPhase({ kind: 'result', detail });
      } else if (st === 'active' && detail.question) {
        setPhase({ kind: 'session', detail });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [resumeSessionId, loadDetail]);

  const applyDetail = (detail: DiagnosticDetail) => {
    const norm = normalizeDetail(detail);
    if (norm.question) setPhase({ kind: 'session', detail: norm });
    else setPhase({ kind: 'result', detail: norm });
  };

  const undo = async (sessionId: number) => {
    try {
      applyDetail(await taxonomyApi.undoDiagnostic(sessionId));
    } catch (err) {
      setResumeError(err);
    }
  };

  const correct = async (sessionId: number, attemptId: number, verdict: CorrectionVerdict) => {
    try {
      // 更正只上送独立 verdict（对/错/不会）；answer 不再发送，服务端保留原答案。
      applyDetail(await taxonomyApi.correctDiagnostic(sessionId, { attempt_id: attemptId, verdict }));
    } catch (err) {
      setResumeError(err);
    }
  };

  const finish = async (sessionId: number) => {
    try {
      applyDetail(await taxonomyApi.finishDiagnostic(sessionId));
    } catch (err) {
      setResumeError(err);
    }
  };

  const retest = async (detail: DiagnosticDetail) => {
    const sid = detail.session?.id;
    if (sid == null) return;
    try {
      // retest 创建新会话（服务端 detail 形状即新会话）。
      applyDetail(await taxonomyApi.retestDiagnostic(sid));
    } catch (err) {
      setResumeError(err);
    }
  };

  const share = async (sessionId: number) => {
    try {
      // POST /share → {url, expires_at}；卡片展示路由页 + 本地二维码。
      const result = await taxonomyApi.shareDiagnostic(sessionId);
      setShareResult(result);
    } catch (err) {
      setResumeError(err);
    }
  };

  if (phase.kind === 'start') {
    return (
      <div className={s.screen}>
        {resumeError ? <ErrorRow error={resumeError} onRetry={() => setResumeError(null)} /> : null}
        <StartPanel
          userId={userId}
          audience={audience}
          presetTargets={presetTargets}
          presetSubject={presetSubject}
          onStarted={applyDetail}
        />
      </div>
    );
  }

  const { detail } = phase;
  const session = detail.session;
  if (!session) return <EmptyRow>会话数据缺失</EmptyRow>;

  if (phase.kind === 'session' && detail.question) {
    const q = normalizeQuestion(detail.question);
    if (q) {
      const attempts = detail.attempts ?? [];
      return (
        <div className={s.screen} data-test="tx-diag-session">
          <div className={s.screenHead}>
            <h2 className={s.title}>{audience === 'kid' ? '挑战进行中' : '诊断进行中'}</h2>
            <p className={s.subtitle}>
              {session.label ?? session.subject} · {sessionModeLabel(session.mode)} · {sessionStatusLabel(session.status)}
            </p>
          </div>
          <QuestionPanel
            sessionId={session.id}
            question={q}
            index={attempts.length + 1}
            limit={session.limit ?? 16}
            audience={audience}
            canUndo={attempts.length > 0}
            onAnswered={applyDetail}
            onUndo={() => undo(session.id)}
            onFinish={() => finish(session.id)}
            onExit={() => setPhase({ kind: 'start' })}
          />
          {attempts.length >= (session.limit ?? 16) && (
            <button type="button" className={s.btnPrimary} data-test="tx-diag-finish" onClick={() => finish(session.id)}>
              结束并查看结果
            </button>
          )}
          {resumeError ? <ErrorRow error={resumeError} /> : null}
        </div>
      );
    }
  }

  return (
    <div className={s.screen}>
      <ResultView
        detail={detail}
        audience={audience}
        onRetest={retest}
        onGenerateCourse={onGenerateCourse}
        onShare={audience === 'parent' ? share : undefined}
        onCorrect={audience === 'parent' ? correct : undefined}
        onUndo={undo}
        onOpenTopic={onOpenTopic}
      />
      {shareResult ? (
        <ShareCard result={shareResult} onClose={() => setShareResult(null)} />
      ) : null}
      {resumeError ? <ErrorRow error={resumeError} /> : null}
    </div>
  );
};

/** 诊断历史列表（孩子端「历史」/家长端「诊断记录」共用）。 */
export const DiagnosticHistory: React.FC<{
  userId?: number | null;
  onOpen: (sessionId: number) => void;
}> = ({ userId, onOpen }) => {
  // 列表行是 detail.session 壳（可能为 null），渲染前先剥掉 null 再排序。
  const [sessions, setSessions] = useState<NonNullable<DiagnosticDetail['session']>[] | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    let cancelled = false;
    taxonomyApi
      .diagnostics(userId ?? undefined)
      .then((page) => {
        if (!cancelled) setSessions(sortSessions((page.sessions ?? []).filter((sess): sess is DiagnosticSession => !!sess)));
      })
      .catch((err) => {
        if (!cancelled) setError(err);
      });
    return () => {
      cancelled = true;
    };
  }, [userId]);

  if (error) return <ErrorRow error={error} test="tx-history-error" />;
  if (!sessions) return <LoadingRow />;
  if (sessions.length === 0) return <EmptyRow test="tx-history-empty">还没有诊断记录。做完一次诊断，结果会汇总在这里。</EmptyRow>;

  return (
    <div className={s.screen} data-test="tx-history">
      {sessions.map((item) => (
        <button
          key={item.id}
          type="button"
          className={s.detailCard}
          data-test={`tx-history-${item.id}`}
          style={{ textAlign: 'left', cursor: 'pointer' }}
          onClick={() => onOpen(item.id)}
        >
          <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
            <span className={s.tag}>{subjectName(item.subject)}</span>
            <span className={s.tag}>{sessionModeLabel(item.mode)}</span>
            <span className={`${s.tag} ${item.status === 'voided' ? s.tagUnknown : ''}`}>
              {sessionStatusLabel(item.status)}
            </span>
          </div>
          <div className={s.topicName}>{item.label ?? `会话 #${item.id}`}</div>
          <div className={s.muted}>
            已答 {item.asked ?? '—'} 题 · 薄弱 {item.weak_count ?? 0} · 不稳 {item.shaky_count ?? 0}
            {item.completed_at ? ` · ${formatDateTime(item.completed_at)}` : ''}
          </div>
        </button>
      ))}
    </div>
  );
};
