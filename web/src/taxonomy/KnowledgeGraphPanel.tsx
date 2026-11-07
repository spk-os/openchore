// 家长端真实知识点邻域图谱（卡 TX-GRAPH-PARENT-UI）。
//
// 数据全部来自 GET /api/taxonomy/graph（真实 DB 有效先修边的无向 1/2 跳
// 展开，served 边保留存储方向），绝不本地造图：
//   - 节点 external_ref 寻址（id 就是 external_ref，不是本地整数主键）；
//   - mastery 是选中孩子的真实 kc_mastery 汇总，null（无记录）与 0 是两个
//     状态，颜色与文字都分开渲染，绝不把 null 伪造成 0；
//   - hard 边实线、soft 边虚线，边上可查看 reason / review_status；
//   - 250 截断（truncated）有可见提示；
//   - 点节点把它设为新 root 并刷新；kidId 为空禁用请求并提示先选孩子。
//
// 布局是纯函数（labels.ts 的 layoutPrereqGraph 同思路的邻域分层）：
// 本文件自持 layoutGraphNeighborhood，行 = 无向 BFS 距 root 的跳数
// （先修行在下、被依赖行在上），行内按 external_ref 排序，确定性渲染。
// 无第三方图库；SVG 元素全部走 React JSX（无 innerHTML / 无危险注入）。

import React, {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import type { GraphEdge, GraphNode, GraphResponse, TopicRow } from './types';
import { taxonomyApi } from './client';
import { useSubjects } from '../hooks/useSubjects';
import { EmptyRow, ErrorRow, LoadingRow } from './parts';
import s from './workspace.module.css';

/** 掌握度阈值（与盲区/诊断一致）：薄弱 < 0.6、不稳 < 0.8、以上已掌握。 */
const MASTERY_WEAK = 0.6;
const MASTERY_SHAKY = 0.8;

/** 邻域分层布局（纯函数）：行 = 无向 BFS 距 root 跳数，行内按 id 排序。
 *  root 永远在第 0 行；没出现在分层里的节点（被截断/脏数据）不渲染。 */
export interface GraphLayoutNode {
  id: string;
  x: number;
  y: number;
}

export function layoutGraphNeighborhood(
  rootId: string,
  nodes: readonly GraphNode[],
  edges: readonly GraphEdge[],
): {
  positions: Record<string, GraphLayoutNode>;
  rows: { depth: number; ids: string[] }[];
  width: number;
  height: number;
} {
  // 无向邻接（服务端已做无向展开，这里只为分层再建一次邻接表）。
  const adj = new Map<string, Set<string>>();
  for (const e of edges) {
    if (!e.topic_id || !e.prerequisite_id || e.topic_id === e.prerequisite_id)
      continue;
    let a = adj.get(e.topic_id);
    if (!a) {
      a = new Set();
      adj.set(e.topic_id, a);
    }
    a.add(e.prerequisite_id);
    let b = adj.get(e.prerequisite_id);
    if (!b) {
      b = new Set();
      adj.set(e.prerequisite_id, b);
    }
    b.add(e.topic_id);
  }
  // BFS 分层：root 第 0 层，逐层向外；同层按 external_ref 排序（确定性）。
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const depthOf = new Map<string, number>([[rootId, 0]]);
  let layer = [rootId];
  let depth = 0;
  const rows: { depth: number; ids: string[] }[] = [
    { depth: 0, ids: [rootId] },
  ];
  while (layer.length > 0) {
    depth += 1;
    const next = new Set<string>();
    for (const cur of layer) {
      for (const nb of adj.get(cur) ?? []) {
        if (depthOf.has(nb) || !byId.has(nb)) continue;
        depthOf.set(nb, depth);
        next.add(nb);
      }
    }
    if (next.size === 0) break;
    const ids = [...next].sort();
    rows.push({ depth, ids });
    layer = ids;
  }
  const positions: Record<string, GraphLayoutNode> = {};
  let width = 0;
  rows.forEach((row, y) => {
    width = Math.max(width, row.ids.length);
    row.ids.forEach((id, x) => {
      positions[id] = { id, x, y };
    });
  });
  return { positions, rows, width, height: rows.length };
}

/** mastery 的四态分类：null（无记录）与 0 必须区分。 */
export type MasteryKind = 'none' | 'weak' | 'shaky' | 'mastered';

export function masteryKind(mastery: number | null | undefined): MasteryKind {
  if (mastery == null) return 'none';
  if (mastery < MASTERY_WEAK) return 'weak';
  if (mastery < MASTERY_SHAKY) return 'shaky';
  return 'mastered';
}

/** 节点掌握度的文字 + 数值（null 显示「未测」，0 显示真实 0）。 */
export function masteryLabel(mastery: number | null | undefined): string {
  const kind = masteryKind(mastery);
  if (kind === 'none') return '未测（无掌握度记录）';
  const pct = Math.round(mastery! * 100);
  const zh = kind === 'weak' ? '薄弱' : kind === 'shaky' ? '不稳' : '已掌握';
  return `${zh} ${pct}%`;
}

/** SVG 节点内的短文字（长版会溢出 132px 节点框，完整版进 aria/图例）。 */
export function masteryShortLabel(mastery: number | null | undefined): string {
  const kind = masteryKind(mastery);
  if (kind === 'none') return '未测';
  const pct = Math.round(mastery! * 100);
  return `${kind === 'mastered' ? '掌握' : kind === 'shaky' ? '不稳' : '薄弱'} ${pct}%`;
}

const MASTERY_CLS: Record<MasteryKind, string> = {
  none: s.graphMasteryNone,
  weak: s.graphMasteryWeak,
  shaky: s.graphMasteryShaky,
  mastered: s.graphMasteryMastered,
};

export interface KnowledgeGraphPanelProps {
  /** 选中的孩子（服务端 user_id）：admin 必须显式指定解析目标；
   *  null 时禁用请求并提示先选孩子。 */
  userId: number | null;
  /** 初始 root（external_ref）；缺省由服务端学科表 + 真实知识点
   *  列表解析（取所选学科的首条 external_ref），绝不写死数学锚点。 */
  initialRoot?: string;
  /** 初始学科（服务端 subject key）；与 initialRoot 同时缺省时取
   *  服务端学科表的第一项。 */
  initialSubject?: string;
}

/** root 输入的提交防抖：敲字不逐字符请求，停顿后才发（D4）。 */
const ROOT_DEBOUNCE_MS = 300;

/**
 * 家长端邻域图谱面板：学科/root/深度选择 → SVG 分层图 + 边详情 +
 * 掌握度图例 + domain 折叠分组（折叠时对应节点从图上隐藏）。
 */
export const KnowledgeGraphPanel: React.FC<KnowledgeGraphPanelProps> = ({
  userId,
  initialRoot,
  initialSubject,
}) => {
  const {
    subjects,
    loading: subjectsLoading,
    error: subjectsError,
    retry: retrySubjects,
  } = useSubjects();

  // 学科：显式 prop 优先；否则取服务端学科表第一项（不默认 math）。
  const [subject, setSubject] = useState<string>(initialSubject ?? '');
  // root 三来源：initialRoot prop（宿主注入）、用户输入（防抖提交）、
  // 自动解析（所选学科的首条 external_ref）。
  const [rootInput, setRootInput] = useState<string>(initialRoot ?? '');
  const [root, setRoot] = useState<string>(initialRoot ?? '');
  const [depth, setDepth] = useState<1 | 2>(1);
  const [data, setData] = useState<GraphResponse | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [openEdge, setOpenEdge] = useState<string | null>(null);
  const [collapsedDomains, setCollapsedDomains] = useState<Set<string>>(
    new Set(),
  );
  /** 自动解析 root 的过程（先学科后知识点列表）也算加载的一部分。 */
  const [resolving, setResolving] = useState(false);
  const [resolveError, setResolveError] = useState<unknown>(null);
  // 请求代数：慢响应回来时不许覆盖更新的 root/depth 请求结果。
  const reqSeq = useRef(0);
  // root 自动解析的代数：学科切换时上一学科的在途解析不得覆盖新学科。
  const resolveSeq = useRef(0);

  // D3：initialRoot prop 变化同步进本地状态（宿主后续改值也生效）。
  useEffect(() => {
    if (initialRoot == null) return;
    setRoot(initialRoot);
    setRootInput(initialRoot);
  }, [initialRoot]);

  // 学科默认值：宿主没显式给时，以服务端学科表第一项为准（服务端顺序，
  // 不默认 math）。学科表到达/变化都重算。
  useEffect(() => {
    if (subject) return;
    const first = subjects[0]?.id;
    if (first) setSubject(first);
  }, [subject, subjects]);

  const load = useCallback(
    async (
      rootRef: string,
      d: 1 | 2,
      uid: number | null,
      subjectFilter: string,
    ) => {
      // admin 契约：必须显式 user_id；没选孩子不发请求（不伪造目标）。
      if (uid == null) return;
      // D2：参数变化即清旧数据，新响应到达前不拿旧图冒充新中心/新孩子。
      setData(null);
      const seq = ++reqSeq.current;
      setLoading(true);
      setError(null);
      try {
        const res = await taxonomyApi.graph({
          root: rootRef,
          depth: d,
          // 自动解析出的 root 才带学科过滤（两者必然同源一致）；
          // 手动输入的 root 不带——用户可能跨学科导航，过滤矛盾会 400。
          ...(subjectFilter ? { subject: subjectFilter } : {}),
          user_id: uid,
        });
        if (seq !== reqSeq.current) return; // 旧响应作废
        setData(res);
        setOpenEdge(null);
      } catch (err) {
        if (seq !== reqSeq.current) return;
        setError(err);
      } finally {
        if (seq === reqSeq.current) setLoading(false);
      }
    },
    // rootAutoRef 是唯一真值源：root 是否自动解析（决定 subject 过滤
    // 是否随请求发送）。ref 不进依赖，避免 rootAuto 变化重复触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  /** 自动解析 root：取所选学科的首条 external_ref（真实知识点，
   *  绝不写死锚点）。学科没有知识点 → null（呈现空态，不发假请求）。 */
  const resolveRoot = useCallback(
    async (subjectKey: string): Promise<string | null> => {
      const seq = ++resolveSeq.current;
      const page = await taxonomyApi.topics({ subject: subjectKey, limit: 1 });
      if (seq !== resolveSeq.current) return null;
      for (const row of page.topics ?? []) {
        const ref = (row.external_ref ?? '').trim();
        if (ref) return ref;
      }
      return null;
    },
    [],
  );

  // root 来源分派：有 root（宿主注入/用户输入/点节点）直接请求；
  // 没有 root 但有学科 → 解析该学科首条真实知识点；学科表也没到
  // → 等。学科/root/depth/userId 变化都重跑（userId null→有值也拉）。
  useEffect(() => {
    if (userId == null) return;
    if (root) {
      void load(root, depth, userId, rootAutoRef.current ? subject : '');
      return;
    }
    if (!subject) return;
    let alive = true;
    setResolving(true);
    setResolveError(null);
    // 自动解析出的 root 天然属于该学科：rootAuto 置真，请求带过滤。
    resolveRoot(subject)
      .then((ref) => {
        if (!alive || ref == null) return;
        rootAutoRef.current = true;
        setRoot(ref);
        setRootInput(ref);
      })
      .catch((err: unknown) => {
        if (alive) setResolveError(err);
      })
      .finally(() => {
        if (alive) setResolving(false);
      });
    return () => {
      alive = false;
    };
  }, [userId, root, subject, depth, load, resolveRoot]);

  // root 是否自动解析而来：自动解析的请求带学科过滤，手动改的不带。
  // rootAutoRef 是唯一真值源；rerender 计数器只为让 UI 随 ref 同步刷新。
  const rootAutoRef = useRef(initialRoot == null);
  const [, setRootAuto] = useState(initialRoot == null);
  // root 输入防抖计时器（D4）：卸载/重输时清掉，不留悬挂 setState。
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    const t = timerRef;
    return () => {
      if (t.current != null) clearTimeout(t.current);
    };
  }, []);

  const nodes = useMemo(() => data?.nodes ?? [], [data]);
  const edges = useMemo(() => data?.edges ?? [], [data]);

  // domain 折叠：折叠的 domain 节点不渲染（其边也不渲染），
  // root 自身永远显示（它是当前视角，不参与折叠）。
  const domains = useMemo(() => {
    const m = new Map<string, number>();
    for (const n of nodes) {
      const key = n.domain || '未标注领域';
      m.set(key, (m.get(key) ?? 0) + 1);
    }
    return [...m.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [nodes]);

  const visibleIds = useMemo(() => {
    const set = new Set<string>();
    for (const n of nodes) {
      const key = n.domain || '未标注领域';
      if (n.id === root || !collapsedDomains.has(key)) set.add(n.id);
    }
    return set;
  }, [nodes, collapsedDomains, root]);

  const visNodes = useMemo(
    () => nodes.filter((n) => visibleIds.has(n.id)),
    [nodes, visibleIds],
  );
  const visEdges = useMemo(
    () =>
      edges.filter(
        (e) => visibleIds.has(e.topic_id) && visibleIds.has(e.prerequisite_id),
      ),
    [edges, visibleIds],
  );

  const layout = useMemo(
    () => layoutGraphNeighborhood(root, visNodes, visEdges),
    [root, visNodes, visEdges],
  );
  const nodeById = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);

  const toggleDomain = (domain: string) => {
    setCollapsedDomains((prev) => {
      const next = new Set(prev);
      if (next.has(domain)) next.delete(domain);
      else next.add(domain);
      return next;
    });
  };

  // 点击节点 = 把它设为新 root 并刷新（external_ref 寻址；即时请求）。
  const pickRoot = (ref: string) => {
    if (ref === root) return;
    rootAutoRef.current = false;
    setRootAuto(false);
    setRoot(ref);
    setRootInput(ref);
  };

  // 没选孩子：面板可见但禁用请求，给出明确指引。
  if (userId == null) {
    return (
      <section className={s.detailCard} data-test="tx-graph-panel">
        <div className={s.screenHead}>
          <h3 className={s.detailTitle}>知识点邻域图谱</h3>
        </div>
        <EmptyRow test="tx-graph-no-kid">
          请先在上方选择孩子，再看 TA 的知识点邻域与掌握度。
        </EmptyRow>
      </section>
    );
  }

  const CELL_W = 150;
  const CELL_H = 64;
  const NODE_W = 132;
  const NODE_H = 44;
  const PAD = 14;
  const width = Math.max(layout.width, 1) * CELL_W + PAD * 2;
  const height = layout.height * CELL_H + PAD * 2 + 12;

  const edgeKey = (e: GraphEdge) => `${e.topic_id}->${e.prerequisite_id}`;

  return (
    <section className={s.detailCard} data-test="tx-graph-panel">
      <div className={s.screenHead}>
        <div>
          <h3 className={s.detailTitle}>知识点邻域图谱</h3>
          <p className={s.subtitle}>
            真实先修邻域：hard 实线 / soft 虚线，点节点把它设为新的中心
          </p>
        </div>
      </div>

      <div className={s.filters} data-test="tx-graph-controls">
        <label className={s.filterInput} style={{ maxWidth: 220 }}>
          <span
            className={s.muted}
            style={{ display: 'block', marginBottom: 2 }}
          >
            学科
          </span>
          <select
            className={s.filterSelect}
            data-test="tx-graph-subject-select"
            value={subject}
            onChange={(e) => {
              // 换学科：清 root 走自动解析（该学科首条真实知识点），
              // 旧图立即清空（D2），解析带 subject 过滤请求。
              const next = e.target.value;
              if (next === subject) return;
              setSubject(next);
              rootAutoRef.current = true;
              setRootAuto(true);
              setRoot('');
              setRootInput('');
            }}
            aria-label="学科"
          >
            {subjects.map((row) => (
              <option key={row.id} value={row.id}>
                {row.name || row.id}
              </option>
            ))}
          </select>
        </label>
        <label className={s.filterInput} style={{ maxWidth: 220 }}>
          <span
            className={s.muted}
            style={{ display: 'block', marginBottom: 2 }}
          >
            中心知识点（external_ref）
          </span>
          <input
            className={s.filterInput}
            data-test="tx-graph-root-input"
            type="search"
            value={rootInput}
            onChange={(e) => {
              // D4：输入只进本地框，停顿 ROOT_DEBOUNCE_MS 后才提交为
              // 新 root 触发请求（点节点换 root 保持即时）。
              const v = e.target.value.trim();
              setRootInput(e.target.value);
              if (timerRef.current != null) {
                clearTimeout(timerRef.current);
                timerRef.current = null;
              }
              timerRef.current = setTimeout(() => {
                // 手动输入的 root 不再带学科过滤（可能跨学科导航）。
                rootAutoRef.current = false;
                setRootAuto(false);
                setRoot(v);
              }, ROOT_DEBOUNCE_MS);
            }}
            placeholder="如 cnm_p001"
            aria-label="中心知识点 external ref"
          />
        </label>
        <label>
          <span
            className={s.muted}
            style={{ display: 'block', marginBottom: 2 }}
          >
            展开深度
          </span>
          <select
            className={s.filterSelect}
            data-test="tx-graph-depth"
            value={depth}
            onChange={(e) => setDepth(Number(e.target.value) === 2 ? 2 : 1)}
            aria-label="展开深度"
          >
            <option value={1}>1 跳（直接先修/被依赖）</option>
            <option value={2}>2 跳</option>
          </select>
        </label>
        <button
          type="button"
          className={s.btn}
          data-test="tx-graph-refresh"
          disabled={loading || !root || resolving}
          onClick={() =>
            void load(root, depth, userId, rootAutoRef.current ? subject : '')
          }
        >
          {loading ? '加载中…' : '刷新'}
        </button>
      </div>

      {domains.length > 1 && (
        <div className={s.pills} data-test="tx-graph-domains">
          {domains.map(([domain, count]) => {
            const collapsed = collapsedDomains.has(domain);
            return (
              <button
                key={domain}
                type="button"
                className={`${s.pill} ${collapsed ? '' : s.graphDomainOn}`}
                data-test={`tx-graph-domain-${domain}`}
                aria-pressed={!collapsed}
                onClick={() => toggleDomain(domain)}
              >
                {collapsed ? '▸' : '▾'} {domain}（{count}）
              </button>
            );
          })}
        </div>
      )}

      {error ? (
        <ErrorRow
          error={error}
          onRetry={() =>
            void load(root, depth, userId, rootAutoRef.current ? subject : '')
          }
          test="tx-graph-error"
        />
      ) : resolveError ? (
        <ErrorRow
          error={resolveError}
          onRetry={() => {
            // 解析失败重试：清错误后重跑解析（root 保持空，效应器重拉）。
            setResolveError(null);
            setRoot('');
          }}
          test="tx-graph-error"
        />
      ) : !data ? (
        loading || resolving ? (
          <LoadingRow label="加载邻域…" />
        ) : subjects.length === 0 ? (
          subjectsLoading ? (
            <LoadingRow label="加载学科…" />
          ) : subjectsError ? (
            <ErrorRow
              error={subjectsError}
              onRetry={retrySubjects}
              test="tx-graph-error"
            />
          ) : (
            <EmptyRow test="tx-graph-empty">
              还没有任何学科，无法定位中心知识点。
            </EmptyRow>
          )
        ) : subject && !root ? (
          <EmptyRow test="tx-graph-empty">
            这个学科还没有知识点，无法定位中心知识点。
          </EmptyRow>
        ) : (
          <EmptyRow test="tx-graph-empty">
            还没有图谱数据，点「刷新」拉取。
          </EmptyRow>
        )
      ) : (
        <>
          {data.truncated && (
            <p
              className={s.notice}
              data-test="tx-graph-truncated"
              role="status"
            >
              邻域太大，服务端按知识点编号截断到 250
              个节点——当前显示的不是完整邻域，可缩小深度或换一个中心点。
            </p>
          )}
          {visNodes.length === 0 ? (
            <EmptyRow test="tx-graph-empty">
              这个中心点没有可显示的邻域节点。
            </EmptyRow>
          ) : (
            <div className={s.graphWrap} data-test="tx-graph-canvas">
              <svg
                className={s.graphSvg}
                width={width}
                height={height}
                viewBox={`0 0 ${width} ${height}`}
                role="group"
                aria-label={`以 ${nodeById.get(root)?.name ?? root} 为中心的知识点邻域图`}
              >
                <defs>
                  <marker
                    id="txg-arrow"
                    viewBox="0 0 8 8"
                    refX="7"
                    refY="4"
                    markerWidth="6"
                    markerHeight="6"
                    orient="auto"
                  >
                    <path d="M 0 0 L 8 4 L 0 8 z" fill="#94a3b8" />
                  </marker>
                </defs>
                {layout.rows.map((row) => (
                  <text
                    key={`row-${row.depth}`}
                    className={s.graphGradeLabel}
                    x={PAD - 2}
                    y={PAD + row.depth * CELL_H + NODE_H / 2 + 4}
                  >
                    {row.depth === 0 ? '中心' : `${row.depth} 跳`}
                  </text>
                ))}
                {visEdges.map((e) => {
                  const from = layout.positions[e.prerequisite_id];
                  const to = layout.positions[e.topic_id];
                  if (!from || !to) return null;
                  const x1 = PAD + from.x * CELL_W + NODE_W / 2;
                  const y1 = PAD + from.y * CELL_H + NODE_H / 2;
                  const x2 = PAD + to.x * CELL_W + NODE_W / 2;
                  const y2 = PAD + to.y * CELL_H + NODE_H / 2;
                  const hard = e.strength === 'hard';
                  return (
                    <path
                      key={edgeKey(e)}
                      d={`M ${x1} ${y1} C ${x1} ${(y1 + y2) / 2}, ${x2} ${(y1 + y2) / 2}, ${x2} ${y2}`}
                      fill="none"
                      stroke={hard ? '#64748b' : '#a78bfa'}
                      strokeWidth={hard ? 1.6 : 1.2}
                      strokeDasharray={hard ? undefined : '5 4'}
                      markerEnd="url(#txg-arrow)"
                      data-test={`tx-graph-edge-${e.topic_id}-${e.prerequisite_id}`}
                    />
                  );
                })}
                {visNodes.map((n) => {
                  const pos = layout.positions[n.id];
                  if (!pos) return null;
                  const x = PAD + pos.x * CELL_W;
                  const y = PAD + pos.y * CELL_H;
                  const isRoot = n.id === root;
                  const kind = masteryKind(n.mastery);
                  return (
                    <g
                      key={n.id}
                      className={`${s.graphNode} ${isRoot ? s.graphNodeCurrent : ''} ${MASTERY_CLS[kind]}`}
                      onClick={() => pickRoot(n.id)}
                      onKeyDown={(ev) => {
                        if (
                          (ev.key === 'Enter' || ev.key === ' ') &&
                          !ev.nativeEvent.isComposing
                        ) {
                          ev.preventDefault();
                          pickRoot(n.id);
                        }
                      }}
                      tabIndex={isRoot ? -1 : 0}
                      role={isRoot ? undefined : 'button'}
                      aria-label={
                        isRoot
                          ? `${n.name}（当前中心）`
                          : `${n.name}，${masteryLabel(n.mastery)}，点按设为新中心`
                      }
                      data-test={`tx-graph-node-${n.id}`}
                    >
                      <rect x={x} y={y} width={NODE_W} height={NODE_H} />
                      <text
                        className={s.graphNodeText}
                        x={x + NODE_W / 2}
                        y={y + 16}
                      >
                        {n.name.length > 9 ? `${n.name.slice(0, 8)}…` : n.name}
                      </text>
                      <text
                        className={s.graphMasteryText}
                        x={x + NODE_W / 2}
                        y={y + 32}
                      >
                        {masteryShortLabel(n.mastery)}
                      </text>
                    </g>
                  );
                })}
              </svg>
              <ul className={s.legend}>
                <li>硬先修（hard）= 实线</li>
                <li>软先修（soft）= 虚线</li>
                <li>箭头从先修指向后继</li>
                <li>绿框 = 当前中心（点按即换）</li>
              </ul>
            </div>
          )}

          <div className={s.pills} data-test="tx-graph-legend">
            <span className={`${s.pill} ${s.graphMasteryMastered}`}>
              已掌握 ≥ 80%
            </span>
            <span className={`${s.pill} ${s.graphMasteryShaky}`}>
              不稳 60–79%
            </span>
            <span className={`${s.pill} ${s.graphMasteryWeak}`}>
              薄弱 &lt; 60%
            </span>
            <span className={`${s.pill} ${s.graphMasteryNone}`}>
              未测（null，无掌握度记录）
            </span>
          </div>

          {visEdges.length > 0 && (
            <div>
              <h4 className={s.sectionTitle}>
                边详情（{visEdges.length} 条，点按展开原因）
              </h4>
              {visEdges.map((e) => {
                const key = edgeKey(e);
                const open = openEdge === key;
                const hard = e.strength === 'hard';
                return (
                  <div
                    key={key}
                    data-test={`tx-graph-edge-row-${e.topic_id}-${e.prerequisite_id}`}
                  >
                    <button
                      type="button"
                      className={s.relRow}
                      aria-expanded={open}
                      onClick={() => setOpenEdge(open ? null : key)}
                    >
                      <span className={hard ? s.badgeHard : s.badgeSoft}>
                        {hard ? '硬先修' : '软先修'}
                      </span>
                      <span className={s.relName}>
                        {nodeById.get(e.prerequisite_id)?.name ??
                          e.prerequisite_id}
                        <span className={s.graphArrowText}> → </span>
                        {nodeById.get(e.topic_id)?.name ?? e.topic_id}
                      </span>
                      <span className={s.relWhere}>
                        {e.review_status === 'reviewed'
                          ? '已人工复核'
                          : '机器导入'}
                      </span>
                    </button>
                    {open && (
                      <div
                        className={s.feedback}
                        data-test={`tx-graph-edge-detail-${e.topic_id}-${e.prerequisite_id}`}
                      >
                        <div>
                          <b>
                            {nodeById.get(e.prerequisite_id)?.name ??
                              e.prerequisite_id}
                          </b>{' '}
                          是{' '}
                          <b>{nodeById.get(e.topic_id)?.name ?? e.topic_id}</b>{' '}
                          的{hard ? '硬' : '软'}先修。
                        </div>
                        <div className={s.reasonLine}>
                          原因：{e.reason || '（未记录）'}
                        </div>
                        <div className={s.reasonLine}>
                          复核状态：
                          {e.review_status === 'reviewed'
                            ? '已人工复核（reviewed）'
                            : '机器导入（machine）'}
                        </div>
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </>
      )}
    </section>
  );
};

/** 把邻域节点转成 TopicRow 形状（供宿主把节点接入其他工作区时复用；
 *  本面板自身只按 external_ref 寻址，不使用本地整数 id）。 */
export function graphNodeToTopicRow(n: GraphNode): TopicRow {
  return {
    id: n.id,
    external_ref: n.id,
    name: n.name,
    grade: n.grade,
    semester: null,
    domain: n.domain || null,
    chapter: n.chapter || null,
    subject: n.subject,
  };
}
