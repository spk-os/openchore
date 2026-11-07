// 家长端先修边管理卡（卡 TX-PREREQ-UI-CODER）。
//
// 先修关系是全局内容（不 per-kid）：卡片是「学科 → 知识点 → 边」三段式，
// props 为显式 subject（缺省 '' = 服务端学科到达后自动选第一个，绝不默认
// math）与可选 initialTopicRef。写路径只走 typed client 的两个冻结契约：
//   - taxonomyApi.putPrerequisite（PUT /api/admin/taxonomy/prerequisites）
//     body {topic_id, prerequisite_id, strength, reason}，两个 ref 都是
//     知识点 external_ref（如 cnm_p009），绝不传本地整数主键；管理员新增
//     即 reviewed 由后端语义处理（前端不发 review_status）。
//   - taxonomyApi.reviewPrerequisite（PATCH .../review）body
//     {topic_id, prerequisite_id, review_status}——这里只发 rejected：
//     被驳回的边退出生效图、行保留审计。恢复走表单重新 PUT 同端点边
//     （PUT 会把该行重写为 reviewed）。
// 读路径：topicDetail 的 prerequisites/dependents（当前生效的 hard/soft
// 边）。rejected 边不出现在任何读取端点里——本卡不伪造被驳回边的列表，
// 改判入口只对当前已知（有效）边开放。
//
// 错误约定：409 成环 / 404 未知 ref 或未知边 / 400 自环与参数错误都来自
// 服务端 JSON {error}，经 TaxonomyAPIError 透传，role=alert 呈现；失败时
// 不乐观污染图（只报错，列表保持原样），成功后用响应 effective_edges
// 提示并重新读取 detail 刷新。学科/知识点切换用请求代数作废在飞响应。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { taxonomyApi } from '../../taxonomy/client';
import type {
  PrerequisiteMutationResult,
  PrerequisiteStrength,
  TopicDetailResponse,
  TopicEdge,
  TopicRow,
} from '../../taxonomy/types';
import { useSubjects } from '../../hooks/useSubjects';
import ui from './ui.module.css';

/** 知识点分页大小（与浏览目录同一量级）。 */
const PAGE_LIMIT = 30;
/** 搜索防抖：敲字不逐字符请求。 */
const SEARCH_DEBOUNCE_MS = 300;

/** 行的 external_ref（寻址键）。缺 ref 的行（家长/AI 自建 tag）返回
 *  null：绝不拿本地整数 id 顶替（那会发出 /topics/1 式 404 请求）。
 *  与 SubjectBrowser.topicRef 同语义，这里内联以避免把整条浏览页
 *  依赖链（parts → MathText → katex）拉进家长端管理卡。 */
const rowRef = (t: Pick<TopicRow, 'id' | 'external_ref'>): string | null => {
  const ref = (t.external_ref ?? '').trim();
  return ref ? ref : null;
};

/** 详情邻居边的 external_ref（详情契约 model.KnowledgeTag 序列化必带；
 *  缺失时返回 null，该边不可寻址、改判按钮禁用）。 */
const edgeRef = (e: TopicEdge): string | null => {
  const ref = (e.external_ref ?? '').trim();
  return ref ? ref : null;
};

export interface TaxonomyPrerequisitesCardProps {
  /** 显式学科（服务端 subject key）。缺省 '' = 服务端学科表到达后取
   *  第一项（服务端顺序），空表保持空态——绝不预设 math。 */
  subject?: string;
  /** 初始知识点 external_ref（可选；不在所选学科时同样请求，
   *  详情端点按 ref 寻址、不做学科过滤）。 */
  initialTopicRef?: string;
}

/** 请求代数守卫：慢响应回来时不许覆盖更新的 topic/学科请求结果。 */
function useSeq(): { current: number } {
  return useRef(0);
}

/**
 * 一条已知先修边（可改判行）：topic 的 prerequisites 之一。
 *  ref 是对端知识点的 external_ref；strength 之外的 reason/grade 为
 *  服务端投影原始值（reason 为空串时显示「未记录」）。
 */
interface EdgeLine {
  key: string;
  ref: string;
  name: string;
  strength: string;
  reason: string;
  grade: number | null;
}

/** 详情 prerequisites → 行（null external_ref 的边不进列表：不可寻址
 *  也不可改判，渲染出来只会制造一个点了必 404 的按钮）。 */
function toLines(edges: TopicEdge[] | null | undefined): EdgeLine[] {
  const out: EdgeLine[] = [];
  for (const e of edges ?? []) {
    const ref = edgeRef(e);
    if (!ref) continue;
    out.push({
      key: `${e.id}-${ref}`,
      ref,
      name: e.name,
      strength: e.strength,
      reason: e.reason ?? '',
      grade: e.grade,
    });
  }
  return out;
}

export const TaxonomyPrerequisitesCard: React.FC<TaxonomyPrerequisitesCardProps> = ({
  subject: subjectProp,
  initialTopicRef,
}) => {
  const { t } = useTranslation();
  const { subjects, loading: subjectsLoading, error: subjectsError, retry: retrySubjects } = useSubjects();

  // 学科：显式 prop 优先；否则服务端学科表第一项（服务端顺序，不默认 math）。
  const [subject, setSubject] = useState<string>(subjectProp ?? '');
  // 当前知识点（external_ref）与候选列表（分页 + 搜索）。
  const [topicRef, setTopicRef] = useState<string>(initialTopicRef ?? '');
  const [topicQuery, setTopicQuery] = useState('');
  const [topicRows, setTopicRows] = useState<TopicRow[] | null>(null);
  const [topicsLoading, setTopicsLoading] = useState(false);
  const [topicsError, setTopicsError] = useState<unknown>(null);
  // 详情（当前 topic 的有效 hard/soft 先修与被先修）。
  const [detail, setDetail] = useState<TopicDetailResponse | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailError, setDetailError] = useState<unknown>(null);

  // 新增边表单：先修知识点 external_ref + 强度 + 原因。
  const [prereqRef, setPrereqRef] = useState('');
  const [strength, setStrength] = useState<PrerequisiteStrength>('hard');
  const [reason, setReason] = useState('');
  // 操作反馈：error 服务端可读错误（role=alert）；ok 成功提示（role=status）。
  const [actionError, setActionError] = useState('');
  const [okMsg, setOkMsg] = useState('');
  // busy 防重复：PUT / PATCH / detail 刷新任一在飞时禁用提交。
  const [busy, setBusy] = useState(false);

  const topicsSeq = useSeq();
  const detailSeq = useSeq();

  // props 的学科变化跟随（宿主顶层学科切换）。
  useEffect(() => {
    if (subjectProp == null) return;
    setSubject(subjectProp);
  }, [subjectProp]);

  // 学科默认值：宿主没显式给时以服务端学科表第一项为准（服务端顺序，
  // 不默认 math）；空表保持 ''（空态由学科区呈现）。
  useEffect(() => {
    if (subject) return;
    const first = subjects[0]?.id;
    if (first) setSubject(first);
  }, [subject, subjects]);

  // 学科变化清空选中知识点（跨学科 external ref 不可达）。
  useEffect(() => {
    setTopicRef('');
    setTopicRows(null);
    setTopicsError(null);
    setDetail(null);
    setDetailError(null);
    setActionError('');
    setOkMsg('');
    // 主题代数递增：学科切换时在飞请求全部作废（旧学科慢响应不落地）。
    topicsSeq.current++;
    detailSeq.current++;
    // topicQuery 保留用户输入（同学科内搜索词不属于学科切换状态）。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subject]);

  // 知识点候选列表：学科 + 搜索词（防抖）变化时重拉。
  const loadTopics = useCallback(
    async (subjectKey: string, query: string) => {
      const seq = ++topicsSeq.current;
      setTopicsLoading(true);
      setTopicsError(null);
      try {
        const page = await taxonomyApi.topics({
          subject: subjectKey,
          q: query || undefined,
          limit: PAGE_LIMIT,
        });
        if (seq !== topicsSeq.current) return; // 旧响应作废
        setTopicRows(page.topics ?? []);
      } catch (err) {
        if (seq !== topicsSeq.current) return;
        setTopicRows([]);
        setTopicsError(err);
      } finally {
        if (seq === topicsSeq.current) setTopicsLoading(false);
      }
    },
    // topicsSeq 是 ref，不进依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  useEffect(() => {
    if (!subject) return;
    const timer = setTimeout(() => {
      void loadTopics(subject, topicQuery);
    }, topicQuery ? SEARCH_DEBOUNCE_MS : 0);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subject, topicQuery, loadTopics]);

  // 详情：当前知识点的有效先修/被先修。
  const loadDetail = useCallback(async (ref: string) => {
    const seq = ++detailSeq.current;
    setDetailLoading(true);
    setDetailError(null);
    try {
      const d = await taxonomyApi.topicDetail(ref);
      if (seq !== detailSeq.current) return; // 旧响应作废
      setDetail(d);
    } catch (err) {
      if (seq !== detailSeq.current) return;
      setDetail(null);
      setDetailError(err);
    } finally {
      if (seq === detailSeq.current) setDetailLoading(false);
    }
    // detailSeq 是 ref，不进依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!topicRef) {
      setDetail(null);
      setDetailError(null);
      return;
    }
    void loadDetail(topicRef);
  }, [topicRef, loadDetail]);

  const prereqLines = toLines(detail?.prerequisites);
  const dependentLines = toLines(detail?.dependents);

  /** 提交新增/改写一条边（PUT）。两个 ref 都必须是 external_ref；
   *  提交前本地校验：空 ref / 自环直接提示（服务端同判 400，这里省一次
   *  往返），其余交给服务端（409 成环 / 404 未知 ref / 400 参数错误
   *  的服务端消息 role=alert 透传）。 */
  const submitEdge = async () => {
    const topic = topicRef.trim();
    const prereq = prereqRef.trim();
    setActionError('');
    setOkMsg('');
    if (!topic) {
      setActionError(t('admin.taxonomyTab.prereqPickTopicFirst'));
      return;
    }
    if (!prereq) {
      setActionError(t('admin.taxonomyTab.prereqPickPrereqFirst'));
      return;
    }
    if (topic === prereq) {
      setActionError(t('admin.taxonomyTab.prereqSelfLoop'));
      return;
    }
    if (busy) return;
    setBusy(true);
    try {
      const res: PrerequisiteMutationResult = await taxonomyApi.putPrerequisite({
        topic_id: topic,
        prerequisite_id: prereq,
        strength,
        reason: reason.trim(),
      });
      setOkMsg(t('admin.taxonomyTab.prereqSaved', { count: res.effective_edges }));
      setPrereqRef('');
      setReason('');
      // 成功后重新读取当前 topic 的详情刷新边列表（不乐观拼装）。
      await loadDetail(topic);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  /** 驳回一条已知边（PATCH review_status=rejected）：行保留审计、退出
   *  生效图。失败不污染列表，成功后重读详情。 */
  const rejectEdge = async (line: EdgeLine) => {
    const topic = topicRef.trim();
    if (!topic || busy) return;
    setActionError('');
    setOkMsg('');
    setBusy(true);
    try {
      const res: PrerequisiteMutationResult = await taxonomyApi.reviewPrerequisite({
        topic_id: topic,
        prerequisite_id: line.ref,
        review_status: 'rejected',
      });
      setOkMsg(t('admin.taxonomyTab.prereqRejected', { count: res.effective_edges }));
      await loadDetail(topic);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={ui.card} data-test="tx-prereq-card">
      <div className={ui.sectionHead}>
        <h3 className={ui.sectionTitle}>{t('admin.taxonomyTab.prereqTitle')}</h3>
      </div>
      <p className={ui.sectionDesc}>{t('admin.taxonomyTab.prereqDesc')}</p>

      {/* 学科选择：服务端 subjects 表驱动（useSubjects 共享缓存），服务端
          顺序原样渲染；失败可重试，空表呈现真实空态，绝不回退硬编码清单。 */}
      <div className={ui.formRow} data-test="tx-prereq-subject-row">
        <label className={ui.field}>
          <span className={ui.label}>{t('admin.taxonomyTab.prereqSubject')}</span>
          <select
            className={ui.input}
            data-test="tx-prereq-subject"
            value={subject}
            disabled={subjectsLoading || subjects.length === 0}
            aria-label={t('admin.taxonomyTab.prereqSubject')}
            onChange={(e) => setSubject(e.target.value)}
          >
            {subjects.map((row) => (
              <option key={row.id} value={row.id}>
                {row.name || row.id}
              </option>
            ))}
          </select>
        </label>
      </div>
      {subjectsError != null && subjects.length === 0 && (
        <p className={ui.msgError} role="alert" data-test="tx-prereq-subject-error">
          {subjectsError instanceof Error ? subjectsError.message : String(subjectsError)}
          {' '}
          <button type="button" className={ui.btnGhost} data-test="tx-prereq-subject-retry" onClick={retrySubjects}>
            {t('admin.taxonomyTab.prereqRetry')}
          </button>
        </p>
      )}
      {subjects.length === 0 && !subjectsError && (
        <p className={ui.emptyInline} data-test="tx-prereq-subject-empty">
          {subjectsLoading
            ? t('common.loading')
            : t('admin.taxonomyTab.prereqNoSubjects')}
        </p>
      )}

      {subject && (
        <>
          {/* 知识点选择：搜索 + 候选列表（external_ref 寻址）。 */}
          <div className={ui.formRow} data-test="tx-prereq-topic-row">
            <label className={ui.field}>
              <span className={ui.label}>{t('admin.taxonomyTab.prereqTopic')}</span>
              <input
                className={ui.input}
                type="search"
                data-test="tx-prereq-topic-search"
                value={topicQuery}
                placeholder={t('admin.taxonomyTab.prereqTopicSearchPlaceholder')}
                aria-label={t('admin.taxonomyTab.prereqTopic')}
                onChange={(e) => setTopicQuery(e.target.value)}
              />
            </label>
          </div>
          {topicsLoading && topicRows == null && (
            <p className={ui.emptyInline} role="status" data-test="tx-prereq-topics-loading">
              {t('common.loading')}
            </p>
          )}
          {topicsError != null && (
            <p className={ui.msgError} role="alert" data-test="tx-prereq-topics-error">
              {topicsError instanceof Error ? topicsError.message : String(topicsError)}
              {' '}
              <button
                type="button"
                className={ui.btnGhost}
                data-test="tx-prereq-topics-retry"
                onClick={() => void loadTopics(subject, topicQuery)}
              >
                {t('admin.taxonomyTab.prereqRetry')}
              </button>
            </p>
          )}
          {topicRows != null && topicRows.length === 0 && topicsError == null && (
            <p
              className={ui.emptyInline}
              data-test={topicQuery ? 'tx-prereq-topics-no-match' : 'tx-prereq-topics-empty'}
            >
              {topicQuery
                ? t('admin.taxonomyTab.prereqTopicsNoMatch')
                : t('admin.taxonomyTab.prereqTopicsEmpty')}
            </p>
          )}
          {topicRows != null && topicRows.length > 0 && (
            <div className={ui.list} data-test="tx-prereq-topic-list">
              {topicRows.map((row) => {
                const ref = rowRef(row);
                const selected = ref != null && ref === topicRef;
                return (
                  <button
                    key={ref ?? row.id}
                    type="button"
                    className={`${ui.rowButton} ${selected ? ui.rowMuted : ''}`}
                    data-test={`tx-prereq-topic-${ref ?? row.id}`}
                    aria-pressed={selected}
                    disabled={ref == null}
                    title={ref == null ? t('admin.taxonomyTab.prereqRowNoRef') : undefined}
                    onClick={() => ref && setTopicRef(ref)}
                  >
                    <span className={ui.rowMain}>
                      <span className={ui.rowTitle}>
                        {selected ? '✓ ' : ''}
                        {row.name}
                      </span>
                      <span className={ui.rowMeta}>
                        {ref ? <span>{ref}</span> : null}
                        {row.chapter ? <span>{row.chapter}</span> : null}
                      </span>
                    </span>
                  </button>
                );
              })}
            </div>
          )}

          {/* 当前知识点的有效边。 */}
          {topicRef && (
            <div data-test="tx-prereq-detail">
              <h4 className={ui.eyebrow}>{t('admin.taxonomyTab.prereqCurrentTopic')}</h4>
              <p className={ui.sectionDesc}>
                <code className={ui.code}>{topicRef}</code>
              </p>
              {detailLoading && (
                <p className={ui.emptyInline} role="status" data-test="tx-prereq-detail-loading">
                  {t('common.loading')}
                </p>
              )}
              {detailError != null && (
                <p className={ui.msgError} role="alert" data-test="tx-prereq-detail-error">
                  {detailError instanceof Error ? detailError.message : String(detailError)}
                  {' '}
                  <button
                    type="button"
                    className={ui.btnGhost}
                    data-test="tx-prereq-detail-retry"
                    onClick={() => void loadDetail(topicRef)}
                  >
                    {t('admin.taxonomyTab.prereqRetry')}
                  </button>
                </p>
              )}
              {!detailLoading && detailError == null && (
                <>
                  <h4 className={ui.eyebrow}>{t('admin.taxonomyTab.prereqEdgesTitle')}</h4>
                  {prereqLines.length === 0 ? (
                    <p className={ui.emptyInline} data-test="tx-prereq-edges-empty">
                      {t('admin.taxonomyTab.prereqNoEdges')}
                    </p>
                  ) : (
                    <ul className={ui.list} data-test="tx-prereq-edges">
                      {prereqLines.map((line) => (
                        <li key={line.key} className={ui.row} data-test={`tx-prereq-edge-${line.ref}`}>
                          <span className={ui.rowMain}>
                            <span className={ui.rowTitle}>
                              <span
                                className={line.strength === 'soft' ? ui.badgeOutline : ui.badge}
                                data-test={`tx-prereq-edge-strength-${line.ref}`}
                              >
                                {line.strength === 'soft'
                                  ? t('admin.taxonomyTab.prereqSoft')
                                  : t('admin.taxonomyTab.prereqHard')}
                              </span>
                              {' '}
                              {line.name}
                            </span>
                            <span className={ui.rowMeta}>
                              <span>{line.ref}</span>
                              <span>
                                {t('admin.taxonomyTab.prereqReason')}：{line.reason || t('admin.taxonomyTab.prereqNoReason')}
                              </span>
                            </span>
                          </span>
                          <button
                            type="button"
                            className={ui.btnDanger}
                            data-test={`tx-prereq-reject-${line.ref}`}
                            disabled={busy}
                            title={t('admin.taxonomyTab.prereqRejectTitle')}
                            onClick={() => { void rejectEdge(line); }}
                          >
                            {busy ? t('admin.taxonomyTab.prereqWorking') : t('admin.taxonomyTab.prereqReject')}
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                  <h4 className={ui.eyebrow}>{t('admin.taxonomyTab.prereqDependentsTitle')}</h4>
                  {dependentLines.length === 0 ? (
                    <p className={ui.emptyInline} data-test="tx-prereq-dependents-empty">
                      {t('admin.taxonomyTab.prereqNoDependents')}
                    </p>
                  ) : (
                    <ul className={ui.list} data-test="tx-prereq-dependents">
                      {dependentLines.map((line) => (
                        <li key={line.key} className={ui.row} data-test={`tx-prereq-dependent-${line.ref}`}>
                          <span className={ui.rowMain}>
                            <span className={ui.rowTitle}>
                              <span
                                className={line.strength === 'soft' ? ui.badgeOutline : ui.badge}
                                data-test={`tx-prereq-dependent-strength-${line.ref}`}
                              >
                                {line.strength === 'soft'
                                  ? t('admin.taxonomyTab.prereqSoftDep')
                                  : t('admin.taxonomyTab.prereqHardDep')}
                              </span>
                              {' '}
                              {line.name}
                            </span>
                            <span className={ui.rowMeta}>
                              <span>{line.ref}</span>
                              <span>
                                {t('admin.taxonomyTab.prereqReason')}：{line.reason || t('admin.taxonomyTab.prereqNoReason')}
                              </span>
                            </span>
                          </span>
                        </li>
                      ))}
                    </ul>
                  )}
                </>
              )}
            </div>
          )}

          {/* 新增/改写边表单。 */}
          <div className={ui.inset} data-test="tx-prereq-form">
            <h4 className={ui.eyebrow}>{t('admin.taxonomyTab.prereqFormTitle')}</h4>
            <div className={ui.formRow}>
              <label className={ui.field}>
                <span className={ui.label}>{t('admin.taxonomyTab.prereqPrereqRef')}</span>
                <input
                  className={ui.input}
                  type="text"
                  data-test="tx-prereq-input-prereq"
                  value={prereqRef}
                  placeholder={t('admin.taxonomyTab.prereqPrereqPlaceholder')}
                  aria-label={t('admin.taxonomyTab.prereqPrereqRef')}
                  onChange={(e) => setPrereqRef(e.target.value)}
                  disabled={busy}
                />
              </label>
              <label className={ui.field}>
                <span className={ui.label}>{t('admin.taxonomyTab.prereqStrength')}</span>
                <select
                  className={ui.input}
                  data-test="tx-prereq-strength"
                  value={strength}
                  disabled={busy}
                  aria-label={t('admin.taxonomyTab.prereqStrength')}
                  onChange={(e) => setStrength(e.target.value === 'soft' ? 'soft' : 'hard')}
                >
                  <option value="hard">{t('admin.taxonomyTab.prereqHard')}</option>
                  <option value="soft">{t('admin.taxonomyTab.prereqSoft')}</option>
                </select>
              </label>
            </div>
            <label className={ui.field}>
              <span className={ui.label}>{t('admin.taxonomyTab.prereqReason')}</span>
              <input
                className={ui.input}
                type="text"
                data-test="tx-prereq-input-reason"
                value={reason}
                placeholder={t('admin.taxonomyTab.prereqReasonPlaceholder')}
                aria-label={t('admin.taxonomyTab.prereqReason')}
                onChange={(e) => setReason(e.target.value)}
                disabled={busy}
              />
            </label>
            <button
              type="button"
              className={ui.btnPrimary}
              data-test="tx-prereq-submit"
              disabled={busy || !topicRef}
              title={!topicRef ? t('admin.taxonomyTab.prereqPickTopicFirst') : undefined}
              onClick={() => { void submitEdge(); }}
            >
              {busy ? t('admin.taxonomyTab.prereqWorking') : t('admin.taxonomyTab.prereqSubmit')}
            </button>
          </div>

          {actionError && (
            <p className={ui.msgError} role="alert" data-test="tx-prereq-error">
              {actionError}
            </p>
          )}
          {okMsg && (
            <p className={ui.msgWaiting} role="status" data-test="tx-prereq-ok">
              {okMsg}
            </p>
          )}
        </>
      )}
    </section>
  );
};
