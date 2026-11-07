// 纯函数测试（node 环境）：标签/分组/布局/结果文案（卡 TX-FE-CODER，
// TX-SUBJECTS-HELPERS-TESTER：SUBJECT_ORDER 已按设计删除，仅保留展示 fallback 测试）。
import { describe, expect, it } from 'vitest';
import {
  clampConfirm,
  clampLimit,
  gradeLabel,
  groupByChapter,
  groupPathByChapter,
  layoutPrereqGraph,
  resultHeadline,
  semesterLabel,
  sessionModeLabel,
  sessionStatusLabel,
  sortSessions,
  stageKind,
  stageLabel,
  statusLabel,
  subjectName,
  SUBJECT_NAMES,
} from './labels';

describe('subjectName / SUBJECT_NAMES 展示 fallback', () => {
  it('十学科中文', () => {
    expect(subjectName('math')).toBe('数学');
    expect(subjectName('chinese')).toBe('语文');
    expect(subjectName('english')).toBe('英语');
    expect(subjectName('physics')).toBe('物理');
    expect(subjectName('chemistry')).toBe('化学');
    expect(subjectName('science')).toBe('科学');
    expect(subjectName('morality')).toBe('道德与法治');
    expect(subjectName('history')).toBe('历史');
    expect(subjectName('geography')).toBe('地理');
    expect(subjectName('biology')).toBe('生物');
  });
  it('空串与未知值', () => {
    expect(subjectName('')).toBe('未分学科');
    expect(subjectName(undefined)).toBe('未分学科');
    expect(subjectName('unknown-key')).toBe('unknown-key');
  });
  it("SUBJECT_NAMES 仅作展示 fallback：'' 映射未分学科，十学科均有中文名", () => {
    expect(SUBJECT_NAMES['']).toBe('未分学科');
    expect(Object.keys(SUBJECT_NAMES).filter(Boolean)).toHaveLength(10);
    for (const key of Object.keys(SUBJECT_NAMES)) {
      expect(subjectName(key)).toBe(SUBJECT_NAMES[key]);
    }
  });
});

describe('gradeLabel / semesterLabel / stage', () => {
  it('1-9 年级中文数字', () => {
    expect(gradeLabel(1)).toBe('一年级');
    expect(gradeLabel(7)).toBe('七年级');
    expect(gradeLabel(9)).toBe('九年级');
  });
  it('高中册次（math/english/physics 各自映射）', () => {
    expect(gradeLabel(10)).toBe('高一');
    expect(gradeLabel(11)).toBe('高二');
    expect(gradeLabel(10, 'english')).toBe('必修一');
    expect(gradeLabel(12, 'physics')).toBe('必修三');
    expect(gradeLabel(16, 'english')).toBe('选必四');
  });
  it('null 与超界回退', () => {
    expect(gradeLabel(null)).toBe('');
    expect(gradeLabel(undefined)).toBe('');
  });
  it('学期上/下/全', () => {
    expect(semesterLabel('上')).toBe('上册');
    expect(semesterLabel('下')).toBe('下册');
    expect(semesterLabel('全')).toBe('全一册');
    expect(semesterLabel(null)).toBe('');
  });
  it('学段划分', () => {
    expect(stageKind(1)).toBe('primary');
    expect(stageKind(6)).toBe('primary');
    expect(stageKind(7)).toBe('junior');
    expect(stageKind(9)).toBe('junior');
    expect(stageKind(10)).toBe('senior');
    expect(stageLabel(3)).toBe('小学');
    expect(stageLabel(8)).toBe('初中');
    expect(stageLabel(12)).toBe('高中');
  });
});

describe('clampLimit / clampConfirm', () => {
  it('普通诊断 8-30', () => {
    expect(clampLimit(3, false)).toBe(8);
    expect(clampLimit(16, false)).toBe(16);
    expect(clampLimit(99, false)).toBe(30);
    expect(clampLimit('12', false)).toBe(12);
    expect(clampLimit(null, false)).toBe(16);
    expect(clampLimit('abc', false)).toBe(16);
  });
  it('筛查 20-60', () => {
    expect(clampLimit(10, true)).toBe(20);
    expect(clampLimit(40, true)).toBe(40);
    expect(clampLimit(100, true)).toBe(60);
    expect(clampLimit(undefined, true)).toBe(40);
  });
  it('确认题数只收 2/3/5', () => {
    expect(clampConfirm(2)).toBe(2);
    expect(clampConfirm(3)).toBe(3);
    expect(clampConfirm(5)).toBe(5);
    expect(clampConfirm(4)).toBe(3);
    expect(clampConfirm('2')).toBe(2);
    expect(clampConfirm(null)).toBe(3);
    expect(clampConfirm('x')).toBe(3);
  });
});

describe('groupByChapter', () => {
  it('按 chapter 字段分组并保持顺序', () => {
    const groups = groupByChapter([
      { id: 'a', external_ref: 'ref_a', name: 'A', grade: 1, semester: null, domain: null, chapter: '一年级上册 第1单元 X' },
      { id: 'b', external_ref: 'ref_b', name: 'B', grade: 1, semester: null, domain: null, chapter: '一年级上册 第2单元 Y' },
      { id: 'c', external_ref: 'ref_c', name: 'C', grade: 1, semester: null, domain: null, chapter: '一年级上册 第1单元 X' },
      { id: 'd', external_ref: 'ref_d', name: 'D', grade: 1, semester: null, domain: null, chapter: null },
    ]);
    expect(groups).toHaveLength(3);
    expect(groups[0].label).toBe('一年级上册 第1单元 X');
    expect(groups[0].topics.map((t) => t.id)).toEqual(['a', 'c']);
    expect(groups[1].label).toBe('一年级上册 第2单元 Y');
    expect(groups[2].label).toBe('未标注章节');
  });
  it('null 输入返回空数组（防御）', () => {
    expect(groupByChapter(null)).toEqual([]);
    expect(groupByChapter(undefined)).toEqual([]);
  });
});

describe('layoutPrereqGraph', () => {
  const topic = { id: 't', external_ref: 'ref_t', name: '当前', grade: 3, semester: null, domain: null, chapter: null };
  const pres = [
    { id: 'p1', external_ref: 'ref_p1', name: '先修一', grade: 1, semester: null, domain: null, chapter: null },
    { id: 'p2', external_ref: 'ref_p2', name: '先修二', grade: 1, semester: null, domain: null, chapter: null },
  ];
  const posts = [{ id: 'd1', external_ref: 'ref_d1', name: '后继', grade: 4, semester: null, domain: null, chapter: null }];

  it('年级升序排层、同行保传入顺序', () => {
    const layout = layoutPrereqGraph(topic, pres, posts);
    expect(layout.rows.map((r) => r.grade)).toEqual([1, 3, 4]);
    expect(layout.rows[0].ids).toEqual(['p1', 'p2']);
    expect(layout.rows[1].ids).toEqual(['t']);
    expect(layout.positions['p1']).toEqual({ x: 0, y: 0, grade: 1 });
    expect(layout.positions['t']).toEqual({ x: 0, y: 1, grade: 3 });
    expect(layout.width).toBe(2);
    expect(layout.height).toBe(3);
  });
  it('null 边列表安全', () => {
    const layout = layoutPrereqGraph(topic, null, null);
    expect(layout.rows).toHaveLength(1);
    expect(layout.positions['t']).toEqual({ x: 0, y: 0, grade: 3 });
  });
});

describe('resultHeadline', () => {
  it('screen 全好无未测', () => {
    expect(
      resultHeadline({ mode: 'screen', weak: [], shaky: [], untested: [], roots: [] }),
    ).toBe('范围内的知识点都已掌握，没有发现薄弱点。');
  });
  it('screen 有未测', () => {
    expect(
      resultHeadline({ mode: 'screen', weak: [], shaky: [], untested: ['a'], roots: [] }),
    ).toBe('已测到的知识点没有发现薄弱点，另有 1 个没测到。');
  });
  it('screen 有薄弱', () => {
    expect(
      resultHeadline({ mode: 'screen', weak: ['a'], shaky: ['b'], untested: ['c'], roots: [{ id: 'a' }] }),
    ).toBe('薄弱 1 个、不稳 1 个、未测 1 个。先看「按章汇总」，薄弱的章排在前面。');
  });
  it('target 目标全掌握', () => {
    expect(
      resultHeadline({ mode: 'target', weak: [], shaky: [], roots: [], targets: ['t1'] }),
    ).toBe('目标知识点已掌握，没有发现需要补的先修。');
  });
  it('target 有不稳无根因', () => {
    expect(
      resultHeadline({ mode: 'target', weak: [], shaky: ['s1'], roots: [], targets: ['t1'] }),
    ).toBe('没有判出薄弱点，但有 1 个知识点「不稳」（中途答错过、确认题答对），建议巩固。');
  });
  it('target 有根因', () => {
    expect(
      resultHeadline({ mode: 'target', weak: ['w1'], shaky: [], roots: [{ id: 'w1' }], targets: ['t1'] }),
    ).toBe('共发现 1 个薄弱根因，按学习路径从前往后补。');
  });
  it('空输入防御（null 数组）', () => {
    expect(resultHeadline({ mode: 'screen' })).toBe('范围内的知识点都已掌握，没有发现薄弱点。');
    expect(resultHeadline({})).toBe('没有判出薄弱点，但目标尚未全部测到。');
  });
});

describe('statusLabel / session 标签', () => {
  it('四态', () => {
    expect(statusLabel('mastered')).toBe('已掌握');
    expect(statusLabel('shaky')).toBe('不稳');
    expect(statusLabel('weak')).toBe('薄弱');
    expect(statusLabel('unknown')).toBe('未测');
    expect(statusLabel(null)).toBe('未测');
    expect(statusLabel('custom')).toBe('custom');
  });
  it('会话状态/模式', () => {
    expect(sessionStatusLabel('active')).toBe('进行中');
    expect(sessionStatusLabel('completed')).toBe('已完成');
    expect(sessionStatusLabel('voided')).toBe('已撤销');
    expect(sessionModeLabel('screen')).toBe('整体筛查');
    expect(sessionModeLabel('retest')).toBe('再测');
    expect(sessionModeLabel('target')).toBe('定向诊断');
  });
});

describe('sortSessions', () => {
  it('进行中在前，完成按时间倒序', () => {
    const sorted = sortSessions([
      { id: 1, subject: 'math', mode: 'target', status: 'completed', created_at: '2026-01-01T00:00:00Z', completed_at: '2026-01-01T00:00:00Z' },
      { id: 2, subject: 'math', mode: 'target', status: 'active', created_at: '2026-01-02T00:00:00Z' },
      { id: 3, subject: 'math', mode: 'screen', status: 'completed', created_at: '2026-01-03T00:00:00Z', completed_at: '2026-01-03T00:00:00Z' },
    ]);
    expect(sorted.map((x) => x.id)).toEqual([2, 3, 1]);
  });
  it('null 输入空数组', () => {
    expect(sortSessions(null)).toEqual([]);
  });
});

describe('groupPathByChapter', () => {
  it('按章切分保持路径顺序', () => {
    const groups = groupPathByChapter(
      [
        { id: 'a', status: 'weak' },
        { id: 'b', status: 'shaky' },
        { id: 'c', status: 'unknown' },
      ],
      (id) => (id === 'b' ? '第二章' : '第一章'),
    );
    expect(groups).toHaveLength(2);
    expect(groups[0].chapter).toBe('第一章');
    expect(groups[0].items.map((i) => i.id)).toEqual(['a', 'c']);
    expect(groups[1].items.map((i) => i.id)).toEqual(['b']);
  });
});
