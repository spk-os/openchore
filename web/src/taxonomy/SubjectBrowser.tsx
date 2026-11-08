// 学科知识浏览（卡 TX-FE-CODER）：学科 → 册/章目录 → 知识点详情 + 局部先修图。
// 家长端与孩子端共用（视角差异只在入口文案与操作按钮，本页全是只读浏览）。
// 参考上游 explore/app.js 的目录/详情/局部图交互，但数据全部来自服务端
//（不再像上游那样整包 JSON 拉到浏览器）。
// 学科导航清单同样来自服务端（卡 TX-DYNAMIC-SUBJECT-BROWSER-V2-CODER）：
// useSubjects 共享缓存（GET /api/taxonomy/subjects，subjects 表驱动）按服务端
// 顺序渲染，加载/失败/空态就地呈现并可重试，不回退本地硬编码学科清单。

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  type TopicDetailResponse,
  type TopicEdge,
  type TopicRow,
} from './types';
import { taxonomyApi } from './client';
import { useSubjects } from '../hooks/useSubjects';
import {
  difficultyLabel,
  gradeLabel,
  groupByChapter,
  layoutPrereqGraph,
  semesterLabel,
  stageKind,
  stageLabel,
  subjectName,
  typeLabel,
} from './labels';
import { EmptyRow, ErrorRow, LoadingRow, MathPrompt } from './parts';
import { SafeFigure } from './SafeFigure';
import s from './workspace.module.css';

const PAGE_LIMIT = 50;

/** 知识点选择键：真实 API 行的 external_ref（如 cnm_p001）。
 *  行上的本地整数 id（knowledge_tags.id，如 1）只用于 DB 主键，
 *  详情/练习/诊断契约都按 external_ref 寻址，传整数 id 会 404。
 *  external_ref 缺失（家长/AI 自建 tag）的行不可选，返回 null 由调用方禁用。 */
export function topicRef(t: Pick<TopicRow, 'id' | 'external_ref'>): string | null {
  // ?? ''：后端 omitempty 可能丢键（自建 tag），缺 ref 一律按不可选处理，
  // 绝不回退到本地整数 id（那会发出 /topics/1 式 404 请求）。
  const ref = (t.external_ref ?? '').trim();
  return ref ? ref : null;
}

/** 详情接口的邻居边（TopicEdge：边强度 + 对端概要）→ 选择行（TopicRow）。
 *  external_ref 优先取边自带值；详情契约（model.KnowledgeTag 序列化）
 *  保证邻居行必带 external_ref。 */
function edgeToRow(e: TopicEdge): TopicRow {
  return {
    id: e.id,
    external_ref: e.external_ref ?? e.id,
    name: e.name,
    grade: e.grade,
    semester: null,
    domain: null,
    chapter: e.chapter,
  };
}

export interface SubjectBrowserProps {
  /** 初始学科（空 = 等 useSubjects 到达后自动选第一个）。 */
  initialSubject?: string;
  /** 初始选中的知识点 external ref（从盲区/详情链接进入）。 */
  initialTopicRef?: string;
  /** 学科切换回调（宿主同步顶层诊断预设用）；不传则切换只在浏览页内部生效。 */
  onSubjectChange?: (subject: string) => void;
  /** 「诊断这个知识点」入口：由宿主接（孩子端/家长端各自路由）。 */
  onDiagnose?: (topic: TopicRow) => void;
  /** 「练习」入口。 */
  onPractice?: (topic: TopicRow) => void;
}

/** 学科导航条：useSubjects（服务端 subjects 表驱动，全 app 共享缓存）。
 *  按服务端返回顺序渲染；loading/失败/空态就地呈现并可重试，
 *  绝不回退本地硬编码学科清单冒充成功。 */
const SubjectChips: React.FC<{
  subject: string;
  onPick: (key: string) => void;
}> = ({ subject, onPick }) => {
  const { subjects, loading, error, retry } = useSubjects();

  // 失败但仍有旧快照（stale）：chips 照常可用，错误叠在下方可重试。
  if (error && subjects.length === 0) {
    return <ErrorRow error={error} onRetry={retry} test="tx-subject-error" />;
  }
  if (subjects.length === 0) {
    return loading ? <LoadingRow label="加载学科…" /> : <EmptyRow test="tx-subject-empty">还没有任何学科</EmptyRow>;
  }
  return (
    <>
      <div className={s.subjectChips} data-test="tx-subject-chips" role="tablist" aria-label="学科">
        {subjects.map((row) => (
          <button
            key={row.id}
            type="button"
            role="tab"
            aria-selected={subject === row.id}
            className={s.subjectChip}
            data-test={`tx-subject-${row.id}`}
            aria-pressed={subject === row.id}
            onClick={() => onPick(row.id)}
          >
            {/* 服务端学科名为主；缺失时回退 subjectName 的字典/原 key 显示。 */}
            {row.name || subjectName(row.id)}
          </button>
        ))}
      </div>
      {error ? <ErrorRow error={error} onRetry={retry} test="tx-subject-error" /> : null}
    </>
  );
};

/** 学科 → 章节目录 → 知识点列表。 */
const TopicList: React.FC<{
  subject: string;
  onSelect: (ref: string) => void;
}> = ({ subject, onSelect }) => {
  const [q, setQ] = useState('');
  const [topics, setTopics] = useState<TopicRow[] | null>(null);
  const [cursor, setCursor] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [moreError, setMoreError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const load = useCallback(
    async (query: string, cur: string | null, replace: boolean) => {
      setLoading(true);
      setError(null);
      setMoreError(null);
      try {
        const page = await taxonomyApi.topics({ subject, q: query, cursor: cur ?? undefined, limit: PAGE_LIMIT });
        const rows = page.topics ?? [];
        setTopics((prev) => (replace ? rows : [...(prev ?? []), ...rows]));
        setCursor(page.next_cursor ?? null);
      } catch (err) {
        if (replace) {
          // 首页失败：清列表展示错误（旧数据不遮错误）。
          setError(err);
          setTopics(null);
          setCursor(null);
        } else {
          // 加载更多失败：保留已加载列表 + 输入，只在按钮旁提示可重试。
          setMoreError(err);
        }
      } finally {
        setLoading(false);
      }
    },
    [subject],
  );

  // 学科或搜索词变化时重新拉首页（搜索做 300ms 防抖，减少手机端打字请求）。
  useEffect(() => {
    const timer = setTimeout(() => load(q, null, true), q ? 300 : 0);
    return () => clearTimeout(timer);
  }, [load, q]);

  const chapters = useMemo(() => groupByChapter(topics), [topics]);

  if (error && !topics) return <ErrorRow error={error} onRetry={() => load(q, null, true)} />;
  if (!topics) return <LoadingRow />;

  // 搜索框始终渲染（0 结果也保留用户输入，可直接改词重搜）。
  const searchBox = (
    <>
      <input
        className={s.filterInput}
        data-test="tx-topic-search"
        type="search"
        inputMode="search"
        placeholder="搜索知识点名称/描述"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      <div style={{ height: 10 }} />
    </>
  );

  if (topics.length === 0) {
    return (
      <div>
        {searchBox}
        <EmptyRow test="tx-topic-empty">{q ? `没有匹配「${q}」的知识点` : '这个学科还没有知识点'}</EmptyRow>
      </div>
    );
  }

  return (
    <div>
      {searchBox}
      {chapters.map((ch) => (
        <section key={ch.key} className={s.chapterGroup} data-test="tx-chapter">
          <div className={s.chapterSummaryRow}>
            <span className={s.topicName}>{ch.label}</span>
            <span className={s.chapterCount}>{ch.topics.length} 个知识点</span>
          </div>
          <div className={s.chapterBody}>
            {ch.topics.map((t) => {
              const ref = topicRef(t);
              return (
                <button
                  key={ref ?? t.id}
                  type="button"
                  className={s.topicLink}
                  data-test={`tx-topic-${ref ?? t.id}`}
                  disabled={ref == null}
                  onClick={() => ref && onSelect(ref)}
                >
                  <span className={s.topicName}>{t.name}</span>
                  <span className={s.topicMeta}>
                    {gradeLabel(t.grade, subject)}
                    {t.semester ? semesterLabel(t.semester) : ''}
                  </span>
                </button>
              );
            })}
          </div>
        </section>
      ))}
      {cursor && (
        <>
          {moreError ? <ErrorRow error={moreError} onRetry={() => load(q, cursor, false)} test="tx-load-more-error" /> : null}
          <button
            type="button"
            className={s.loadMore}
            data-test="tx-load-more"
            disabled={loading}
            onClick={() => load(q, cursor, false)}
          >
            {loading ? '加载中…' : '加载更多'}
          </button>
        </>
      )}
    </div>
  );
};

/** 局部先修图（纯 SVG，无图库依赖）：先修行在下、当前点中排、被依赖行在上。
 *  邻居行来自详情接口（topicDetail.prerequisites/dependents），元素形状与
 *  TopicRow 同构（external_ref 是后端寻址键），统一以 TopicRow 承载。 */
const PrereqGraph: React.FC<{
  topic: TopicRow;
  prerequisites: TopicRow[];
  dependents: TopicRow[];
  onSelect: (ref: string) => void;
}> = ({ topic, prerequisites, dependents, onSelect }) => {
  // 图内定位与 data-test 一律用 external_ref（与 onSelect 同键）：
  // 本地整数 id 只用于 React key 兜底，绝不进选择路径。
  const nodeKey = (t: TopicRow): string => topicRef(t) ?? t.id;
  const topicKey = nodeKey(topic);
  const layout = useMemo(
    () =>
      layoutPrereqGraph(
        { id: topicKey, grade: topic.grade },
        prerequisites.map((p) => ({ id: nodeKey(p), grade: p.grade })),
        dependents.map((d) => ({ id: nodeKey(d), grade: d.grade })),
      ),
    [topic, prerequisites, dependents, topicKey],
  );
  const positions = layout.positions;
  const CELL_W = 150;
  const CELL_H = 52;
  const NODE_W = 130;
  const NODE_H = 36;
  const PAD = 12;
  const width = Math.max(layout.width, 1) * CELL_W + PAD * 2;
  const height = layout.height * CELL_H + PAD * 2 + 14;

  const stageCls = (grade: number | null) => {
    const kind = stageKind(grade);
    return kind === 'primary' ? s.graphNodeP : kind === 'junior' ? s.graphNodeJ : s.graphNodeH;
  };

  return (
    <div className={s.graphWrap} data-test="tx-prereq-graph">
      <svg
        className={s.graphSvg}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        role="group"
        aria-label={`「${topic.name}」的局部先修图`}
      >
        {layout.rows.map((row, ri) => (
          <text
            key={`row-${ri}`}
            className={s.graphGradeLabel}
            x={PAD - 2}
            y={PAD + ri * CELL_H + NODE_H / 2 + 14}
          >
            {row.label}
          </text>
        ))}
        {/* 先修 → 当前点的连线（实线；上游 soft 边数据在列表里，图上统一实线保持可读） */}
        {prerequisites.map((p) => {
          const from = positions[nodeKey(p)];
          const to = positions[topicKey];
          if (!from || !to) return null;
          const x1 = PAD + from.x * CELL_W + NODE_W / 2;
          const y1 = PAD + from.y * CELL_H + NODE_H + 14;
          const x2 = PAD + to.x * CELL_W + NODE_W / 2;
          const y2 = PAD + to.y * CELL_H + 14;
          return (
            <path
              key={`edge-${p.id}`}
              d={`M ${x1} ${y1} C ${x1} ${(y1 + y2) / 2}, ${x2} ${(y1 + y2) / 2}, ${x2} ${y2}`}
              fill="none"
              stroke="#94a3b8"
              strokeWidth={1.4}
              markerEnd="url(#tx-arrow)"
            />
          );
        })}
        {dependents.map((d) => {
          const from = positions[topicKey];
          const to = positions[nodeKey(d)];
          if (!from || !to) return null;
          const x1 = PAD + from.x * CELL_W + NODE_W / 2;
          const y1 = PAD + from.y * CELL_H + 14;
          const x2 = PAD + to.x * CELL_W + NODE_W / 2;
          const y2 = PAD + to.y * CELL_H + NODE_H + 14;
          return (
            <path
              key={`edge-d-${d.id}`}
              d={`M ${x1} ${y1} C ${x1} ${(y1 + y2) / 2}, ${x2} ${(y1 + y2) / 2}, ${x2} ${y2}`}
              fill="none"
              stroke="#cbd5e1"
              strokeWidth={1.2}
              strokeDasharray="4 3"
            />
          );
        })}
        <defs>
          <marker id="tx-arrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="6" markerHeight="6" orient="auto">
            <path d="M 0 0 L 8 4 L 0 8 z" fill="#94a3b8" />
          </marker>
        </defs>
        {[...prerequisites, topic, ...dependents].map((t) => {
          const key = nodeKey(t);
          const pos = positions[key];
          if (!pos) return null;
          const x = PAD + pos.x * CELL_W;
          const y = PAD + pos.y * CELL_H + 14;
          const current = key === topicKey;
          // 键盘可操作：非当前点是聚焦目标（Enter/Space 走 g 的 onClick），
          // tab 顺序 = 传入顺序（先修 → 当前 → 被依赖）。
          const interactive = !current;
          // 图节点点击只能跳 external_ref（详情契约）；无 ref 的自建 tag 不可点。
          const ref = topicRef(t);
          const clickable = interactive && ref != null;
          return (
            <g
              key={key}
              className={`${s.graphNode} ${stageCls(t.grade ?? null)} ${current ? s.graphNodeCurrent : ''}`}
              onClick={() => clickable && onSelect(ref)}
              onKeyDown={(e) => {
                if (clickable && (e.key === 'Enter' || e.key === ' ') && !e.nativeEvent.isComposing) {
                  e.preventDefault();
                  onSelect(ref);
                }
              }}
              tabIndex={clickable ? 0 : -1}
              role={clickable ? 'button' : undefined}
              aria-label={clickable ? `${t.name}（点击查看详情）` : `${t.name}${current ? '（当前知识点）' : ''}`}
              data-test={`tx-graph-node-${key}`}
            >
              <rect x={x} y={y} width={NODE_W} height={NODE_H} />
              <text className={s.graphNodeText} x={x + NODE_W / 2} y={y + NODE_H / 2}>
                {t.name.length > 10 ? `${t.name.slice(0, 9)}…` : t.name}
              </text>
            </g>
          );
        })}
      </svg>
      <ul className={s.legend}>
        <li>先修（实线，点按跳转）</li>
        <li>被依赖（虚线）</li>
        <li>绿框 = 当前知识点</li>
      </ul>
    </div>
  );
};

/** 知识点详情：元信息/掌握标准/描述/课标/样题/先修/被依赖 + 局部图。 */
const TopicDetailView: React.FC<{
  externalRef: string;
  onBack: () => void;
  onDiagnose?: (topic: TopicRow) => void;
  onPractice?: (topic: TopicRow) => void;
  onSelect: (ref: string) => void;
}> = ({ externalRef, onBack, onDiagnose, onPractice, onSelect }) => {
  const [detail, setDetail] = useState<TopicDetailResponse | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    let cancelled = false;
    setDetail(null);
    setError(null);
    taxonomyApi
      .topicDetail(externalRef)
      .then((d) => {
        if (!cancelled) setDetail(d);
      })
      .catch((err) => {
        if (!cancelled) setError(err);
      });
    return () => {
      cancelled = true;
    };
  }, [externalRef]);

  if (error) return <ErrorRow error={error} onRetry={onBack} test="tx-detail-error" />;
  if (!detail) return <LoadingRow />;

  const topic = detail.topic;
  if (!topic) {
    return <EmptyRow test="tx-detail-missing">找不到这个知识点（可能已被移除）</EmptyRow>;
  }

  const prerequisites = detail.prerequisites ?? [];
  const dependents = detail.dependents ?? [];
  const questions = detail.questions ?? [];
  const standards = detail.standards ?? [];
  const criteria = topic.mastery_criteria ?? [];

  // 详情行已在 topicRef(topic) === externalRef 的请求里返回：诊断/练习
  // 回调拿到的 TopicRow.external_ref 必有值（无 ref 的行进不了详情页）。
  const detailRef = topicRef(topic);

  // 诊断/练习回调必须收到按 external_ref 寻址的行（id = external_ref）：
  // topic 是 model.KnowledgeTag 的序列化行，JSON id 是 DB 自增整数（如 1），
  // 原样传出会让宿主 practicePreset / TopicPracticePanel / PracticeRunner
  // 拿 topic.id 发 GET /api/taxonomy/topics/1 式 404（后端 catalog 按
  // external_ref 寻址）。对照深链路径 detailToTopicRow 的先例：行上放
  // external_ref，不把本机自增整数 id 发给按 external_ref 寻址的接口。
  // detailRef 必有值；理论边缘 null 时 id 回退本地整数串保持旧行为
  //（PrereqGraph 内部 nodeKey = topicRef(t) ?? t.id 定位不受影响）。
  // String(topic.id)：运行时后端 id 是 number，包一层保证 TopicRow.id 的
  // string 类型安全；semester 运行时是 number 属既有类型偏差，本处只透传。
  const detailAsRow: TopicRow = {
    id: detailRef ?? String(topic.id),
    external_ref: detailRef ?? '',
    name: topic.name,
    grade: topic.grade ?? null,
    semester: topic.semester ?? null,
    domain: topic.domain ?? null,
    chapter: topic.chapter ?? null,
    subject: topic.subject ?? null,
    question_count: topic.question_count ?? null,
    description: topic.description ?? null,
    external: topic.external,
  };
  return (
    <div className={s.screen} data-test="tx-topic-detail">
      <button type="button" className={s.btnGhost} data-test="tx-detail-back" onClick={onBack}>
        ← 返回目录
      </button>
      <article className={s.detailCard}>
        <h2 className={s.detailTitle} data-test="tx-detail-name">{topic.name}</h2>
        <div className={s.pills}>
          <span className={`${s.pill} ${stageKind(topic.grade) === 'primary' ? s.pillStageP : stageKind(topic.grade) === 'junior' ? s.pillStageJ : s.pillStageH}`}>
            {stageLabel(topic.grade)}
          </span>
          <span className={s.pill}>{detailRef ?? topic.id}</span>
          {topic.domain && <span className={s.pill}>{topic.domain}</span>}
        </div>
        {(onDiagnose || onPractice) && detailRef != null && (
          <div className={s.actionRow}>
            {onDiagnose && (
              <button
                type="button"
                className={s.btnPrimary}
                data-test="tx-detail-diagnose"
                onClick={() => onDiagnose(detailAsRow)}
              >
                诊断这个知识点
              </button>
            )}
            {onPractice && (
              <button
                type="button"
                className={s.btn}
                data-test="tx-detail-practice"
                onClick={() => onPractice(detailAsRow)}
              >
                练习题
              </button>
            )}
          </div>
        )}
        <dl className={s.metaGrid}>
          <div>
            <dt>年级册次</dt>
            <dd>
              {gradeLabel(topic.grade)}
              {topic.semester ? ` ${semesterLabel(topic.semester)}` : ''}
            </dd>
          </div>
          <div>
            <dt>章节</dt>
            <dd>{topic.chapter || '—'}</dd>
          </div>
          <div>
            <dt>类型</dt>
            <dd>{topic.type || '—'}</dd>
          </div>
          <div>
            <dt>题目数</dt>
            <dd>{questions.length}</dd>
          </div>
        </dl>
        {topic.description && <MathPrompt className={s.desc} text={topic.description} />}
        {topic.root_reason && <p className={s.muted}>{topic.root_reason}</p>}
        {criteria.length > 0 && (
          <>
            <h3 className={s.sectionTitle}>掌握标准</h3>
            <ul className={s.critList}>
              {criteria.map((c, i) => (
                <li key={i}>{c}</li>
              ))}
            </ul>
          </>
        )}
      </article>

      <PrereqGraph
        topic={detailAsRow}
        prerequisites={(detail.prerequisites ?? []).map(edgeToRow)}
        dependents={(detail.dependents ?? []).map(edgeToRow)}
        onSelect={onSelect}
      />

      {questions.length > 0 && (
        <article className={s.detailCard} data-test="tx-detail-questions">
          <h3 className={s.sectionTitle}>样题（{questions.length}）</h3>
          {questions.slice(0, 3).map((q) => (
            <div key={q.id}>
              <div className={s.questionMeta}>
                <span className={s.tag}>{difficultyLabel(q.difficulty)}</span>
                <span className={s.tag}>{typeLabel(q.type)}</span>
              </div>
              <MathPrompt text={q.prompt} />
              <SafeFigure svg={q.figure} label="题目配图" />
              {q.choices && q.choices.length > 0 && (
                <div className={s.choices}>
                  {q.choices.map((c) => (
                    <span key={c.key} className={s.muted}>
                      {c.key}. {c.text}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
        </article>
      )}

      <article className={s.detailCard}>
        <h3 className={s.sectionTitle}>先修知识点（直接 {prerequisites.length} 个）</h3>
        {prerequisites.length === 0 ? (
          <p className={s.muted}>这是起点，没有更早的先修。</p>
        ) : (
          prerequisites.map((p) => {
            const row = edgeToRow(p);
            const ref = topicRef(row);
            return (
              <button
                key={ref ?? row.id}
                type="button"
                className={s.relRow}
                data-test={`tx-prereq-${ref ?? row.id}`}
                disabled={ref == null}
                onClick={() => ref && onSelect(ref)}
              >
                <span className={p.strength === 'soft' ? s.badgeSoft : s.badgeHard}>
                  {p.strength === 'soft' ? '软先修' : '硬先修'}
                </span>
                <span className={s.relName}>{row.name}</span>
                <span className={s.relWhere}>{gradeLabel(row.grade)}</span>
              </button>
            );
          })
        )}
        <h3 className={s.sectionTitle}>被哪些知识点依赖（直接 {dependents.length} 个）</h3>
        {dependents.length === 0 ? (
          <p className={s.muted}>还没有后续知识点依赖它。</p>
        ) : (
          dependents.map((d) => {
            const row = edgeToRow(d);
            const ref = topicRef(row);
            return (
              <button
                key={ref ?? row.id}
                type="button"
                className={s.relRow}
                data-test={`tx-dependent-${ref ?? row.id}`}
                disabled={ref == null}
                onClick={() => ref && onSelect(ref)}
              >
                <span className={d.strength === 'soft' ? s.badgeSoft : s.badgeHard}>
                  {d.strength === 'soft' ? '软依赖' : '硬依赖'}
                </span>
                <span className={s.relName}>{row.name}</span>
                <span className={s.relWhere}>{gradeLabel(row.grade)}</span>
              </button>
            );
          })
        )}
      </article>

      {standards.length > 0 && (
        <article className={s.detailCard}>
          <h3 className={s.sectionTitle}>课标条目（{standards.length}）</h3>
          {standards.map((std) => (
            <div key={std.id} className={s.stdItem}>
              <div className={s.stdId}>
                {std.id} · {std.confidence === 'exact' ? '直接对应' : '推断对应'}
              </div>
              <div>{std.text || '（条目文本缺失）'}</div>
              {std.note && <div className={s.muted}>说明：{std.note}</div>}
            </div>
          ))}
        </article>
      )}
    </div>
  );
};

/**
 * 学科知识浏览工作区：学科选择 → 章节目录 / 详情 + 局部先修图。
 * 家长端与孩子端共用（孩子端只读视角同样安全：无管理操作）。
 *
 * 动态学科（卡 TX-COURSE-DEEPLINK-DYNAMIC-CODER）：
 *  - initialSubject 默认 ''（不再预设 math）：subjects 到达后若当前值不在
 *    服务端列表里则选首项，空列表保持 ''——服务端只有 history/biology 或
 *    空学科时绝不请求/预设数学。
 *  - initialSubject 是受控初值：props 变化（宿主顶层学科切换）时跟随更新。
 *  - onSubjectChange：用户点选学科时上抛（宿主同步顶层诊断预设）。
 */
export const SubjectBrowser: React.FC<SubjectBrowserProps> = ({
  initialSubject = '',
  initialTopicRef,
  onSubjectChange,
  onDiagnose,
  onPractice,
}) => {
  const [subject, setSubject] = useState(initialSubject);
  const [selected, setSelected] = useState<string | null>(initialTopicRef ?? null);
  const { subjects } = useSubjects();

  // 宿主下传的初始学科变化时跟随（顶层与浏览页共用同一学科）。
  useEffect(() => {
    setSubject(initialSubject);
  }, [initialSubject]);

  // 服务端学科到达：当前学科不在列表里（含初始 ''）则选首项；空列表保持 ''。
  useEffect(() => {
    if (subjects.length === 0) return;
    setSubject((cur) => (subjects.some((row) => row.id === cur) ? cur : subjects[0].id));
  }, [subjects]);

  const select = (ref: string) => setSelected(ref);

  // 用户主动点选学科：切换并清空选中（跨学科 external ref 不可达）。
  // 清空只挂在 pick 上、不挂在 subject 变化上：自动选科（'' → 服务端首项）
  // 与宿主 initialSubject 跟随都不是用户操作，绝不能清空由深链
  // initialTopicRef 带入的选中——否则深链详情刚打开就被自动选科冲回目录
  // （DEF-DEEPLINK-1）。学科 chips 只在目录态（selected 为空）渲染，用户
  // pick 时 selected 必为 null，同步清空与旧版「subject 变化即清空」在
  // 用户可感知行为上等价；宿主回传路径（onSubjectChange → initialSubject
  // 变化）只是 pick 的回声，无需重复清空。
  const pick = useCallback((key: string) => {
    setSubject(key);
    setSelected(null);
    onSubjectChange?.(key);
  }, [onSubjectChange]);

  return (
    <div className={s.screen} data-test="tx-browser">
      <div className={s.screenHead}>
        <h2 className={s.title}>{selected ? '知识点详情' : subjectName(subject)}</h2>
        <p className={s.subtitle}>
          {selected ? '局部先修图与课标、样题' : '按册/章浏览知识点，点开看详情与先修'}
        </p>
      </div>
      {!selected && <SubjectChips subject={subject} onPick={pick} />}
      {selected ? (
        <TopicDetailView
          externalRef={selected}
          onBack={() => setSelected(null)}
          onDiagnose={onDiagnose}
          onPractice={onPractice}
          onSelect={select}
        />
      ) : subject ? (
        // 学科未定（''：服务端学科还在路上/为空）时不发目录请求——
        // 加载/失败/空态由 SubjectChips 就地呈现。
        <TopicList subject={subject} onSelect={select} />
      ) : null}
    </div>
  );
};
