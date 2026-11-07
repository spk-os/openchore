// @vitest-environment jsdom
// PracticeRunner 课程进展上报（卡 TX-PRACTICE-SCORE-V4）关键行为：
// 1) 有 courseContext 时完成练习只按服务端确定性判定（correct/wrong）算分，
//    reportProgress 恰一次且 resourceId/submitted_ref 正确；无 courseContext 零调用。
// 2) 全 skip/manual/self 时 judged=0，不上报。
// 3) 上报失败显示错误且不自动重试；点重试成功后恰再一次；双击重试不并发重复。
// 卡 DEF-PRACTICE-REATTEMPT-COUNT-V2-TESTER：judged 后无重新作答入口、同轮同题
// 只计一次；self POST 失败不 onAnswered/不推进且可重试；manual/self 成功只推进
// 一次且不进 judged 分母；correct/wrong 意外返回不重复计数。
// 卡 DEF-PRACTICE-RUNNER-R2-CODER：慢网/键盘/乱序响应下的一次性与竞态——
// 慢网络连续 Enter/点击只 1 请求；题库切换/卸载后迟到响应不写 state 不上报；
// 未知 verdict role=alert 停留本题；课程上报恰一次；提交前无答案泄露。
// api 客户端模块级 mock，组件只通过 taxonomyApi / api.courses 说话。

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';

const { taxonomyApi, reportProgress } = vi.hoisted(() => ({
  taxonomyApi: { practice: vi.fn() },
  reportProgress: vi.fn(),
}));
vi.mock('./client', () => ({ taxonomyApi, TaxonomyAPIError: class extends Error {} }));
vi.mock('../api', () => ({ api: { courses: { reportProgress } } }));

import { PracticeRunner } from './PracticeRunner';
import type { QuestionRow, TopicRow } from './types';
import './testUtils'; // data-test 作为 testId 查询属性（仓库约定）

const topic: TopicRow = {
  id: '1',
  external_ref: 'cnm_p001',
  name: '1到5各数的认识',
  grade: 1,
  semester: '上',
  domain: '数与代数',
  chapter: '一年级上册 第1单元',
};

const q = (id: string, type: QuestionRow['type'] = 'choice', over: Partial<QuestionRow> = {}): QuestionRow => ({
  id,
  difficulty: 'basic',
  type,
  prompt: `题 ${id}`,
  choices: type === 'choice' ? [{ key: 'A', text: '1' }, { key: 'B', text: '2' }] : null,
  ...over,
});

const courseContext = { resourceId: 42, topicRef: 'cnm_p001' };

type PracticeReply = { verdict: string; reference_answer?: string; explanation?: string | null };

/** 手动控制响应时机的 practice deferred（不依赖定时器）。 */
function deferredReply() {
  let resolve!: (v: PracticeReply) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<PracticeReply>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/** 每次 practice 调用按本题 answer 给出 verdict：'不会'→manual，'对'→self，其余用预设。 */
function mockVerdicts(byAnswer: Record<string, string>) {
  taxonomyApi.practice.mockImplementation(async (req: { answer: string }) => {
    const verdict = byAnswer[req.answer] ?? (req.answer === '对' ? 'self' : req.answer === '不会' ? 'manual' : 'correct');
    return { verdict, reference_answer: '参考答案', explanation: null };
  });
}

/** choice 题：第 n 题点对应选项再提交（A→correct，B→wrong），进入 judged 面板后下一题。 */
function choiceKeyFor(verdict: string) {
  return verdict === 'wrong' ? 'B' : 'A';
}

const renderRunner = (questions: QuestionRow[], ctx?: typeof courseContext) =>
  render(<PracticeRunner topic={topic} questions={questions} userId={2} onExit={() => {}} courseContext={ctx} />);

/** judged 分母探测：reportProgress 未被调用即视为 0（judged<=0 不上报）。 */
const judgedZero = () => reportProgress.mock.calls.length === 0;

/** 注入 onAnswered spy：观察每题是否恰上报一次（同轮同题只计一次）。 */
const renderWithAnswered = (
  questions: QuestionRow[],
  onAnswered: (questionId: string, correct: boolean) => void,
  ctx?: typeof courseContext,
) =>
  render(
    <PracticeRunner
      topic={topic}
      questions={questions}
      userId={2}
      onExit={() => {}}
      onAnswered={onAnswered}
      courseContext={ctx}
    />,
  );

/** choice 题：点选项提交，进入 judged 面板后下一题。 */
async function answerChoice(key = 'A') {
  fireEvent.click(screen.getByTestId(`tx-choice-${key}`));
  fireEvent.click(screen.getByTestId('tx-submit'));
  await screen.findByTestId('tx-verdict');
  fireEvent.click(screen.getByTestId('tx-next'));
}

beforeEach(() => {
  vi.clearAllMocks();
  reportProgress.mockResolvedValue({});
});

afterEach(() => cleanup());

describe('完成练习的课程上报', () => {
  it('correct+wrong+skip：score 只按 correct/wrong，reportProgress 恰一次且参数正确', async () => {
    mockVerdicts({ A: 'correct', B: 'wrong' });
    renderRunner([q('1'), q('2'), q('3')], courseContext);

    await answerChoice(choiceKeyFor('correct'));
    await answerChoice(choiceKeyFor('wrong'));
    // 第 3 题选「不会」→ manual，进程为 skip 面板，点完成
    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    fireEvent.click(screen.getByTestId('tx-skip-next'));

    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
    // 等一拍，确认没有延迟的第二次自动上报
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).toHaveBeenCalledTimes(1);
  });

  it('无 courseContext 时零调用', async () => {
    mockVerdicts({});
    renderRunner([q('1')]);
    await answerChoice();
    await screen.findByText('练习完成');
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).not.toHaveBeenCalled();
  });

  it('全 skip/manual/self：judged=0，零 reportProgress', async () => {
    mockVerdicts({});
    renderRunner([q('1', 'self'), q('2', 'self'), q('3')], courseContext);

    // 两道 self：看参考答案 → 自评「对」→ 自动进入下一题
    fireEvent.click(screen.getByTestId('tx-peek'));
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right'));
    // 第二次 submitSelf 的 post 返回 self，需等下一题渲染
    await screen.findByText('第 2 / 3 题');
    fireEvent.click(screen.getByTestId('tx-peek'));
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right'));

    await screen.findByText('第 3 / 3 题');
    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    fireEvent.click(screen.getByTestId('tx-skip-next'));

    await screen.findByText('练习完成');
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).not.toHaveBeenCalled();
  });
});

describe('上报失败与重试', () => {
  it('reject 显示非空错误且不自动重复；点重试成功后恰再一次', async () => {
    mockVerdicts({});
    reportProgress.mockRejectedValueOnce(new Error('网络中断')).mockResolvedValueOnce({});
    renderRunner([q('1')], courseContext);

    await answerChoice();
    const err = await screen.findByTestId('tx-report-error');
    expect(err.textContent?.trim()).not.toBe('');
    expect(err.textContent).toContain('网络中断');
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId('tx-retry'));
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryByTestId('tx-report-error')).toBeNull());
    expect(reportProgress).toHaveBeenNthCalledWith(2, 42, { score: 100, submitted_ref: 'cnm_p001' });
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).toHaveBeenCalledTimes(2);
  });

  it('连续两次点重试不并发重复上报', async () => {
    mockVerdicts({});
    let resolve!: (v: unknown) => void;
    reportProgress.mockRejectedValueOnce(new Error('网络中断')).mockImplementationOnce(
      () => new Promise((r) => { resolve = r; }),
    );
    renderRunner([q('1')], courseContext);

    await answerChoice();
    await screen.findByTestId('tx-report-error');

    const retry = screen.getByTestId('tx-retry');
    fireEvent.click(retry);
    fireEvent.click(retry);
    expect(reportProgress).toHaveBeenCalledTimes(2);
    resolve({});
    await waitFor(() => expect(screen.queryByTestId('tx-report-error')).toBeNull());
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).toHaveBeenCalledTimes(2);
  });
});

// 卡 TX-PRACTICE-HOOKS-SKIP-TESTER：Hooks 顺序 / submitSkip 按服务端 verdict 分流。
describe('题库切换与 Hooks 稳定性', () => {
  const rerenderWith = (
    r: ReturnType<typeof render>,
    questions: QuestionRow[] | null,
    ctx?: typeof courseContext,
  ) =>
    r.rerender(<PracticeRunner topic={topic} questions={questions} userId={2} onExit={() => {}} courseContext={ctx} />);

  it('空→非空切换不违反 Hooks 顺序，之后练习与上报照常', async () => {
    mockVerdicts({});
    const r = renderRunner([], courseContext);
    expect(screen.getByTestId('tx-practice-empty')).toBeTruthy();

    rerenderWith(r, [q('1')], courseContext);
    expect(screen.queryByTestId('tx-practice-empty')).toBeNull();
    expect(screen.getByText('第 1 / 1 题')).toBeTruthy();

    await answerChoice();
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
  });

  it('练习中途切换到空题库再切回：不崩、已判对计数保留（50 分而非 100）', async () => {
    mockVerdicts({ A: 'correct', B: 'wrong' });
    const r = renderRunner([q('1'), q('2')], courseContext);
    await answerChoice('A'); // 第 1 题 correct
    await screen.findByText('第 2 / 2 题');
    fireEvent.click(screen.getByTestId('tx-choice-B')); // 选中未提交，验证状态不丢

    rerenderWith(r, [], courseContext);
    expect(screen.getByTestId('tx-practice-empty')).toBeTruthy();

    rerenderWith(r, [q('1'), q('2')], courseContext);
    expect(screen.getByText('第 2 / 2 题')).toBeTruthy();
    // 已选选项（ask 态本地状态）保留，直接提交
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    expect(screen.getByTestId('tx-verdict').textContent).toContain('不正确');
    fireEvent.click(screen.getByTestId('tx-next'));

    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
  });
});

describe('submitSkip 按服务端 verdict 分流', () => {
  it('服务端 manual：显示 skip 面板并回填服务端参考答案，不进 judged', async () => {
    let asked = 0;
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) => {
      asked += 1; // 统计真实 POST 次数（skip 也必须是真实请求）
      if (req.answer === '不会') return { verdict: 'manual', reference_answer: '服务端参考', explanation: '服务端解析' };
      return { verdict: 'correct', reference_answer: '参考答案', explanation: null };
    });
    renderRunner([q('1'), q('2')], courseContext);

    // 第 1 题点「不会」
    fireEvent.click(screen.getByTestId('tx-skip'));
    const skipNext = await screen.findByTestId('tx-skip-next');
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.getByText('服务端参考')).toBeTruthy();
    // 解析必须取提交后服务端响应的顶层 explanation（现契约），不得从
    // question.explanation 等题目数据补。
    expect(screen.getByText('服务端解析')).toBeTruthy();
    expect(screen.queryByText('题库内置解析')).toBeNull();
    fireEvent.click(skipNext);
    await screen.findByText('第 2 / 2 题');

    // 第 2 题真实答对
    await answerChoice('A');
    expect(asked).toBe(2); // 两次提交均走真实 POST

    await screen.findByText('练习完成');
    // skip 不进分母：1 对 / 1 judged = 100
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
  });

  it('服务端对「不会」判 correct：走 judged 面板与真实计数，不进入 skip（100 分）', async () => {
    taxonomyApi.practice.mockImplementation(async () => ({
      verdict: 'correct',
      reference_answer: '不会',
      explanation: null,
    }));
    renderRunner([q('1'), q('2')], courseContext);

    fireEvent.click(screen.getByTestId('tx-skip'));
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-skips')).toBeNull();

    // 第 1 题结束 → 第 2 题同样被判 correct
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-verdict');
    fireEvent.click(screen.getByTestId('tx-next'));

    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
  });

  it('服务端对「不会」判 wrong：显示不正确反馈，计入 wrong 分母（0 分）', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) =>
      req.answer === '不会'
        ? { verdict: 'wrong', reference_answer: '2', explanation: '应为 2' }
        : { verdict: 'correct', reference_answer: '2', explanation: null },
    );
    renderRunner([q('1'), q('2')], courseContext);

    // 第 1 题点「不会」→ wrong
    fireEvent.click(screen.getByTestId('tx-skip'));
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答不正确');
    expect(screen.getByText('应为 2')).toBeTruthy();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    fireEvent.click(screen.getByTestId('tx-next'));

    // 第 2 题正常答对 → 1 对 / 2 judged = 50
    await answerChoice('A');
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
  });

  it('skip 的 POST 失败：显示错误、不进入 skip/judged、不推进题目', async () => {
    taxonomyApi.practice.mockRejectedValueOnce(new Error('网络中断'));
    renderRunner([q('1'), q('2')], courseContext);

    fireEvent.click(screen.getByTestId('tx-skip'));
    const err = await screen.findByTestId('tx-practice-error');
    expect(err.textContent).toContain('网络中断');
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.getByTestId('tx-submit')).toBeTruthy();

    // 重试后回到 ask 态可继续
    taxonomyApi.practice.mockImplementation(async () => ({ verdict: 'manual', reference_answer: 'x', explanation: null }));
    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
  });
});

// 卡 DEF-PRACTICE-REATTEMPT-COUNT-V2-TESTER：judged 后无重新作答入口、同轮同题只
// 计一次；self 落库 POST 失败不 onAnswered/不推进且可重试；manual/self 成功只推进
// 一次且不进 judged 分母；correct/wrong 意外返回不重复计数/不重复推进。
describe('重新作答与计数去重', () => {
  type PracticeResult = { verdict: string; reference_answer: string; explanation: null };

  it('judged 后无重新作答入口，双击下一题只推进一次且同轮同题只计一次', async () => {
    mockVerdicts({ A: 'correct', B: 'wrong' });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');

    // judged 面板只允许前进，不再提供任何重新作答/自评/跳题入口
    expect(screen.queryByTestId('tx-submit')).toBeNull();
    expect(screen.queryByTestId('tx-choices')).toBeNull();
    expect(screen.queryByTestId('tx-choice-A')).toBeNull();
    expect(screen.queryByTestId('tx-answer-input')).toBeNull();
    expect(screen.queryByTestId('tx-skip')).toBeNull();
    expect(screen.queryByTestId('tx-peek')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.queryByTestId('tx-self-wrong')).toBeNull();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.getByTestId('tx-next')).toBeTruthy();

    // 双击「下一题」：不得重复计数，也不得跳到第 3 题/直接完成
    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('第 2 / 2 题');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(taxonomyApi.practice).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ question_id: '1', answer: 'A' }),
    );
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', true);

    // 第 2 题答错：1 对 / 2 judged = 50；若第 1 题被重复计数则不是 50
    await answerChoice('B');
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenNthCalledWith(2, '2', false);
    expect(screen.getByText(/共 2 题，答对 1 题/)).toBeTruthy();
  });

  it('self 落库 POST 失败：不 onAnswered、不推进、错误可见且原入口可重试', async () => {
    let calls = 0;
    taxonomyApi.practice.mockImplementation(async () => {
      calls += 1;
      if (calls === 2) throw new Error('自评落库失败');
      return { verdict: 'manual', reference_answer: '服务端参考', explanation: null };
    });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1', 'self'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-peek')); // 看参考答案 → manual/self 面板
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right')); // 落库 POST 失败

    const err = await screen.findByTestId('tx-practice-error');
    expect(err.textContent).toContain('自评落库失败');
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.getByTestId('tx-self-right')).toBeTruthy();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(taxonomyApi.practice).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ question_id: '1', answer: '对' }),
    );

    // 重试同一个入口：成功后恰推进一次、恰上报一次
    fireEvent.click(screen.getByTestId('tx-self-right'));
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(3);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', true);

    // 第 2 题 skip→manual：judged=0，不上报课程进度
    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    fireEvent.click(screen.getByTestId('tx-skip-next'));
    await screen.findByText('练习完成');
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).not.toHaveBeenCalled();
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/共 2 题，答对 0 题/)).toBeTruthy();
  });

  it('self 自评成功只推进一次、不进 judged 分母（零 reportProgress）', async () => {
    // 第 2 次 POST（自评落库）挂起，验证 busy 闸门挡住重复点击。
    let resolveSelf!: (v: PracticeResult) => void;
    let calls = 0;
    taxonomyApi.practice.mockImplementation(async () => {
      calls += 1;
      if (calls === 2) return new Promise<PracticeResult>((r) => { resolveSelf = r; });
      return { verdict: 'manual', reference_answer: '服务端参考', explanation: null };
    });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1', 'self'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-peek'));
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(2));
    await waitFor(() => expect((screen.getByTestId('tx-self-right') as HTMLButtonElement).disabled).toBe(true));
    fireEvent.click(screen.getByTestId('tx-self-right')); // 重复点击不发起第二次落库
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);

    resolveSelf({ verdict: 'manual', reference_answer: '服务端参考', explanation: null });
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', true);

    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    fireEvent.click(screen.getByTestId('tx-skip-next'));
    await screen.findByText('练习完成');
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).not.toHaveBeenCalled();
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(screen.getByText(/共 2 题，答对 0 题/)).toBeTruthy();
  });

  it('manual（不会）成功只推进一次且不进 judged 分母（1 对 / 1 judged = 100）', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) =>
      req.answer === 'A'
        ? { verdict: 'correct', reference_answer: '1', explanation: null }
        : { verdict: 'manual', reference_answer: '服务端参考', explanation: null },
    );
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    // 双击「下一题」：manual 只推进到 Q2，不直接完成、不重复计数
    const skipNext = screen.getByTestId('tx-skip-next');
    fireEvent.click(skipNext);
    fireEvent.click(skipNext);
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(onAnswered).not.toHaveBeenCalled();

    await answerChoice('A');
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '2', true);
  });

  it('self 自评 POST 意外返回 correct：post 已计数，submitSelf 不重复计数/推进', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) => {
      if (req.answer === '对') return { verdict: 'correct', reference_answer: '对', explanation: null };
      if (req.answer === 'B') return { verdict: 'wrong', reference_answer: '2', explanation: null };
      return { verdict: 'manual', reference_answer: '服务端参考', explanation: null };
    });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1', 'self'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-peek')); // manual → self 面板
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right')); // 意外 correct
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.queryByTestId('tx-self-wrong')).toBeNull();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', true);

    fireEvent.click(screen.getByTestId('tx-next')); // 由 judged 面板推进，不得二次 advance
    await screen.findByText('第 2 / 2 题');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(1);

    await answerChoice('B'); // 第 2 题判错
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    // 1 对 / 2 judged = 50；若 Q1 重复计数则为 67，若重复推进则为 100
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(3);
    expect(onAnswered).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenNthCalledWith(2, '2', false);
  });

  it('skip POST 意外返回 wrong：post 已计数，submitSkip 不覆盖面板/不重复推进', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) =>
      req.answer === '不会'
        ? { verdict: 'wrong', reference_answer: '2', explanation: '应为 2' }
        : { verdict: 'correct', reference_answer: '1', explanation: null },
    );
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-skip'));
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答不正确');
    expect(screen.getByText('应为 2')).toBeTruthy();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', false);

    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenCalledTimes(1);

    await answerChoice('A');
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    // 1 对 / 2 judged = 50；若 Q1 重复计数则为 33，若重复推进则为 0
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenNthCalledWith(2, '2', true);
  });
});

// 卡 DEF-PRACTICE-RUNNER-R2-CODER：慢网/键盘/乱序响应下的一次性与竞态。
// deferred promise 手动控制响应时序，不依赖定时器碰运气。
describe('慢网络与竞态（R2）', () => {
  /** 手动控制的 pending practice 响应：resolve/reject 由测试决定时机。 */
  function deferredPractice() {
    let resolve!: (v: { verdict: string; reference_answer?: string; explanation?: string | null }) => void;
    let reject!: (e: unknown) => void;
    const promise = new Promise<Parameters<typeof resolve>[0]>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    taxonomyApi.practice.mockImplementation(() => promise);
    return { resolve, reject };
  }

  it('慢网络下 numeric 输入框连按 Enter + 连点提交：只发 1 个 POST，一次推进', async () => {
    const { resolve } = deferredPractice();
    const onAnswered = vi.fn();
    render(
      <PracticeRunner
        topic={topic}
        questions={[q('1', 'numeric'), q('2', 'numeric')]}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );

    const input = screen.getByTestId('tx-answer-input') as HTMLInputElement;
    fireEvent.change(input, { target: { value: '5' } });
    // 同一 tick 连按 Enter 与点击提交：状态更新 flush 前的并发提交
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.click(screen.getByTestId('tx-submit'));
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    // 慢响应落地：一次判定面板，一次 onAnswered
    await act(async () => {
      resolve({ verdict: 'correct', reference_answer: '5' });
    });
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    // 下一题按钮可点且只推进一次（双击不跳题）
    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(onAnswered).toHaveBeenCalledTimes(1);
  });

  it('choice 慢响应未回时连点提交两次：只 1 个 POST，返回后只一次判定', async () => {
    const { resolve } = deferredPractice();
    renderRunner([q('1'), q('2')]);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    fireEvent.click(screen.getByTestId('tx-skip')); // 同回合并发（skip 与 submit 互斥）
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(taxonomyApi.practice).toHaveBeenCalledWith(
      expect.objectContaining({ question_id: '1', answer: 'A' }),
    );

    await act(async () => {
      resolve({ verdict: 'correct', reference_answer: 'A' });
    });
    await screen.findByTestId('tx-verdict');
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
  });

  it('乱序：第一回合响应在推进到第二回合后才返回，不覆盖第二回合状态', async () => {
    // 用两条可控 deferred：第 1 题响应故意拖到第 2 题提交并返回之后才
    // resolve，验证 round 令牌丢弃迟到写入（不覆盖第 2 题面板、不重复计数）。
    const pendings: Array<{
      resolve: (v: { verdict: string; reference_answer: string }) => void;
    }> = [];
    let calls = 0;
    taxonomyApi.practice.mockImplementation(
      () =>
        new Promise((res) => {
          calls += 1;
          pendings.push({ resolve: res });
        }),
    );
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    // 第 1 题提交（响应不 resolve，挂起中）
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await waitFor(() => expect(calls).toBe(1));
    expect(pendings[0]).toBeTruthy();

    // 第 1 题迟到响应先落地 → judged 面板 → 推进到第 2 题
    await act(async () => {
      pendings[0].resolve({ verdict: 'wrong', reference_answer: '第1题答案' });
    });
    await screen.findByTestId('tx-verdict');
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');

    // 第 2 题提交并返回（响应 2）
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await waitFor(() => expect(calls).toBe(2));
    await act(async () => {
      pendings[1].resolve({ verdict: 'correct', reference_answer: '第2题答案' });
    });
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(screen.getByText('第2题答案')).toBeTruthy();
    expect(screen.queryByText('第1题答案')).toBeNull();

    // 双题各计一次；若第 1 题迟到响应被重复处理则计数/上报会翻倍
    expect(onAnswered).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', false);
    expect(onAnswered).toHaveBeenNthCalledWith(2, '2', true);
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
  });

  it('卸载后迟到响应：不写 state、不上报课程（act 内不抛未处理更新警告即通过）', async () => {
    const { resolve } = deferredPractice();
    const onExit = vi.fn();
    const { unmount } = render(
      <PracticeRunner
        topic={topic}
        questions={[q('1')]}
        userId={2}
        onExit={onExit}
        courseContext={courseContext}
      />,
    );

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    // 卸载后响应才回来：不得写任何 state（React 会因此抛警告/报错）
    unmount();
    await act(async () => {
      resolve({ verdict: 'correct', reference_answer: 'A' });
    });
    expect(reportProgress).not.toHaveBeenCalled();
  });

  it('题库切换后旧回合的迟到响应被丢弃：不污染新题库第一题', async () => {
    let resolveOld!: (v: { verdict: string; reference_answer: string }) => void;
    let first = true;
    taxonomyApi.practice.mockImplementation(() => {
      if (first) {
        first = false;
        return new Promise((r) => {
          resolveOld = r;
        });
      }
      return Promise.resolve({ verdict: 'manual', reference_answer: '新题库参考', explanation: null });
    });
    const onAnswered = vi.fn();
    const r = renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(1));

    // 切换题库（questions prop 变化）→ 旧回合令牌失效
    r.rerender(
      <PracticeRunner
        topic={{ ...topic, id: 'other' }}
        questions={[q('9')]}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
        courseContext={courseContext}
      />,
    );
    expect(screen.getByText('第 1 / 1 题')).toBeTruthy();

    // 旧回合迟到响应：不写新题库的 state、不计数
    await act(async () => {
      resolveOld({ verdict: 'wrong', reference_answer: '旧题库答案' });
    });
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByText('旧题库答案')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(judgedZero()).toBe(true);

    // 新题库正常作答（busy 已复位，不被旧回合卡死）
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-self-right'); // manual → self 自评面板
    expect(screen.getByText('新题库参考')).toBeTruthy();
  });

  it('未知 verdict：role=alert、停留本题、不推进不上报不进分母，重试可恢复', async () => {
    let calls = 0;
    taxonomyApi.practice.mockImplementation(() => {
      calls += 1;
      if (calls === 1) return { verdict: 'weird-future-value', reference_answer: 'X', explanation: 'Y' };
      return { verdict: 'correct', reference_answer: 'A', explanation: null };
    });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));

    // 未知 verdict → ErrorRow（role=alert），停留本题 ask 态
    const alert = await screen.findByTestId('tx-practice-error');
    expect(alert.getAttribute('role')).toBe('alert');
    expect(alert.textContent).toContain('未知判定');
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.getByTestId('tx-submit')).toBeTruthy();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(judgedZero()).toBe(true);

    // 重试：服务端恢复正常，正常走 judged 面板
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
    await answerChoice('A');
    await screen.findByText('练习完成');
    // 未知 verdict 不进分母：2 对 / 2 judged = 100；若计入则为 67
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
  });

  it('verdict 缺失（空）：与未知 verdict 同路，不推进不上报', async () => {
    taxonomyApi.practice.mockResolvedValueOnce({ verdict: '', reference_answer: 'X', explanation: null });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    const alert = await screen.findByTestId('tx-practice-error');
    expect(alert.getAttribute('role')).toBe('alert');
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(judgedZero()).toBe(true);
  });

  it('提交前无答案泄露：ask 态不渲染 question.answer/explanation/accept', async () => {
    const secret = q('1', 'numeric', { answer: '保密答案', accept: ['保密答案'], explanation: '保密解析' });
    renderRunner([secret]);

    const html = document.body.innerHTML;
    expect(html).not.toContain('保密答案');
    expect(html).not.toContain('保密解析');
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
  });

  it('空 manual 响应（reference_answer 缺省）不是获得答案的入口：面板显示无参考答案', async () => {
    taxonomyApi.practice.mockImplementation(async () => ({
      verdict: 'manual',
      question: { id: '1', prompt: '题 1', answer: '题库内置答案', explanation: '题库内置解析' },
    }));
    renderRunner([q('1')]);

    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    const html = document.body.innerHTML;
    expect(html).not.toContain('题库内置答案');
    expect(html).not.toContain('题库内置解析');
    expect(screen.getByText('（无参考答案）')).toBeTruthy();
  });

  it('课程上报恰一次：真实双击 tx-next 完成不重复上报（reportProgress 挂起期间）', async () => {
    let resolveReport!: (v: unknown) => void;
    reportProgress.mockImplementationOnce(
      () => new Promise((r) => {
        resolveReport = r;
      }),
    );
    mockVerdicts({ A: 'correct' });
    renderRunner([q('1')], courseContext);

    // 只提交到 judged 面板，不借 answerChoice 提前点 next
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');

    // 真实双击「完成练习」：只允许一次推进/一次上报
    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
    // 完成页不再存在下一题入口（真实存在性断言，替代恒真的 textContent 检查）
    expect(screen.queryByTestId('tx-next')).toBeNull();

    // 上报挂起期间无第二次；resolve 后也不追加
    await act(async () => {
      resolveReport({});
    });
    await waitFor(() => expect(screen.queryByTestId('tx-report-error')).toBeNull());
    await new Promise((r) => setTimeout(r, 30));
    expect(reportProgress).toHaveBeenCalledTimes(1);
  });
});

// 卡 DEF-PRACTICE-RUNNER-R2-TESTER：冻结契约要求 skip 与 manual/self 一样是
// 「成功推进信号」——不判错、不进 judged 分母、不写错误。服务端 verdict 契约
// 含 skip（诊断 index / ResultSkip），组件不得把它当未知值停在本题。
describe('服务端真实 skip verdict（冻结契约：成功推进信号）', () => {
  it('skip：进入答案面板可推进，不判错、不 onAnswered、不进 judged 分母', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { answer: string }) =>
      req.answer === '不会'
        ? { verdict: 'skip', reference_answer: '服务端参考', explanation: '服务端解析' }
        : { verdict: 'correct', reference_answer: '1', explanation: null },
    );
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-skip'));
    // 冻结契约：skip 是成功推进信号，不能落进「未知判定」错误分支
    const skipNext = await screen.findByTestId('tx-skip-next');
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();

    fireEvent.click(skipNext);
    await screen.findByText('第 2 / 2 题');
    // skip 不进分母：后续 1 对 / 1 judged = 100
    await answerChoice('A');
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 100, submitted_ref: 'cnm_p001' });
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '2', true);
  });

  it('skip 之后本题仍可重试且只推进一次（不因 skip 被当错误卡死）', async () => {
    let calls = 0;
    taxonomyApi.practice.mockImplementation(async () => {
      calls += 1;
      if (calls === 1) return { verdict: 'skip', reference_answer: '参考', explanation: null };
      return { verdict: 'correct', reference_answer: '1', explanation: null };
    });
    const onAnswered = vi.fn();
    renderWithAnswered([q('1'), q('2')], onAnswered);

    fireEvent.click(screen.getByTestId('tx-skip'));
    await screen.findByTestId('tx-skip-next');
    const next = screen.getByTestId('tx-skip-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
    expect(onAnswered).not.toHaveBeenCalled();
  });
});

// 卡 DEF-PRACTICE-RUNNER-R2-TESTER：回合身份必须包含作答者与课程上下文。
// 同一 topic/questions 下只换 userId 或 courseContext.resourceId 时，旧回合
// 在飞请求的迟到响应不得写入新回合面板/计数/上报。
describe('userId / courseContext 切换的迟到响应隔离', () => {
  it('同一 topic/questions 下 userId 切换：旧孩子迟到响应不写面板不 onAnswered，新孩子可重试', async () => {
    let resolveOld!: (v: PracticeReply) => void;
    taxonomyApi.practice.mockImplementation((req: { user_id?: number | null }) => {
      if (req.user_id === 2) {
        return new Promise<PracticeReply>((r) => {
          resolveOld = r;
        });
      }
      return Promise.resolve({ verdict: 'correct', reference_answer: '新孩子答案', explanation: null });
    });
    const onAnswered = vi.fn();
    const questions = [q('1'), q('2')];
    const r = renderWithAnswered(questions, onAnswered);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(1));
    expect(taxonomyApi.practice).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ user_id: 2, question_id: '1' }),
    );

    // 同一题库同一题目，只切作答者：旧请求仍在飞
    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={9}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();

    await act(async () => {
      resolveOld({ verdict: 'wrong', reference_answer: '旧孩子答案' });
    });
    // 旧孩子的迟到响应不得污染新孩子回合
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByText('旧孩子答案')).toBeNull();
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();

    // R2 冻结契约：身份切换完整复位——busy/answer/picked 一并复位，重新
    // 选题前提交保持禁用（旧断言 expected false 是 R1 部分复位时代的过期
    // 期望，与同文件 reviewer P1 用例的完整复位断言矛盾）。
    // 闸门已复位的意图由下方重新选题后可提交验证。
    expect((screen.getByTestId('tx-submit') as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    expect((screen.getByTestId('tx-submit') as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(taxonomyApi.practice).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ user_id: 9, question_id: '1' }),
    );
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
  });

  it('courseContext resource 切换：旧课程迟到响应不污染新回合，完成按新 resource 上报', async () => {
    let resolveOld!: (v: PracticeReply) => void;
    let first = true;
    taxonomyApi.practice.mockImplementation(() => {
      if (first) {
        first = false;
        return new Promise<PracticeReply>((r) => {
          resolveOld = r;
        });
      }
      return Promise.resolve({ verdict: 'correct', reference_answer: '新课程答案', explanation: null });
    });
    const onAnswered = vi.fn();
    const questions = [q('1')];
    const r = render(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
        courseContext={courseContext}
      />,
    );

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(1));

    // 同一 topic/questions，只切课程资源（42 → 43）：旧回合必须失效
    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
        courseContext={{ resourceId: 43, topicRef: 'cnm_p001' }}
      />,
    );
    await act(async () => {
      resolveOld({ verdict: 'wrong', reference_answer: '旧课程答案' });
    });
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByText('旧课程答案')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(judgedZero()).toBe(true);

    // 新回合正常完成：上报只按新 resourceId=43，score=100
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(43, { score: 100, submitted_ref: 'cnm_p001' });
    expect(reportProgress).not.toHaveBeenCalledWith(42, expect.anything());
  });
});

// 卡 DEF-PRACTICE-RUNNER-R2-TESTER：请求失败不得推进/上报，且可通过同一入口
// 重试成功；deferred 控制失败与成功时序，不依赖定时器。
describe('numeric 请求失败与拒绝后重试', () => {
  it('首次 reject 显示 role=alert 停留本题不推进；Enter 重试成功后仅推进一次', async () => {
    let rejectFirst!: (e: unknown) => void;
    let resolveSecond!: (v: PracticeReply) => void;
    let calls = 0;
    taxonomyApi.practice.mockImplementation(() => {
      calls += 1;
      if (calls === 1) {
        return new Promise<PracticeReply>((_, rej) => {
          rejectFirst = rej;
        });
      }
      return new Promise<PracticeReply>((res) => {
        resolveSecond = res;
      });
    });
    const onAnswered = vi.fn();
    render(
      <PracticeRunner
        topic={topic}
        questions={[q('1', 'numeric'), q('2', 'numeric')]}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );

    const input = screen.getByTestId('tx-answer-input') as HTMLInputElement;
    fireEvent.change(input, { target: { value: '5' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(calls).toBe(1));
    await act(async () => {
      rejectFirst(new Error('网络中断'));
    });

    const alert = await screen.findByTestId('tx-practice-error');
    expect(alert.getAttribute('role')).toBe('alert');
    expect(alert.textContent).toContain('网络中断');
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect((screen.getByTestId('tx-answer-input') as HTMLInputElement).value).toBe('5');
    expect(onAnswered).not.toHaveBeenCalled();
    expect(judgedZero()).toBe(true);

    // 拒绝后重试：同题同回合只再发一次 POST，成功后只推进一次
    fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(calls).toBe(2));
    await act(async () => {
      resolveSecond({ verdict: 'correct', reference_answer: '5', explanation: null });
    });
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(onAnswered).toHaveBeenNthCalledWith(1, '1', true);

    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
  });
});

// reviewer P1：身份（userId / courseContext）切换后，上一回合的本地进度
// （index/phase/correctCount/judgedRef/done/reported）必须整体复位。
describe('身份切换后的本地状态复位（reviewer P1）', () => {
  it('judged 面板后切 userId：新孩子回第 1 题 ask，旧 verdict/答案/计数不进新回合', async () => {
    taxonomyApi.practice.mockImplementation(
      async (req: { user_id?: number | null; question_id: string }) => {
        if (req.user_id === 2) {
          return { verdict: 'correct', reference_answer: '旧孩子参考', explanation: '旧孩子解析' };
        }
        if (req.question_id === '1') {
          return { verdict: 'wrong', reference_answer: '新参考1', explanation: null };
        }
        return { verdict: 'correct', reference_answer: '新参考2', explanation: null };
      },
    );
    const onAnswered = vi.fn();
    const questions = [q('1'), q('2')];
    const r = renderWithAnswered(questions, onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    const oldVerdict = await screen.findByTestId('tx-verdict');
    expect(oldVerdict.textContent).toContain('回答正确');
    expect(screen.getByText('旧孩子参考')).toBeTruthy();
    expect(onAnswered).toHaveBeenCalledTimes(1);

    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={9}
        onExit={() => {}}
        onAnswered={onAnswered}
        courseContext={courseContext}
      />,
    );

    // 新孩子必须从第 1 题 ask 开始：无旧面板/旧答案/旧选中/旧计数
    await waitFor(() => expect(screen.queryByTestId('tx-verdict')).toBeNull());
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.queryByText('回答正确')).toBeNull();
    expect(screen.queryByText('旧孩子参考')).toBeNull();
    expect(screen.queryByTestId('tx-next')).toBeNull();
    expect((screen.getByTestId('tx-submit') as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByTestId('tx-choice-A').getAttribute('aria-pressed')).toBe('false');

    // 新孩子自己作答：Q1 wrong → Q2 correct。旧 judgedRef.correct 若残留，
    // 课程分是 67 而不是 50；旧 phase.correct 若被 next 计入则进一步污染。
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    const v1 = await screen.findByTestId('tx-verdict');
    expect(v1.textContent).toContain('回答不正确');
    expect(onAnswered).toHaveBeenCalledTimes(2);
    expect(onAnswered).toHaveBeenNthCalledWith(2, '1', false);

    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    expect(onAnswered).toHaveBeenNthCalledWith(3, '2', true);

    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
  });

  it('done 后切 userId+resource：重置 done/index/reported，并按新 resource 重新上报', async () => {
    mockVerdicts({ A: 'correct' });
    const questions = [q('1')];
    const r = render(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={2}
        onExit={() => {}}
        courseContext={courseContext}
      />,
    );

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    expect(reportProgress).toHaveBeenNthCalledWith(1, 42, { score: 100, submitted_ref: 'cnm_p001' });
    await waitFor(() => expect(screen.queryByText('正在上报课程进度…')).toBeNull());

    // 换孩子 + 换课程资源：done/score/index/reportedRef 都必须复位
    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={9}
        onExit={() => {}}
        courseContext={{ resourceId: 43, topicRef: 'cnm_p001' }}
      />,
    );
    await waitFor(() => expect(screen.queryByText('练习完成')).toBeNull());
    expect(screen.queryByText(/答对 1 题/)).toBeNull();
    expect(screen.getByText('第 1 / 1 题')).toBeTruthy();
    expect((screen.getByTestId('tx-submit') as HTMLButtonElement).disabled).toBe(true);

    // 新身份完成后必须再次上报（reportedRef 未复位则这里调用数仍为 1）
    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(2));
    expect(reportProgress).toHaveBeenNthCalledWith(2, 43, { score: 100, submitted_ref: 'cnm_p001' });
  });

  it('切换到更短题库：不残留越界 index、不跳过作答直接完成', async () => {
    taxonomyApi.practice.mockImplementation(async (req: { question_id: string }) =>
      req.question_id === 'x' || req.question_id === 'y'
        ? { verdict: 'correct', reference_answer: '答案', explanation: null }
        : { verdict: 'wrong', reference_answer: '答案', explanation: null },
    );
    const onAnswered = vi.fn();
    const r = renderWithAnswered([q('a'), q('b'), q('c')], onAnswered);

    await answerChoice('A'); // a wrong → b
    await answerChoice('A'); // b wrong → c（旧 index=2）
    expect(screen.getByText('第 3 / 3 题')).toBeTruthy();

    // 新题库只有 2 题：渲染 index 必须落在合法范围，且仍需真实作答
    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={[q('x'), q('y')]}
        userId={2}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );
    await waitFor(() => expect(screen.queryByText('第 3 / 2 题')).toBeNull());
    expect(screen.getByText(/第 [12] \/ 2 题/)).toBeTruthy();
    expect(screen.queryByText('第 3 / 2 题')).toBeNull();
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(screen.queryByTestId('tx-next')).toBeNull();
    expect(screen.getByTestId('tx-submit')).toBeTruthy();

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(3); // 2 旧 + 1 新
    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    expect(screen.getByText(/共 2 题，答对 1 题/)).toBeTruthy();
  });

  it('React.StrictMode 冒烟：初始化不 POST，连击只 1 个 POST 且只推进一次', async () => {
    const { promise, resolve } = deferredReply();
    taxonomyApi.practice.mockImplementation(() => promise);
    const onAnswered = vi.fn();

    render(
      <StrictMode>
        <PracticeRunner
          topic={topic}
          questions={[q('1', 'numeric'), q('2', 'numeric')]}
          userId={2}
          onExit={() => {}}
          onAnswered={onAnswered}
        />
      </StrictMode>,
    );
    // 初始化不触发 POST
    expect(taxonomyApi.practice).not.toHaveBeenCalled();

    const input = screen.getByTestId('tx-answer-input') as HTMLInputElement;
    fireEvent.change(input, { target: { value: '5' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.keyDown(input, { key: 'Enter' });
    fireEvent.click(screen.getByTestId('tx-submit'));
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolve({ verdict: 'correct', reference_answer: '5', explanation: null });
    });
    await screen.findByTestId('tx-verdict');
    expect(onAnswered).toHaveBeenCalledTimes(1);

    const next = screen.getByTestId('tx-next');
    fireEvent.click(next);
    fireEvent.click(next);
    await screen.findByText('第 2 / 2 题');
    expect(screen.queryByText('练习完成')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
  });
});

// reviewer 追加：await 后换轮（self/skip）与 onAnswered 回调异常。
describe('await 后换轮与回调异常隔离', () => {
  it('self 自评 POST 在飞时切 userId：迟到 self 不推进不 onAnswered，新孩子回 ask', async () => {
    let resolveSelf!: (v: PracticeReply) => void;
    taxonomyApi.practice.mockImplementation((req: { answer: string }) => {
      if (req.answer === '对') {
        return new Promise<PracticeReply>((r) => {
          resolveSelf = r;
        });
      }
      return Promise.resolve({ verdict: 'manual', reference_answer: '服务端参考', explanation: null });
    });
    const onAnswered = vi.fn();
    const questions = [q('1', 'self'), q('2', 'self')];
    const r = renderWithAnswered(questions, onAnswered);

    fireEvent.click(screen.getByTestId('tx-peek'));
    await screen.findByTestId('tx-self-right');
    fireEvent.click(screen.getByTestId('tx-self-right'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(2));

    // 自评落库在飞时换孩子：新回合必须回到 Q1 ask
    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={9}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );
    await waitFor(() => expect(screen.queryByTestId('tx-self-right')).toBeNull());
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.getByTestId('tx-peek')).toBeTruthy();

    // 旧回合 self 响应迟到：不得推进/不 onAnswered/不污染新回合
    await act(async () => {
      resolveSelf({ verdict: 'self', reference_answer: '服务端参考', explanation: null });
    });
    expect(onAnswered).not.toHaveBeenCalled();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.queryByText('第 2 / 2 题')).toBeNull();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(2);
  });

  it('skip POST 在飞时切 userId：迟到 manual 不显示 skip 面板不推进不上报', async () => {
    const { promise, resolve } = deferredReply();
    taxonomyApi.practice.mockImplementation(() => promise);
    const onAnswered = vi.fn();
    const questions = [q('1'), q('2')];
    const r = renderWithAnswered(questions, onAnswered);

    fireEvent.click(screen.getByTestId('tx-skip'));
    await waitFor(() => expect(taxonomyApi.practice).toHaveBeenCalledTimes(1));

    r.rerender(
      <PracticeRunner
        topic={topic}
        questions={questions}
        userId={9}
        onExit={() => {}}
        onAnswered={onAnswered}
      />,
    );
    await waitFor(() => expect(screen.getByTestId('tx-skip')).toBeTruthy());
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();

    await act(async () => {
      resolve({ verdict: 'manual', reference_answer: '服务端参考', explanation: null });
    });
    expect(screen.queryByTestId('tx-skip-next')).toBeNull();
    expect(screen.queryByTestId('tx-self-right')).toBeNull();
    expect(screen.queryByTestId('tx-verdict')).toBeNull();
    expect(onAnswered).not.toHaveBeenCalled();
    expect(screen.getByText('第 1 / 2 题')).toBeTruthy();
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);
  });

  it('onAnswered 抛错不被当网络失败：仍进 judged 面板且不重复 judged', async () => {
    mockVerdicts({ A: 'correct', B: 'wrong' });
    const onAnswered = vi.fn((id: string, _correct: boolean) => {
      if (id === '1') throw new Error('onAnswered 崩溃');
    });
    renderWithAnswered([q('1'), q('2')], onAnswered, courseContext);

    fireEvent.click(screen.getByTestId('tx-choice-A'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    // 回调异常不得被 catch 成请求失败：面板照常进 judged，无错误行
    const verdict = await screen.findByTestId('tx-verdict');
    expect(verdict.textContent).toContain('回答正确');
    expect(screen.queryByTestId('tx-practice-error')).toBeNull();
    expect(onAnswered).toHaveBeenCalledTimes(1);
    expect(taxonomyApi.practice).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('第 2 / 2 题');
    fireEvent.click(screen.getByTestId('tx-choice-B'));
    fireEvent.click(screen.getByTestId('tx-submit'));
    await screen.findByTestId('tx-verdict');
    expect(onAnswered).toHaveBeenCalledTimes(2);

    fireEvent.click(screen.getByTestId('tx-next'));
    await screen.findByText('练习完成');
    await waitFor(() => expect(reportProgress).toHaveBeenCalledTimes(1));
    // 1 对 / 2 judged = 50；回调抛错若导致重复 judged 会变 67
    expect(reportProgress).toHaveBeenCalledWith(42, { score: 50, submitted_ref: 'cnm_p001' });
  });
});
