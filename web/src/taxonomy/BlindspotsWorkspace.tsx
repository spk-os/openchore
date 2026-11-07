// 盲区工作区（卡 TX-FE-CODER）：薄弱/不稳知识点清单 + 按章汇总 + 一键再测。
// 孩子端文案「再挑战」，家长端「盲区再测」（FR-8）。
// 数据来自服务端 GET /api/taxonomy/blindspots（诊断 findings 聚合），不是 localStorage。

import React, { useEffect, useMemo, useState } from 'react';
import type { BlindSpotEntry, BlindspotsResponse } from './types';
import { taxonomyApi } from './client';
import { blindspotRef } from './normalize';
import { formatDateTime, gradeLabel, statusLabel, subjectName } from './labels';
import { ChapterBar, EmptyRow, ErrorRow, LoadingRow } from './parts';
import s from './workspace.module.css';

export interface BlindspotsWorkspaceProps {
  userId?: number | null;
  audience: 'kid' | 'parent';
  /** 再测入口：把目标交给诊断工作区（宿主负责路由/状态）。 */
  onRetest: (targets: BlindSpotEntry[]) => void;
  /** 点名称跳详情。 */
  onOpenTopic?: (topicRef: string) => void;
}

interface ChapterBlock {
  chapter: string;
  entries: BlindSpotEntry[];
}

/** 盲区按章分组（保持服务端顺序）。 */
function groupBlindspots(entries: BlindSpotEntry[]): ChapterBlock[] {
  const out: ChapterBlock[] = [];
  const byKey = new Map<string, ChapterBlock>();
  for (const e of entries) {
    const key = e.chapter?.trim() || '未标注章节';
    let g = byKey.get(key);
    if (!g) {
      g = { chapter: key, entries: [] };
      byKey.set(key, g);
      out.push(g);
    }
    g.entries.push(e);
  }
  return out;
}

const EntryRow: React.FC<{
  entry: BlindSpotEntry;
  audience: 'kid' | 'parent';
  onRetest: (targets: BlindSpotEntry[]) => void;
  onOpenTopic?: (topicRef: string) => void;
}> = ({ entry, audience, onRetest, onOpenTopic }) => {
  // ref/id 都缺失的行禁用再测（targets 不进 undefined，见 validTargets 注释）。
  const ref = blindspotRef(entry);
  const retestable = ref != null;
  return (
  <li style={{ display: 'flex', flexDirection: 'column', gap: 4, padding: '8px 0', listStyle: 'none' }}>
    <div style={{ display: 'flex', gap: 6, alignItems: 'center', flexWrap: 'wrap' }}>
      {onOpenTopic && ref ? (
        <button type="button" className={s.relRow} onClick={() => onOpenTopic(ref)} data-test={`tx-blind-${ref}`}>
          {entry.name ?? ref}
        </button>
      ) : (
        <span>{entry.name ?? ref ?? '（缺少知识点信息）'}</span>
      )}
      <span className={`${s.tag} ${entry.status === 'weak' ? s.tagWeak : s.tagShaky}`}>
        {statusLabel(entry.status)}
      </span>
      {entry.root && <span className={`${s.tag} ${s.tagRoot}`}>根因</span>}
      <span className={s.tag}>{subjectName(entry.subject)}</span>
    </div>
    <div className={s.muted}>
      {entry.chapter || ''}
      {entry.chapter ? ' · ' : ''}
      {gradeLabel(entry.grade, entry.subject ?? undefined)}
      {entry.wrong_count ? ` · 答错 ${entry.wrong_count} 次` : ''}
      {entry.last_at ? ` · ${formatDateTime(entry.last_at)}` : ''}
    </div>
    <button
      type="button"
      className={s.btnSmall}
      data-test={`tx-blind-retest-${ref ?? 'none'}`}
      disabled={!retestable}
      title={retestable ? undefined : '这条记录缺少知识点编号，无法再测'}
      onClick={() => retestable && onRetest([entry])}
    >
      {audience === 'kid' ? '再挑战' : '再测'}
    </button>
  </li>
  );
};

/** 盲区工作区（双端复用）。 */
export const BlindspotsWorkspace: React.FC<BlindspotsWorkspaceProps> = ({
  userId,
  audience,
  onRetest,
  onOpenTopic,
}) => {
  const [data, setData] = useState<BlindspotsResponse | null>(null);
  const [error, setError] = useState<unknown>(null);

  useEffect(() => {
    let cancelled = false;
    taxonomyApi
      .blindspots(userId ?? undefined)
      .then((d) => {
        if (!cancelled) setData(d);
      })
      .catch((err) => {
        if (!cancelled) setError(err);
      });
    return () => {
      cancelled = true;
    };
  }, [userId]);

  const weak = useMemo(() => data?.weak ?? [], [data]);
  const shaky = useMemo(() => data?.shaky ?? [], [data]);
  const byChapter = useMemo(() => data?.by_chapter ?? [], [data]);
  const all = useMemo(() => [...weak, ...shaky], [weak, shaky]);

  if (error) return <ErrorRow error={error} onRetry={() => setError(null)} test="tx-blind-error" />;
  if (!data) return <LoadingRow />;

  if (all.length === 0) {
    return (
      <EmptyRow test="tx-blind-empty">
        {audience === 'kid'
          ? '太棒了，暂时没有需要再挑战的知识点。'
          : '还没有盲区。做完一次诊断，薄弱点会汇总在这里。'}
      </EmptyRow>
    );
  }

  // 再测目标防御：知识点引用缺失（ref/id 都为 undefined/null/空串）的行
  // 不进 targets——后端 createDiagnostic 的 targets 只收 external_ref，
  // 混进 undefined 会把整组请求打 400（DEF：盲区 retest targets 不为 undefined）。
  // 真实后端 blindspotRow 的引用字段是 ref，id 仅兼容旧 mock。
  const validTargets = (entries: BlindSpotEntry[]): BlindSpotEntry[] =>
    entries.filter((e) => blindspotRef(e) != null);

  const weakRoots = validTargets(weak.filter((e) => e.root));
  const weakAll = validTargets(weak);
  const shakyAll = validTargets(shaky);
  const blocks = groupBlindspots(all);

  return (
    <div className={s.screen} data-test="tx-blindspots">
      <div className={s.actionRow}>
        {weakRoots.length > 0 && (
          <button type="button" className={s.btnPrimary} data-test="tx-retest-roots" onClick={() => onRetest(weakRoots)}>
            {audience === 'kid' ? `再挑战薄弱根因（${weakRoots.length}）` : `再测薄弱根因（${weakRoots.length}）`}
          </button>
        )}
        {weakAll.length > 0 && weakRoots.length !== weakAll.length && (
          <button type="button" className={s.btn} data-test="tx-retest-weak" onClick={() => onRetest(weakAll)}>
            再测薄弱知识点（{weakAll.length}）
          </button>
        )}
        {shakyAll.length > 0 && (
          <button type="button" className={s.btn} data-test="tx-retest-shaky" onClick={() => onRetest(shakyAll)}>
            再测不稳知识点（{shakyAll.length}）
          </button>
        )}
      </div>
      <p className={s.muted}>
        薄弱 {weak.length} 个、不稳 {shaky.length} 个。同一个知识点测过好几次以最近一次为准；
        {audience === 'kid' ? '点「再挑战」重新过关。' : '点「再测」按该点重新诊断。'}
      </p>

      {byChapter.length > 0 && (
        <article className={s.detailCard} data-test="tx-blind-chapters">
          <h3 className={s.sectionTitle}>按章汇总</h3>
          {byChapter.map((c) => (
            <div key={c.key ?? c.label} style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '6px 0' }}>
              <span className={s.topicName}>{c.label}</span>
              <ChapterBar row={c} />
              <span className={s.muted}>
                薄弱 {c.weak} · 不稳 {c.shaky}
              </span>
            </div>
          ))}
        </article>
      )}

      {blocks.map((block) => (
        <section key={block.chapter} className={s.chapterGroup}>
          <div className={s.chapterSummaryRow}>
            <span className={s.topicName}>{block.chapter}</span>
            <span className={s.chapterCount}>{block.entries.length} 个知识点</span>
          </div>
          <div className={s.chapterBody} style={{ padding: '0 14px 8px' }}>
            <ul style={{ margin: 0, padding: 0 }}>
              {block.entries.map((e) => (
                <EntryRow
                  key={`${blindspotRef(e) ?? e.tag_id ?? 'none'}-${e.status}`}
                  entry={e}
                  audience={audience}
                  onRetest={onRetest}
                  onOpenTopic={onOpenTopic}
                />
              ))}
            </ul>
          </div>
        </section>
      ))}
    </div>
  );
};
