// 组件间共用的纯函数：学科中文标签、章节分组、局部先修图布局、结果页文案。
// 全部无副作用、可单测（node 环境即可，无需 jsdom）。

import type { ChapterSummaryRow, DiagnosticSession, PathItem, TopicRow } from './types';

/** 十学科中文名（学科 key 与后端 subjects 表同域）。
 *  仅作 subjectName 的展示 fallback：字典没命中的学科原样回显 key，
 *  任何学科列表迭代不得再从这里取值（用 useSubjects）。 */
export const SUBJECT_NAMES: Record<string, string> = {
  '': '未分学科',
  math: '数学',
  chinese: '语文',
  english: '英语',
  physics: '物理',
  chemistry: '化学',
  science: '科学',
  morality: '道德与法治',
  history: '历史',
  geography: '地理',
  biology: '生物',
};

export function subjectName(key: string | null | undefined): string {
  if (!key) return SUBJECT_NAMES[''];
  return SUBJECT_NAMES[key] ?? key;
}

/** 学科 key 列表不再由本文件提供（TX-SUBJECTS-HELPERS-CODER）：浏览页顶栏
 *  等任何学科列表迭代一律改用 useSubjects（服务端 subjects 表驱动），
 *  本文件只保留单值展示的纯函数。 */

/** 知识点状态四态中文。 */
export const STATUS_LABELS: Record<string, string> = {
  mastered: '已掌握',
  shaky: '不稳',
  weak: '薄弱',
  unknown: '未测',
};

export function statusLabel(status: string | null | undefined): string {
  if (!status) return STATUS_LABELS.unknown;
  return STATUS_LABELS[status] ?? status;
}

/** 题型中文。 */
export const TYPE_LABELS: Record<string, string> = {
  numeric: '数值题',
  choice: '选择题',
  short: '短答题',
  self: '开放题',
  legacy: '诊断题',
};

export function typeLabel(type: string | null | undefined): string {
  if (!type) return '';
  return TYPE_LABELS[type] ?? type;
}

/** 难度中文。 */
export const DIFFICULTY_LABELS: Record<string, string> = {
  basic: '基础',
  standard: '提高',
  challenge: '拓展',
  advanced: '拓展',
};

export function difficultyLabel(difficulty: string | null | undefined): string {
  if (!difficulty) return '';
  return DIFFICULTY_LABELS[difficulty] ?? difficulty;
}

/** 作答结果中文（答题记录表）。 */
export const RESULT_LABELS: Record<string, string> = {
  correct: '对',
  wrong: '错',
  skip: '不会',
};

export function resultLabel(result: string | null | undefined): string {
  if (!result) return '';
  return RESULT_LABELS[result] ?? result;
}

/** 年级数字 → 中文（1-9；10+ 沿用上游册次习惯由调用方另行映射）。 */
const CN_DIGITS = ['', '一', '二', '三', '四', '五', '六', '七', '八', '九'];

/** 学段：primary/junior/senior（图例配色与文案共用）。 */
export function stageKind(grade: number | null | undefined): 'primary' | 'junior' | 'senior' {
  const g = grade ?? 0;
  if (g <= 6) return 'primary';
  if (g <= 9) return 'junior';
  return 'senior';
}

export function stageLabel(grade: number | null | undefined): string {
  const kind = stageKind(grade);
  return kind === 'primary' ? '小学' : kind === 'junior' ? '初中' : '高中';
}

/** 年级显示：1-9 → 「N年级」，10+ 高中册次。 */
export function gradeLabel(grade: number | null | undefined, subject?: string): string {
  const g = grade ?? 0;
  if (g >= 1 && g <= 9) return `${CN_DIGITS[g]}年级`;
  const seniorMath: Record<number, string> = { 10: '高一', 11: '高二', 12: '选必一', 13: '选必二', 14: '选必三' };
  const seniorEn: Record<number, string> = { 10: '必修一', 11: '必修二', 12: '必修三', 13: '选必一', 14: '选必二', 15: '选必三', 16: '选必四' };
  const seniorPhy: Record<number, string> = { 10: '必修一', 11: '必修二', 12: '必修三', 13: '选必一', 14: '选必二', 15: '选必三' };
  if (subject === 'english' && seniorEn[g]) return seniorEn[g];
  if (subject === 'physics' && seniorPhy[g]) return seniorPhy[g];
  if (seniorMath[g]) return seniorMath[g];
  return g ? `年级 ${g}` : '';
}

/** 学期显示：「上」→ 上册、「下」→ 下册。 */
export function semesterLabel(semester: string | null | undefined): string {
  if (!semester) return '';
  if (semester === '上') return '上册';
  if (semester === '下') return '下册';
  if (semester === '全') return '全一册';
  return semester;
}

/** ISO 时间 → 「2026-11-05 14:30」本地显示；解析失败原样返回。 */
export function formatDateTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 题量输入收敛：普通 8–30，筛查 20–60（上游 LIMIT_MIN/MAX 同值）。 */
export function clampLimit(raw: number | string | null | undefined, screen: boolean): number {
  const min = screen ? 20 : 8;
  const max = screen ? 60 : 30;
  let v = typeof raw === 'number' ? raw : parseInt(String(raw ?? ''), 10);
  if (!Number.isFinite(v)) v = screen ? 40 : 16;
  if (v < min) v = min;
  if (v > max) v = max;
  return v;
}

/** 确认题数收敛：只接受 2/3/5，其余回默认 3（引擎 ClampConfirm 同语义）。 */
export function clampConfirm(raw: number | string | null | undefined): number {
  const v = typeof raw === 'number' ? raw : parseInt(String(raw ?? ''), 10);
  if (v === 2 || v === 3 || v === 5) return v;
  return 3;
}

export interface ChapterGroup {
  key: string;
  label: string;
  topics: TopicRow[];
}

/**
 * 知识点按章分组（浏览目录）。chapter 字段本身就是「册 章号 章名」全文，
 * 直接以其为 key 保持教材顺序；空 chapter 归「未标注章节」。
 */
export function groupByChapter(topics: TopicRow[] | null | undefined): ChapterGroup[] {
  const list = topics ?? [];
  const groups: ChapterGroup[] = [];
  const byKey = new Map<string, ChapterGroup>();
  for (const t of list) {
    const key = t.chapter?.trim() || '未标注章节';
    let g = byKey.get(key);
    if (!g) {
      g = { key, label: key, topics: [] };
      byKey.set(key, g);
      groups.push(g);
    }
    g.topics.push(t);
  }
  return groups;
}

/**
 * 局部先修图的分层布局（纯函数）：节点按「学段带 + 年级」分行，
 * 同行按拓扑可达序（传入的 ids 顺序即先修在前的顺序）从左到右排。
 * 返回每个 id 的 {x, y} 网格坐标（单位为格），渲染层再乘格子尺寸。
 *
 * 这是上游 Cytoscape+dagre 分层布局的轻量替代（无图库依赖）：
 * 行 = 年级（先修在下，后继在上），列 = 同年级内顺序。
 */
export interface GraphLayout {
  positions: Record<string, { x: number; y: number; grade: number | null }>;
  rows: { grade: number | null; label: string; ids: string[] }[];
  width: number;
  height: number;
}

/** 图布局需要的最小节点形状：TopicRow 与 TopicEdge 都是它的结构子类型。 */
export interface GraphNodeLike {
  id: string;
  grade: number | null;
}

export function layoutPrereqGraph(
  topic: GraphNodeLike,
  prerequisites: readonly GraphNodeLike[] | null | undefined,
  dependents: readonly GraphNodeLike[] | null | undefined,
): GraphLayout {
  const rowsByGrade = new Map<number | null, string[]>();
  const push = (t: GraphNodeLike) => {
    const key = t.grade ?? null;
    const row = rowsByGrade.get(key) ?? [];
    if (!row.includes(t.id)) row.push(t.id);
    rowsByGrade.set(key, row);
  };
  // 先修在前（低年级），当前点居中，被依赖在后（同年级内保持传入顺序）。
  const pre = prerequisites ?? [];
  const post = dependents ?? [];
  pre.forEach(push);
  push(topic);
  post.forEach(push);
  // 年级升序排列行；null 年级排最后。
  const sortedKeys = [...rowsByGrade.keys()].sort((a, b) => {
    if (a === null) return 1;
    if (b === null) return -1;
    return a - b;
  });
  const positions: Record<string, { x: number; y: number; grade: number | null }> = {};
  const rows: GraphLayout['rows'] = [];
  let maxCols = 0;
  sortedKeys.forEach((gradeKey, y) => {
    const ids = rowsByGrade.get(gradeKey) ?? [];
    maxCols = Math.max(maxCols, ids.length);
    rows.push({
      grade: gradeKey,
      label: gradeKey === null ? '未标注' : gradeLabel(gradeKey),
      ids,
    });
    ids.forEach((id, x) => {
      positions[id] = { x, y, grade: gradeKey };
    });
  });
  return { positions, rows, width: maxCols, height: sortedKeys.length };
}

/** 结果页 headline（上游 renderResult 的中文文案语义）。 */
export function resultHeadline(summary: {
  mode?: string | null;
  weak?: string[] | null;
  shaky?: string[] | null;
  untested?: string[] | null;
  roots?: unknown[] | null;
  targets?: string[] | null;
}): string {
  const weak = summary.weak ?? [];
  const shaky = summary.shaky ?? [];
  const untested = summary.untested ?? [];
  const roots = summary.roots ?? [];
  if (summary.mode === 'screen') {
    if (weak.length === 0 && shaky.length === 0) {
      return untested.length
        ? `已测到的知识点没有发现薄弱点，另有 ${untested.length} 个没测到。`
        : '范围内的知识点都已掌握，没有发现薄弱点。';
    }
    return `薄弱 ${weak.length} 个、不稳 ${shaky.length} 个、未测 ${untested.length} 个。先看「按章汇总」，薄弱的章排在前面。`;
  }
  const targets = summary.targets ?? [];
  if (roots.length === 0 && shaky.length === 0) {
    return targets.length
      ? '目标知识点已掌握，没有发现需要补的先修。'
      : '没有判出薄弱点，但目标尚未全部测到。';
  }
  if (roots.length === 0) {
    return `没有判出薄弱点，但有 ${shaky.length} 个知识点「不稳」（中途答错过、确认题答对），建议巩固。`;
  }
  return `共发现 ${roots.length} 个薄弱根因${shaky.length ? `、${shaky.length} 个不稳的知识点` : ''}，按学习路径从前往后补。`;
}

/** 会话状态标签。 */
export function sessionStatusLabel(status: string | null | undefined): string {
  switch (status) {
    case 'active': return '进行中';
    case 'completed': return '已完成';
    case 'voided': return '已撤销';
    default: return status ?? '';
  }
}

/** 会话模式标签。 */
export function sessionModeLabel(mode: string | null | undefined): string {
  switch (mode) {
    case 'screen': return '整体筛查';
    case 'retest': return '再测';
    default: return '定向诊断';
  }
}

/** 学习路径分组（按章打印视图用）：保持传入顺序（引擎已按年级升序）。 */
export function groupPathByChapter(path: PathItem[] | null | undefined, chapterOf: (id: string) => string): { chapter: string; items: PathItem[] }[] {
  const list = path ?? [];
  const out: { chapter: string; items: PathItem[] }[] = [];
  const byKey = new Map<string, { chapter: string; items: PathItem[] }>();
  for (const item of list) {
    const key = chapterOf(item.id) || '未标注章节';
    let g = byKey.get(key);
    if (!g) {
      g = { chapter: key, items: [] };
      byKey.set(key, g);
      out.push(g);
    }
    g.items.push(item);
  }
  return out;
}

/** 章汇总行的色条分段宽度（百分比，供按章汇总条渲染）。 */
export function chapterBarSegments(row: ChapterSummaryRow): { cls: 'm' | 's' | 'w' | 'u'; pct: number }[] {
  const total = Math.max(row.total ?? 0, 1);
  const segs: { cls: 'm' | 's' | 'w' | 'u'; pct: number }[] = [];
  const add = (cls: 'm' | 's' | 'w' | 'u', n: number | null | undefined) => {
    if (n && n > 0) segs.push({ cls, pct: (n / total) * 100 });
  };
  add('m', row.mastered);
  add('s', row.shaky);
  add('w', row.weak);
  add('u', row.untested);
  return segs;
}

/** 会话列表的默认排序：未完成在前，完成的按时间倒序。 */
export function sortSessions(sessions: DiagnosticSession[] | null | undefined): DiagnosticSession[] {
  return [...(sessions ?? [])].sort((a, b) => {
    const aActive = a.status === 'active' ? 0 : 1;
    const bActive = b.status === 'active' ? 0 : 1;
    if (aActive !== bActive) return aActive - bActive;
    const at = new Date(a.completed_at ?? a.created_at ?? 0).getTime();
    const bt = new Date(b.completed_at ?? b.created_at ?? 0).getTime();
    return bt - at;
  });
}
