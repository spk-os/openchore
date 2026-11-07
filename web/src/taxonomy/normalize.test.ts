// 契约归一化纯函数测试（卡 TX-FE-FIX2）：camelCase 引擎 tag → snake_case、
// detail.question 两段拍平、share url token 提取、盲区 ref/id 回退、
// attempts 结果从独立 verdict correct 派生。真实后端形状以
// internal/taxonomy/engine.go 与 internal/api/taxonomy.go 的 json tag 为准。

import { describe, expect, it } from 'vitest';
import {
  attemptResult,
  blindspotRef,
  normalizeDetail,
  normalizePathItem,
  normalizeQuestion,
  normalizeRootCause,
  normalizeSummary,
  tokenFromShareURL,
} from './normalize';
import type {
  BlindSpotEntry,
  DiagnosticAttempt,
  DiagnosticDetail,
  DiagnosticQuestionEnvelope,
  DiagnosticSummary,
} from './types';

describe('normalizeRootCause / normalizePathItem（camel → snake）', () => {
  it('引擎 camelCase tag（pendingPrereqs 等）归一为 snake_case', () => {
    const r = normalizeRootCause({
      id: 'cnm_p001',
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pendingPrereqs: ['cnm_p000'],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      outOfScopePrereqs: ['ext_001'],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      externalPrereqs: null,
    } as any);
    expect(r.pending_prereqs).toEqual(['cnm_p000']);
    expect(r.out_of_scope_prereqs).toEqual(['ext_001']);
    expect(r.external_prereqs).toBeNull();
  });

  it('snake 字段已存在时优先（camel 不覆盖）', () => {
    const r = normalizeRootCause({
      id: 'a',
      pending_prereqs: ['snake'],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      pendingPrereqs: ['camel'],
    } as any);
    expect(r.pending_prereqs).toEqual(['snake']);
  });

  it('PathItem isRoot/isTarget → is_root/is_target（缺省 false）', () => {
    expect(normalizePathItem({ id: 'a', status: 'weak' }).is_root).toBe(false);
    const p = normalizePathItem({ id: 'b', status: 'mastered', isRoot: true, isTarget: true });
    expect(p.is_root).toBe(true);
    expect(p.is_target).toBe(true);
    // snake 优先。
    expect(normalizePathItem({ id: 'c', status: 'weak', is_root: false, isRoot: true }).is_root).toBe(false);
  });
});

describe('normalizeSummary（limitReached → limit_reached）', () => {
  it('引擎 summary 归一（含 roots/path 深层）', () => {
    const raw = {
      mode: 'screen',
      asked: 8,
      limit: 8,
      finished: true,
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      limitReached: true,
      mastered: ['a'],
      shaky: [],
      weak: ['b'],
      roots: [{ id: 'b', externalPrereqs: ['x'] }],
      path: [{ id: 'b', status: 'weak', isRoot: true }],
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
    } as any;
    const s = normalizeSummary(raw as DiagnosticSummary);
    expect(s?.limit_reached).toBe(true);
    expect(s?.roots?.[0].external_prereqs).toEqual(['x']);
    expect(s?.path?.[0].is_root).toBe(true);
  });

  it('null 入参返回 null', () => {
    expect(normalizeSummary(null)).toBeNull();
    expect(normalizeSummary(undefined)).toBeNull();
  });
});

describe('normalizeQuestion（detail.question 两段拍平）', () => {
  it('服务端 envelope {topic_id, attempt, question} 拍平为单层', () => {
    const env: DiagnosticQuestionEnvelope = {
      topic_id: 'cnm_p001',
      attempt: 1,
      question: {
        id: 'cnm_p001_q1',
        difficulty: 'basic',
        type: 'numeric',
        prompt: '计算 2+2',
        choices: null,
      },
    };
    const q = normalizeQuestion(env);
    expect(q?.id).toBe('cnm_p001_q1');
    expect(q?.topic_id).toBe('cnm_p001');
    expect(q?.attempt).toBe(1);
    expect(q?.prompt).toBe('计算 2+2');
  });

  it('question 缺失（active 会话已无题）返回 null', () => {
    expect(normalizeQuestion({ topic_id: 'x', question: null })).toBeNull();
    expect(normalizeQuestion(null)).toBeNull();
    expect(normalizeQuestion(undefined)).toBeNull();
  });

  it('normalizeDetail 同时归一 summary 与保留 attempts', () => {
    const detail = {
      session: { id: 1, subject: 'math', mode: 'screen', status: 'completed' },
      attempts: [{ id: 9, seq: 1, correct: true, answer_json: '4', status: 'answered' }],
      summary: {
        asked: 1,
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        limitReached: false,
      },
    } as unknown as DiagnosticDetail;
    const norm = normalizeDetail(detail);
    expect(norm.summary?.limit_reached).toBe(false);
    expect(norm.attempts?.[0].correct).toBe(true);
  });
});

describe('tokenFromShareURL', () => {
  it('从 API 端点 url 提取 token', () => {
    expect(tokenFromShareURL('/api/taxonomy/shared/abc123')).toBe('abc123');
  });

  it('已是裸 token 时原样返回', () => {
    expect(tokenFromShareURL('abc123')).toBe('abc123');
  });
});

describe('blindspotRef（真实后端 blindspotRow 用 ref）', () => {
  it('优先 ref（真实后端形状：tag_id/ref/name/subject/mastery/...）', () => {
    const e = {
      tag_id: 3,
      ref: 'cnm_p002',
      name: '6-10的认数',
      subject: 'math',
      mastery: 0.3,
      correct_count: 1,
      total_count: 4,
      low_confidence: false,
      status: 'weak',
    } as BlindSpotEntry;
    expect(blindspotRef(e)).toBe('cnm_p002');
  });

  it('ref 缺失回退 id（旧 mock/快照兼容）', () => {
    expect(blindspotRef({ id: 'cnm_p001', status: 'weak' })).toBe('cnm_p001');
  });

  it('两处都缺/空串返回 null（再测按钮禁用）', () => {
    expect(blindspotRef({ status: 'weak' })).toBeNull();
    expect(blindspotRef({ ref: '  ', id: '', status: 'shaky' })).toBeNull();
  });
});

describe('attemptResult（真实后端无 result 字段，从 correct 派生）', () => {
  it('correct=true → correct；false → wrong；null（manual/self）→ skip', () => {
    expect(attemptResult({ id: 1, correct: true } as DiagnosticAttempt)).toBe('correct');
    expect(attemptResult({ id: 2, correct: false } as DiagnosticAttempt)).toBe('wrong');
    expect(attemptResult({ id: 3, correct: null } as DiagnosticAttempt)).toBe('skip');
    expect(attemptResult({ id: 4 } as DiagnosticAttempt)).toBe('skip');
  });

  it('旧 mock 带 result 时优先 result（兼容位）', () => {
    expect(attemptResult({ id: 5, result: 'wrong', correct: true } as DiagnosticAttempt)).toBe('wrong');
    expect(attemptResult({ id: 6, result: 'skip', correct: false } as DiagnosticAttempt)).toBe('skip');
  });
});
