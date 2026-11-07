// 题库练习（卡 TX-FE-FIX2）：知识点详情里的题目逐题作答，服务端判分。
// ask 绝不下发答案（服务端剥除）；提交后 practice 响应才带
// reference_answer/explanation（judge/self/skip 三种路径都回参考答案）。
// self 走真实 POST（answer='对'/'不对'，verdict=self），skip 也真实 POST
// （answer='不会'），不再是纯前端本地状态翻转。
//
// 卡 DEF-PRACTICE-RUNNER-R2-CODER：慢网/键盘/乱序响应下的一次性与竞态修复。
// - 鼠标与 Enter 共用同步闸门 busyRoundRef（ref，在 setBusy 状态更新前即
//   拒绝同一回合的重复提交），同一轮同一题只发一次 POST；
// - round 回合令牌：推进到下一题与 roundKey 变化都使其失效（+1），异步
//   await 返回后必须核对 mounted + round，迟到/卸载/换轮响应不写 state、
//   不计数、不上报；
// - verdict 只认服务端明确五值：correct/wrong 走 judged 计数路径；
//   manual/self/skip 都是成功推进信号（self/skip 面板，成功可推进但不
//   判错、不计 fail、不写错误、不进 judged 分母）；其余未知值一律
//   role=alert 且停留本题，不推进不上报；
// - 双 key 隔离（R2）：identityKey = topic.id|userId|resourceId|topicRef，
//   不含 questions（题库临时为空再恢复不丢进度）；变化时在
//   useLayoutEffect 里完整复位 index/answer/picked/phase/error/done/
//   correctCount/judgedRef/busy/report 相关 ref——新孩子看不到旧答案/
//   得分/verdict，旧 phase.correct 不计入新孩子。roundKey = identityKey +
//   题列表，负责在飞请求失效与 busy 渲染态复位；
// - key 比较与 roundRef 递增都在 useLayoutEffect（提交后）执行，render 期
//   零副作用，StrictMode 双渲染安全；
// - onAnswered 回调异常与 fetch 失败隔离：回调抛错不当网络失败，计数/
//   面板/重试三者一致；
// - 答案反馈统一提交后顶层 reference_answer/explanation，绝不从
//   result.question.answer / question.answer / question.explanation 预提交
//   fallback（空 manual 不是获得答案的入口）。

import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { api } from '../api';
import type { QuestionRow, TopicRow } from './types';
import { taxonomyApi } from './client';
import { difficultyLabel, typeLabel } from './labels';
import { EmptyRow, ErrorRow, MathPrompt } from './parts';
import { SafeFigure } from './SafeFigure';
import s from './workspace.module.css';

export interface PracticeRunnerProps {
  topic: TopicRow;
  questions: QuestionRow[] | null;
  /** 作答者（服务端用于掌握度回写）；家长代答时传孩子 id。 */
  userId?: number | null;
  onExit: () => void;
  onAnswered?: (questionId: string, correct: boolean) => void;
  /** 课程上下文：传入时完成练习会向对应课程资源上报得分（judged=0 不上报）。 */
  courseContext?: { resourceId: number; topicRef: string };
}

/** 提交后的反馈面板状态：judge / self / skip 三路共享。
 *  reference/explanation 一律来自提交后服务端响应的顶层字段。 */
type Phase =
  | { kind: 'ask' }
  | { kind: 'judged'; correct: boolean; verdict: string; explanation: string | null; reference: string | null }
  | { kind: 'self'; reference: string | null; explanation: string | null }
  | { kind: 'skip'; reference: string | null; explanation: string | null };

/** 一次练习：一题一题过，choice 点选、numeric/short 输入、self 自评。 */
export const PracticeRunner: React.FC<PracticeRunnerProps> = ({
  topic,
  questions,
  userId,
  onExit,
  onAnswered,
  courseContext,
}) => {
  const list = useMemo(() => questions ?? [], [questions]);
  const [index, setIndex] = useState(0);
  const [answer, setAnswer] = useState('');
  const [picked, setPicked] = useState<string | null>(null);
  const [phase, setPhase] = useState<Phase>({ kind: 'ask' });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [done, setDone] = useState<{ correct: number; total: number } | null>(null);
  const [correctCount, setCorrectCount] = useState(0);
  // 服务端判定的对/错计数（ref 防闭包丢更新；skip/manual/self 不进分母，
  // 也不当错误）。judged = correct + wrong，只算服务端确定性判定。
  const judgedRef = useRef({ correct: 0, wrong: 0 });
  // 课程上报：完成后只自动上报一次（reportedRef 成功后置位）；失败后仅靠
  // 显式重试按钮再调，reportingRef 拦掉并发重复触发（如完成按钮双击）。
  const [reportError, setReportError] = useState<unknown>(null);
  const [reportBusy, setReportBusy] = useState(false);
  const reportedRef = useRef(false);
  const reportingRef = useRef(false);

  // ---- 一次性与竞态闸门（卡 DEF-PRACTICE-RUNNER-R2-CODER）----
  // 与其余 Hooks 一起声明在任何 early return 之前：空题库/非空切换时
  // Hooks 顺序保持不变。
  // 卸载标记：在飞请求的迟到响应不得写 state、不得上报。
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  // 回合令牌：post/advance 捕获发起时的值，响应回来核对 roundRef.current
  // 是否仍等于它——不等即整轮作废（迟到/卸载/换轮不写 state、不计数、不上报）。
  const roundRef = useRef(0);
  // 身份 key（不含 questions）：换 topic/孩子/课程资源即换身份。题库临时为
  // 空（拉取中）再恢复不触发本复位，进度不丢。
  const identityKey = [
    topic.id,
    userId ?? '',
    courseContext?.resourceId ?? '',
    courseContext?.topicRef ?? '',
  ].join('|');
  // 回合 key：身份 + 题列表。身份或题库任一变化都令旧回合在飞请求失效。
  const roundKey = `${identityKey}|${list.map((x) => x.id).join(',')}`;
  const identityKeyRef = useRef(identityKey);
  const roundKeyRef = useRef(roundKey);
  // 身份变化：完整复位所有练习状态与 ref——新孩子看不到旧孩子的
  // answer/picked/verdict/done/得分，旧 phase.correct 不计入新孩子，
  // 课程上报标记（reportedRef）也复位（新资源重新上报）。
  // useLayoutEffect：提交后同步执行，render 期零副作用，StrictMode 双挂载
  // 下第二次 effect 前 key 已相等，只复位一次，复位后的首帧渲染即干净。
  useLayoutEffect(() => {
    if (identityKeyRef.current === identityKey) return;
    identityKeyRef.current = identityKey;
    roundKeyRef.current = roundKey;
    roundRef.current += 1; // 旧身份的在飞请求整体作废
    setIndex(0);
    setAnswer('');
    setPicked(null);
    setPhase({ kind: 'ask' });
    setError(null);
    setDone(null);
    setCorrectCount(0);
    judgedRef.current = { correct: 0, wrong: 0 };
    reportedRef.current = false;
    setReportError(null);
    setReportBusy(false);
    setBusy(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- list 参与 key 拼装，仅 identityKey 变化触发复位
  }, [identityKey]);
  // 回合 key 变化（含仅题库变化，如身份相同的换题库/空题库恢复）：
  // 旧回合在飞请求失效 + busy 渲染态复位。busyRoundRef 的旧值会被新回合
  // 直接覆盖，不会误清新闸门。同一身份下换成更短题库时把越界 index 钳回
  // 合法范围（不残留"第 3 / 2 题"）；空列表不钳（恢复后进度不丢）。
  useLayoutEffect(() => {
    if (roundKeyRef.current === roundKey) return;
    roundKeyRef.current = roundKey;
    roundRef.current += 1;
    if (list.length > 0) setIndex((i) => Math.min(i, list.length - 1));
    setBusy(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- list 参与 key 拼装，仅 roundKey 变化触发
  }, [roundKey]);
  // 同步闸门：鼠标与 Enter 共用。busyRoundRef 在 setBusy 之前同步置位，
  // 同一回合内的第二个提交（React 状态更新尚未 flush）立即被拒，只发
  // 一次 POST。跨回合（值不同）不阻塞——旧回合响应由 round 核对丢弃。
  const busyRoundRef = useRef<number | null>(null);
  // 回合令牌渲染快照：useLayoutEffect 提交 roundRef 后由 setBusy 触发的
  // 重渲染对齐闭包值。但 effect 与下一次渲染之间（同一 commit 内）按钮
  // onClick 闭包仍是旧帧值——所以提交入口一律在调用瞬间读
  // roundRef.current（见 submit/onClick），渲染快照仅供判定渲染态展示。
  const round = roundRef.current;

  if (list.length === 0) {
    return (
      <div className={s.screen} data-test="tx-practice">
        <EmptyRow test="tx-practice-empty">这个知识点还没有练习题</EmptyRow>
        <button type="button" className={s.btnGhost} onClick={onExit}>
          ← 返回
        </button>
      </div>
    );
  }

  const question = list[Math.min(index, list.length - 1)];
  const choices = question.choices ?? [];

  const resetForNext = () => {
    setAnswer('');
    setPicked(null);
    setPhase({ kind: 'ask' });
    setError(null);
  };

  // 课程进度上报：有 courseContext 且 judged>0 才调 reportProgress；
  // 完成时自动上报只一次（reportedRef 成功后置位），失败后仅靠重试按钮
  // 再调，reportingRef 拦掉并发重复触发（如完成按钮双击）。上报响应
  // 迟到且组件已卸载时不得再写 state。
  const reportCourse = async () => {
    if (!courseContext || reportingRef.current) return;
    const { correct, wrong } = judgedRef.current;
    const judged = correct + wrong;
    if (judged <= 0) return;
    const score = Math.round((correct * 100) / judged);
    reportingRef.current = true;
    setReportBusy(true);
    setReportError(null);
    try {
      await api.courses.reportProgress(courseContext.resourceId, {
        score,
        submitted_ref: courseContext.topicRef,
      });
      reportedRef.current = true;
    } catch (err) {
      if (mountedRef.current) setReportError(err);
    } finally {
      reportingRef.current = false;
      if (mountedRef.current) setReportBusy(false);
    }
  };

  const finish = (correctSoFar: number) => {
    setDone({ correct: correctSoFar, total: list.length });
    if (courseContext && !reportedRef.current) {
      void reportCourse();
    }
  };

  // 推进：同一回合只允许一次成功推进。round 核对在写任何状态之前；推进
  // 即令牌失效，同 tick 双击、迟到响应触发的第二次 advance 全部被拒。
  const advance = (currentRound: number, deltaCorrect: number) => {
    if (roundRef.current !== currentRound) return;
    roundRef.current += 1;
    const nextCorrect = correctCount + deltaCorrect;
    setCorrectCount(nextCorrect);
    if (index + 1 >= list.length) {
      finish(nextCorrect);
    } else {
      setIndex(index + 1);
      resetForNext();
    }
  };

  // 统一提交：judge（choice/numeric/short 确定性判）与 self（'对'/'不对'）、
  // skip（'不会'）共用一个 POST；verdict 决定反馈面板形状。
  // 返回服务端 verdict（闸门拒绝/请求失败/迟到作废为 null）。
  // onAnswered 回调异常与 fetch 失败隔离：回调抛错不当网络失败——计数与
  // 面板照常落地，catch 只兜 fetch/判空本身的错，重试不会重复计数。
  const post = async (value: string, currentRound: number): Promise<string | null> => {
    if (busyRoundRef.current === currentRound) return null; // 同步闸门：拒绝重复 POST
    busyRoundRef.current = currentRound;
    setBusy(true);
    setError(null);
    let verdict: string | null = null;
    try {
      const result = await taxonomyApi.practice({
        user_id: userId ?? undefined,
        topic_id: topic.id,
        question_id: question.id,
        answer: value,
      });
      // 迟到/卸载/换轮响应：不写 state、不计数、不上报。
      if (!mountedRef.current || roundRef.current !== currentRound) return null;
      // 答案反馈只认提交后响应的顶层字段：绝无 result.question.answer /
      // question.answer / question.explanation 预提交 fallback。
      const reference = result.reference_answer ?? null;
      const explanation = result.explanation ?? null;
      verdict = result.verdict ?? null;
      if (verdict === 'manual' || verdict === 'self') {
        // self/manual 评估路径：先给参考答案，等孩子点「对/不对」再落库。
        setPhase({ kind: 'self', reference, explanation });
      } else if (verdict === 'correct' || verdict === 'wrong') {
        // 只记服务端确定性判定：correct/wrong 进分母，skip/manual/self 不进。
        const correct = verdict === 'correct';
        if (correct) judgedRef.current.correct += 1;
        else judgedRef.current.wrong += 1;
        setPhase({ kind: 'judged', correct, verdict, explanation, reference });
        // 回调异常单独兜（与 fetch catch 隔离），计数/面板已落地不受影响。
        try {
          onAnswered?.(question.id, correct);
        } catch (cbErr) {
          logCallbackError(cbErr);
        }
      } else if (verdict === 'skip') {
        // 冻结契约：服务端真实 skip verdict 与 manual 同路——成功推进信号，
        // 进 skip 面板（答案/解析来自本次响应顶层字段），不判错、不
        // onAnswered、不进 judged 分母、不写错误。
        setPhase({ kind: 'skip', reference, explanation });
      } else {
        // 未知 verdict：role=alert（ErrorRow 自带）、停留本题可重试，
        // 不得推进、不得上报、不进 judged 分母。
        setError(new Error(`服务端返回未知判定（${verdict ?? '空'}），请重试`));
      }
      return verdict;
    } catch (err) {
      if (mountedRef.current && roundRef.current === currentRound) setError(err);
      return null;
    } finally {
      if (busyRoundRef.current === currentRound) busyRoundRef.current = null;
      if (mountedRef.current && roundRef.current === currentRound) setBusy(false);
    }
  };

  // 鼠标（onClick）与 Enter（onKeyDown）共用入口：先过同步闸门再取值，
  // 状态更新 flush 前的连击在 post 内被 busyRoundRef 拒绝。
  const submit = async (currentRound: number) => {
    if (busyRoundRef.current === currentRound) return;
    const value = question.type === 'choice' ? (picked ?? '') : answer.trim();
    if (!value) return;
    await post(value, currentRound);
  };

  // self 自评落库：真实 POST，answer='对'/'不对'。self 与 manual 都视为
  // 成功推进信号；只有落库成功才上报 onAnswered 并 advance(0)；失败
  // （post 返回 null）或未知 verdict 停留当前面板并保留错误，不推进不上报。
  // 服务端把自评判成 correct/wrong（如题库答案恰为「对」）时 post 已按
  // judged 一次性计数并上报，面板停在 judged 由「下一题」推进，此处不得
  // 重复计（同轮同题只计一次）。回调异常由 post 内部兜，不阻塞推进。
  const submitSelf = async (correct: boolean, currentRound: number) => {
    const verdict = await post(correct ? '对' : '不对', currentRound);
    // post await 返回后窄窗口重核：期间卸载/换轮则不 onAnswered、不推进。
    if (!mountedRef.current || roundRef.current !== currentRound) return;
    if (verdict !== 'self' && verdict !== 'manual') return;
    try {
      onAnswered?.(question.id, correct);
    } catch (cbErr) {
      logCallbackError(cbErr);
    }
    advance(currentRound, 0);
  };

  // skip：真实 POST（answer='不会'），服务端记 manual（或冻结契约的 skip），
  // 不下发答案给判题。提交后按服务端 verdict 分流：choice/numeric/short 题
  // 的「不会」可能被服务端判成 correct/wrong（如选项文字恰好是「不会」）——
  // 此时 post 已按真实判定走了 judged 面板（judged=correct+wrong 照常进
  // 分母），不得覆盖为 skip；只有 manual/skip 才按 skip 路径切换面板
  // （post 对 manual 已置 self 面板，此处切到 skip；skip verdict 由 post
  // 直接置 skip 面板，函数式更新原样返回）。
  const submitSkip = async (currentRound: number) => {
    const verdict = await post('不会', currentRound);
    // post await 返回后窄窗口重核：期间卸载/换轮则不切面板。
    if (!mountedRef.current || roundRef.current !== currentRound) return;
    if (verdict !== 'manual' && verdict !== 'skip') return;
    // manual → skip 面板复用 post 已置的服务端参考答案/解析，不从题目数据补。
    setPhase((prev) =>
      prev.kind === 'self'
        ? { kind: 'skip', reference: prev.reference, explanation: prev.explanation }
        : prev,
    );
  };

  if (done) {
    return (
      <div className={s.screen} data-test="tx-practice">
        <article className={s.detailCard}>
          <h2 className={s.detailTitle}>练习完成</h2>
          <p className={s.subtitle}>
            「{topic.name}」共 {done.total} 题，答对 {done.correct} 题。练习结果已记录到学习画像。
          </p>
          {reportError ? (
            <ErrorRow error={reportError} onRetry={() => void reportCourse()} test="tx-report-error" />
          ) : null}
          {reportBusy ? <p className={s.subtitle}>正在上报课程进度…</p> : null}
          <button type="button" className={s.btnPrimary} onClick={onExit}>
            返回
          </button>
        </article>
      </div>
    );
  }

  return (
    <div className={s.screen} data-test="tx-practice">
      <div className={s.screenHead}>
        <h2 className={s.title}>{topic.name}</h2>
        <p className={s.subtitle}>
          第 {index + 1} / {list.length} 题
        </p>
      </div>
      <div className={s.progressTrack}>
        <div className={s.progressFill} style={{ width: `${(index / list.length) * 100}%` }} />
      </div>
      <article className={s.questionCard}>
        <div className={s.questionMeta}>
          <span className={s.tag}>{difficultyLabel(question.difficulty)}</span>
          <span className={s.tag}>{typeLabel(question.type)}</span>
        </div>
        <MathPrompt text={question.prompt} />
        <SafeFigure svg={question.figure} label="题目配图" />

        {phase.kind === 'ask' && (
          <>
            {question.type === 'choice' ? (
              <div className={s.choices} data-test="tx-choices">
                {choices.map((c) => {
                  const hideText = question.hide_choice_text && c.figure;
                  return (
                    <button
                      key={c.key}
                      type="button"
                      className={`${s.choice} ${picked === c.key ? s.choicePicked : ''}`}
                      data-test={`tx-choice-${c.key}`}
                      aria-pressed={picked === c.key}
                      onClick={() => {
                        setPicked(c.key);
                        setAnswer(c.key);
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
                data-test="tx-answer-input"
                rows={3}
                placeholder="开放题：写下自己的思路，再看参考答案自评"
                value={answer}
                onChange={(e) => setAnswer(e.target.value)}
              />
            ) : (
              <input
                className={s.answerInput}
                data-test="tx-answer-input"
                type="text"
                inputMode={question.type === 'numeric' ? 'decimal' : 'text'}
                autoComplete="off"
                placeholder="输入答案（数字、分数、多个答案用逗号分开）"
                value={answer}
                onChange={(e) => setAnswer(e.target.value)}
                onKeyDown={(e) => {
                  // Enter 与鼠标共用 submit：同步闸门在状态更新前拒绝重复提交。
                  if (e.key === 'Enter' && !e.nativeEvent.isComposing) submit(roundRef.current);
                }}
              />
            )}
            <div className={s.actionRow}>
              {question.type !== 'self' && (
                <button type="button" className={s.btnPrimary} data-test="tx-submit" disabled={busy || !answer.trim()} onClick={() => submit(roundRef.current)}>
                  {busy ? '提交中…' : '提交答案'}
                </button>
              )}
              {question.type === 'self' && (
                <button
                  type="button"
                  className={s.btn}
                  data-test="tx-peek"
                  disabled={busy}
                  onClick={() => {
                    // self 提交：开放题先提交自己的思路（manual verdict），再看参考答案自评。
                    void post(answer.trim() || '不会', roundRef.current);
                  }}
                >
                  {busy ? '提交中…' : '看参考答案并自评'}
                </button>
              )}
              <button type="button" className={s.btnGhost} data-test="tx-skip" disabled={busy} onClick={() => submitSkip(roundRef.current)}>
                {busy ? '提交中…' : '不会'}
              </button>
            </div>
          </>
        )}

        {phase.kind === 'judged' && (
          <>
            <div className={phase.correct ? s.verdictOk : s.verdictNo} data-test="tx-verdict">
              {phase.correct ? '回答正确' : '回答不正确'}
            </div>
            <div className={s.feedback}>
              {phase.reference && (
                <div>
                  <b>参考答案：</b>
                  <MathPrompt text={phase.reference} />
                </div>
              )}
              {phase.explanation && (
                <div>
                  <b>解析：</b>
                  <MathPrompt text={phase.explanation} />
                </div>
              )}
            </div>
            <div className={s.actionRow}>
              <button type="button" className={s.btnPrimary} data-test="tx-next" onClick={() => advance(round, phase.correct ? 1 : 0)}>
                {index + 1 >= list.length ? '完成练习' : '下一题'}
              </button>
            </div>
          </>
        )}

        {phase.kind === 'self' && (
          <>
            <div className={s.notice}>请对照参考答案自己判断（自动判题只是近似，以参考答案为准）。</div>
            <div className={s.feedback}>
              <div>
                <b>参考答案：</b>
                <MathPrompt text={phase.reference ?? '（无参考答案）'} />
              </div>
              {phase.explanation && (
                <div>
                  <b>解析：</b>
                  <MathPrompt text={phase.explanation} />
                </div>
              )}
            </div>
            <div className={s.actionRow}>
              <button type="button" className={s.btnGood} data-test="tx-self-right" disabled={busy} onClick={() => submitSelf(true, roundRef.current)}>
                对（我做对了）
              </button>
              <button type="button" className={s.btnBad} data-test="tx-self-wrong" disabled={busy} onClick={() => submitSelf(false, roundRef.current)}>
                不对
              </button>
            </div>
          </>
        )}

        {phase.kind === 'skip' && (
          <>
            <div className={s.notice}>选择了「不会」。看一下答案和解析再继续：</div>
            <div className={s.feedback}>
              <div>
                <b>参考答案：</b>
                <MathPrompt text={phase.reference ?? '（无参考答案）'} />
              </div>
              {phase.explanation && (
                <div>
                  <b>解析：</b>
                  <MathPrompt text={phase.explanation} />
                </div>
              )}
            </div>
            <div className={s.actionRow}>
              <button type="button" className={s.btnPrimary} data-test="tx-skip-next" onClick={() => advance(round, 0)}>
                {index + 1 >= list.length ? '完成练习' : '下一题'}
              </button>
            </div>
          </>
        )}

        {error ? <ErrorRow error={error} test="tx-practice-error" /> : null}
        <p className={s.reasonLine}>自动判题只是近似的；判错了可以在结果里向家长说明。</p>
      </article>
      <button type="button" className={s.btnGhost} onClick={onExit}>
        退出练习
      </button>
    </div>
  );
};

/** onAnswered 回调异常兜底：只记日志（仓库约定 stdlib log → console），
 *  绝不让消费端异常伪装成网络失败或打断已落地的计数/面板。 */
function logCallbackError(err: unknown): void {
  // eslint-disable-next-line no-console -- 消费端回调异常按仓库约定记日志
  console.error('PracticeRunner onAnswered callback failed:', err);
}
