// The parent's mistake-management workbench (L1+): the kid's whole error
// book with subject chips and mastery filters, per-question fix/delete and
// the one-tap correction task. OD prototype parent-analysis.html's
// pa-week-scope / pa-error-strip family is the contract; measures (the
// 034 engine) live in their own tab, this one stays the questions.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import { taxonomyApi } from '../../taxonomy/client';
import type { Chore, ErrorItem, KnowledgeTag, User } from '../../types';
import { subjectOptionLabel } from '../../subjects';
import { MathText } from '../MathText';
import { Icon } from '../../design';
import ui from './ui.module.css';
import s from './ErrorbookTab.module.css';

const subjectLabel = (t: (k: string) => string, subject: string) =>
  subjectOptionLabel(t, subject, subject);

const errorTypeLabel = (t: (k: string) => string, type?: string) => {
  switch (type) {
    case 'calculation': return t('admin.analysis.errorTypeCalculation');
    case 'concept': return t('admin.analysis.errorTypeConcept');
    case 'reading': return t('admin.analysis.errorTypeReading');
    case 'method': return t('admin.analysis.errorTypeMethod');
    default: return t('admin.analysis.errorTypeUnknown');
  }
};

const masteryKey = (m: number) => m === 2 ? 'mastered' : m === 1 ? 'reviewing' : 'new';

// Mirrors the server's maxErrorItemKnowledgeTags (internal/api/error_items.go):
// 50 ids cap one *selection* — the picker stops offering more before the PUT
// can 400. It is NOT a cap on the candidate pool (D1): every page of the
// subject's system tags loads and stays selectable.
const MAX_TAGS = 50;

// The system-tag candidate page size: BrowseTopics serves at most 200 rows
// (limit > 200 is normalized to 50 server-side). D1: the picker follows
// next_cursor until it is exhausted — a subject's tags all arrive, not just
// the first 200.
const TOPIC_PAGE_LIMIT = 200;

// D1 pagination fuse: 50 pages × 200 rows = 10k candidates, far beyond any
// real subject (the widest pack ships ~3.1k). A cursor that still yields a
// next page past this is a broken server response — surfaced as a retryable
// load error, never a silent truncation and never a spin loop.
const MAX_TOPIC_PAGES = 50;

/** Fetch every page of one subject's system tags (D1).
 *
 * Order-preserving, deduped by id. Terminal conditions:
 *  - a null/empty next_cursor ends the walk normally;
 *  - a repeated cursor (server loop) or the MAX_TOPIC_PAGES fuse throws —
 *    both surface as the picker's role=alert load error with a retry, so a
 *    broken response is never silently truncated nor fetched forever.
 */
const loadAllTopicPages = async (subject: string): Promise<KnowledgeTag[]> => {
  const rows: KnowledgeTag[] = [];
  const seenIds = new Set<number>();
  const seenCursors = new Set<string>();
  let cursor: string | undefined;
  for (let page = 0; ; page++) {
    if (page >= MAX_TOPIC_PAGES) throw new Error('topic pagination did not terminate');
    const resp = await taxonomyApi.topics({
      subject,
      limit: TOPIC_PAGE_LIMIT,
      ...(cursor ? { cursor } : {}),
    });
    for (const row of resp.topics ?? []) {
      const id = Number(row.id);
      // Skip unparsable ids and repeats; keep the first (page) occurrence.
      if (!Number.isFinite(id) || seenIds.has(id)) continue;
      seenIds.add(id);
      rows.push({
        id,
        name: row.name,
        subject: row.subject ?? subject,
        sort_order: 0,
        is_system: true,
      });
    }
    const next = resp.next_cursor ?? null;
    if (!next) return rows;
    if (seenCursors.has(next)) throw new Error('repeated pagination cursor');
    seenCursors.add(next);
    cursor = next;
  }
};

/** The same-subject knowledge tags of one error item, editable in place.
 *
 * D2 state lift: the selection (picked), the in-flight save (busy) and the
 * last save failure (saveError) live in ItemRow, so collapsing the row
 * mid-save (the header stays clickable — the row header is the parent's
 * navigation, not part of the editor) and re-expanding restores the exact
 * editing state instead of resetting to a stale initial and allowing a
 * second PUT. The picker keeps only its own load state
 * (candidates/loadError): a remount simply refetches.
 *
 * The candidate pool is the union of:
 *  - every page of taxonomyApi.topics({subject}) — the system tags of the
 *    item's *current* subject (BrowseTopics only serves is_system rows;
 *    parent/AI custom tags never appear there), and
 *  - the item's own current tags (custom ones included) so an existing link
 *    stays visible and re-submittable.
 *
 * Saving lives in ItemRow and sends knowledge_tag_ids only ([] clears,
 * non-empty replaces) — the server re-reads and returns the fresh item, and
 * ItemRow adopts that response (its hydrated tags), never a locally stitched
 * list. A late response after the picker unmounted only ever touches the
 * owning row's lifted state and the id-keyed onUpdated — never another
 * item's.
 */
const KnowledgeTagPicker: React.FC<{
  item: ErrorItem;
  /** The lifted selection (owned by ItemRow). */
  picked: Set<number>;
  /** The lifted in-flight save (owned by ItemRow). */
  busy: boolean;
  /** The lifted last-save failure (owned by ItemRow; re-shown on remount). */
  saveError: boolean;
  onToggleTag: (id: number) => void;
  onClear: () => void;
  onCancel: () => void;
  onSave: () => void;
}> = ({ item, picked, busy, saveError, onToggleTag, onClear, onCancel, onSave }) => {
  const { t } = useTranslation();
  // System-tag candidates from BrowseTopics (null = not loaded yet).
  const [candidates, setCandidates] = useState<KnowledgeTag[] | null>(null);
  const [loadError, setLoadError] = useState(false);

  // Candidates load per mount, keyed by the item's final subject — the only
  // pool the server will accept on save.
  useEffect(() => {
    let cancelled = false;
    setLoadError(false);
    loadAllTopicPages(item.subject)
      .then(rows => {
        if (!cancelled) setCandidates(rows);
      })
      .catch(() => {
        if (!cancelled) setLoadError(true);
      });
    return () => { cancelled = true; };
  }, [item.subject]);

  const retry = () => {
    setLoadError(false);
    loadAllTopicPages(item.subject)
      .then(rows => setCandidates(rows))
      .catch(() => setLoadError(true));
  };

  const full = picked.size >= MAX_TAGS;
  // Current custom tags stay selectable even though BrowseTopics never
  // lists them; cross-subject legacy tags cannot legally be re-submitted
  // (the server validates against the item's subject) and stay out.
  const ownCustom = (item.tags ?? []).filter((x: KnowledgeTag) => x.subject === item.subject);
  const pool = useMemo(() => {
    const byId = new Map<number, KnowledgeTag>();
    for (const tag of [...ownCustom, ...(candidates ?? [])]) byId.set(tag.id, tag);
    return [...byId.values()].sort((a, b) => a.name.localeCompare(b.name));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [candidates, item.tags]);

  return (
    <div className={s.tagEditor} data-test={`pe-tags-editor-${item.id}`}>
      <p className={s.itemTags}>
        {t('admin.errorbookTab.tagPickerTitle', { defaultValue: '知识点标签' })}
        {picked.size > 0 && ` (${picked.size}/${MAX_TAGS})`}
      </p>
      {loadError ? (
        <p className={ui.msgError} role="alert" data-test={`pe-tags-load-error-${item.id}`}>
          {t('admin.errorbookTab.tagsLoadFailed', { defaultValue: '标签列表加载失败' })}
          {' '}
          <button type="button" className={ui.btnGhost} onClick={retry}>
            {t('admin.kidsStatusTab.refreshLabel')}
          </button>
        </p>
      ) : candidates === null ? (
        <p className={ui.emptyInline}>{t('common.loading')}</p>
      ) : (
        <>
          <div className={ui.chips} role="group">
            {pool.map(tag => {
              const on = picked.has(tag.id);
              return (
                <button
                  key={tag.id}
                  type="button"
                  className={clsx(ui.chip, on && ui.chipOn)}
                  aria-pressed={on}
                  disabled={!on && full}
                  data-test={`pe-tag-${item.id}-${tag.id}`}
                  onClick={() => onToggleTag(tag.id)}
                >
                  {tag.name}
                </button>
              );
            })}
            {pool.length === 0 && (
              <span className={ui.emptyInline} data-test={`pe-tags-empty-${item.id}`}>
                {t('admin.errorbookTab.tagsEmpty', { defaultValue: '该学科还没有可选的知识点标签' })}
              </span>
            )}
          </div>
          <div className={s.itemActions}>
            <button type="button" className={ui.btnGhost} onClick={onClear} disabled={busy || picked.size === 0} data-test={`pe-tags-clear-${item.id}`}>
              {t('admin.errorbookTab.tagsClear', { defaultValue: '全部清空' })}
            </button>
            <button type="button" className={ui.btnGhost} onClick={onCancel} disabled={busy} data-test={`pe-tags-cancel-${item.id}`}>
              {t('admin.apiTokens.cancel')}
            </button>
            <button type="button" className={ui.btnPrimary} onClick={onSave} disabled={busy} data-test={`pe-tags-save-${item.id}`}>
              {t('common.save')}
            </button>
          </div>
        </>
      )}
      {saveError && (
        <p className={ui.msgError} role="alert" data-test={`pe-tags-save-error-${item.id}`}>
          {t('admin.analysis.saveFailed')}
        </p>
      )}
    </div>
  );
};

/** One wrong question row: verdict, stem, AI note, and the actions. */
const ItemRow: React.FC<{
  item: ErrorItem;
  /** 错题→订正任务的现成关联：external_id 为 error_item:<id> 的任务，
   *  由 ErrorbookTab 拉一次 chores 建映射传入（live 真实存在）。 */
  linkedTask?: { id: number; title: string };
  onDeleted: (id: number) => void;
  onUpdated: (fresh: ErrorItem) => void;
  onToast: (msg: string) => void;
}> = ({ item, linkedTask, onDeleted, onUpdated, onToast }) => {
  const { t } = useTranslation();
  const { i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [madeTask, setMadeTask] = useState(false);
  const [editTags, setEditTags] = useState(false);
  // D2：标签编辑的关键状态上提到行级——保存 in-flight 中收起/重开
  // 行（picker 卸载再挂载）不丢选择、不重置 initial、不丢失败告警、
  // 更不会允许第二次 PUT。卸载掉的 picker 只是丢了候选缓存，重开重拉。
  const [pickedTagIds, setPickedTagIds] = useState<Set<number>>(() =>
    new Set((item.tags ?? []).filter((x: KnowledgeTag) => x.subject === item.subject).map((x: KnowledgeTag) => x.id)));
  const [tagSaving, setTagSaving] = useState(false);
  const [tagSaveError, setTagSaveError] = useState(false);
  // 挂载即知已有订正任务（外部关联优先），不再只看本轮点击。
  const hasTask = madeTask || !!linkedTask;

  // 服务端 tags 变化（保存成功 onUpdated 写回，或外部刷新）同步行级
  // 选中基线；仅在不在编辑/保存中时收敛，避免覆盖正在进行的编辑。
  useEffect(() => {
    if (editTags || tagSaving) return;
    setPickedTagIds(new Set(
      (item.tags ?? []).filter((x: KnowledgeTag) => x.subject === item.subject).map((x: KnowledgeTag) => x.id)));
  }, [item.tags, item.subject, editTags, tagSaving]);

  // 任务②（第三次反馈）：日期恒显示——errored_date 没有时回退
  // created_at（live 恒有），家长不展开也能看出「哪天进本的」。
  const dateStr = item.errored_date || item.created_at.slice(0, 10);
  const dateLabel = dateStr
    ? new Date(`${dateStr}T00:00:00`).toLocaleDateString(i18n.language, { month: 'numeric', day: 'numeric' })
    : '';

  const del = async () => {
    if (busy) return;
    if (!window.confirm(t('admin.errorbookTab.deleteConfirm'))) return;
    setBusy(true);
    try {
      await api.admin.deleteErrorItem(item.id);
      onDeleted(item.id);
      onToast(t('admin.errorbookTab.deleted'));
    } catch {
      onToast(t('admin.errorbookTab.deleteFailed'));
    }
    setBusy(false);
  };

  const makeTask = async () => {
    if (busy || hasTask) return;
    setBusy(true);
    try {
      await api.admin.createCorrectionTask(item.id);
      setMadeTask(true);
      onToast(t('admin.errorbookTab.taskCreated'));
    } catch {
      onToast(t('admin.errorbookTab.taskFailed'));
    }
    setBusy(false);
  };

  // ---- 标签编辑（状态在行级，picker 卸载也不丢） ----
  const toggleTag = (id: number) => {
    setTagSaveError(false);
    setPickedTagIds(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else if (next.size < MAX_TAGS) next.add(id); // full: the chip is disabled anyway
      return next;
    });
  };

  const clearTags = () => {
    setTagSaveError(false);
    setPickedTagIds(new Set());
  };

  const cancelTags = () => {
    // 恢复服务端值：直接回到 item.tags 的同学科集合。
    setTagSaveError(false);
    setPickedTagIds(new Set(
      (item.tags ?? []).filter((x: KnowledgeTag) => x.subject === item.subject).map((x: KnowledgeTag) => x.id)));
    setEditTags(false);
  };

  const saveTags = async () => {
    if (tagSaving) return;
    setTagSaving(true);
    setTagSaveError(false);
    try {
      // Only knowledge_tag_ids rides the PUT: every other field keeps its
      // stored value server-side, so the FSRS/fix state is never touched.
      const fresh = await api.admin.updateErrorItem(item.id, { knowledge_tag_ids: [...pickedTagIds] });
      // 迟到响应防线（D2）：行已被删/换人时 items 里没有本行，这里
      // 什么也不写；onUpdated 按 id 定位，永不波及别的 item。
      onUpdated(fresh);
      setEditTags(false);
      onToast(t('admin.settingsTab.learning.saveSuccess'));
    } catch {
      setTagSaveError(true);
    }
    setTagSaving(false);
  };

  const tags = (item.tags ?? []).map((x: KnowledgeTag) => x.name);
  const toggleEditTags = () => {
    setEditTags(v => !v);
  };
  return (
    <li className={s.item} data-test={`pe-item-${item.id}`}>
      {/* D2 说明：行头（展开/收起导航）在保存中保持可点——编辑状态已
          全部上提到本行（pickedTagIds/tagSaving/tagSaveError），保存中
          收起只是卸载候选面板：选择不重置、in-flight PUT 不中断、失败
          告警重开仍在、迟到响应只按 id 写回本行（见 onUpdated），永不
          波及别的 item。真正的防重入闸门是 saveTags 的 tagSaving 早退
          与取消/清空按钮的 disabled（鼠标键盘同挡）。 */}
      <button type="button" className={s.itemHead} onClick={() => setOpen(v => !v)} aria-expanded={open}>
        <span className={s.itemVerdict} data-test={`pe-item-${item.id}-cause`}>
          {errorTypeLabel(t, item.error_type)}
        </span>
        <span className={s.itemStem}>{item.question_text || item.ocr_text || '—'}</span>
        {dateLabel && <span className={s.itemDate} data-test={`pe-item-${item.id}-date`}>{dateLabel}</span>}
        <span className={clsx(s.itemMastery, s['itemMastery' + masteryKey(item.mastery_level)])}>
          {t(`admin.errorbookTab.mastery${masteryKey(item.mastery_level).charAt(0).toUpperCase()}${masteryKey(item.mastery_level).slice(1)}`)}
        </span>
        <Icon name="chev" className={clsx(s.itemChev, open && s.itemChevOpen)} />
      </button>
      {open && (
        <div className={s.itemBody}>
          {/* 任务②（第三次反馈）：「来历」行恒渲染——上轮依赖的
              errored_date/chore_title 在 live 存量全 NULL，条件永不成立，
              所以用户「说了两次都没改」。本轮用 live 真实存在的数据：
              日期（errored_date 回退 created_at）+ 关联订正任务
              （external_id=error_item:<id>）+ 来源 + 知识点，一个都不少。 */}
          <p className={s.itemMeta} data-test={`pe-item-${item.id}-origin`}>
            {t('admin.errorbookTab.itemOrigin', { subject: subjectLabel(t, item.subject), date: dateStr })}
          </p>
          {linkedTask && (
            <p className={s.itemMeta} data-test={`pe-item-${item.id}-task`}>
              {t('admin.errorbookTab.itemLinkedTask', { title: linkedTask.title })}
            </p>
          )}
          {item.source && (
            <p className={s.itemMeta}>{t('admin.errorbookTab.itemSource', { source: item.source })}</p>
          )}
          {item.wrong_answer_text && (
            <p className={s.itemWrote}>{t('admin.errorbookTab.fixWrong')}：<MathText text={item.wrong_answer_text} /></p>
          )}
          {item.answer_text && (
            <p className={s.itemAnswer}>{t('admin.errorbookTab.fixAnswer')}：<MathText text={item.answer_text} /></p>
          )}
          {item.mistake_analysis && (
            <p className={s.itemMistake} data-test={`pe-item-${item.id}-analysis`}><MathText text={item.mistake_analysis} /></p>
          )}
          {tags.length > 0 && (
            <p className={s.itemTags} data-test={`pe-item-${item.id}-tagline`}>
              {t('admin.analysis.qTags', { tags: tags.join(' · ') })}
            </p>
          )}
          {/* The knowledge tags are visible *and* editable: the picker
              below loads the same-subject candidates (all pages, D1) and
              saves through updateErrorItem(knowledge_tag_ids) adopting the
              server's fresh item (onUpdated) — no locally stitched tag
              list. The editing state lives at row level (D2), so
              collapsing the row mid-save neither loses the in-flight PUT
              nor allows a second one. */}
          {editTags ? (
            <KnowledgeTagPicker
              item={item}
              picked={pickedTagIds}
              busy={tagSaving}
              saveError={tagSaveError}
              onToggleTag={toggleTag}
              onClear={clearTags}
              onCancel={cancelTags}
              onSave={saveTags}
            />
          ) : (
            <div className={s.itemActions}>
              <button type="button" className={ui.btnGhost} onClick={toggleEditTags} data-test={`pe-tags-edit-${item.id}`}>
                {t('admin.errorbookTab.editTags', { defaultValue: '编辑知识点标签' })}
              </button>
            </div>
          )}
          {!editTags && (
            <div className={s.itemActions}>
              <button type="button" className={ui.btnGhost} onClick={makeTask} disabled={busy || hasTask} data-test="pe-make-task">
                {hasTask ? <><Icon name="check" /> {t('admin.errorbookTab.taskDone')}</> : t('admin.errorbookTab.makeTask')}
              </button>
              <button type="button" className={ui.btnDanger} onClick={del} disabled={busy} data-test="pe-delete">
                {t('admin.errorbookTab.delete')}
              </button>
            </div>
          )}
        </div>
      )}
    </li>
  );
};

/** 任务②：错题→订正任务映射——拉一次 chores，按 external_id
 *  (error_item:<id>) 建错题 id → 任务行映射。 */
const buildTaskMap = (chores: Chore[]): Map<number, { id: number; title: string }> => {
  const m = new Map<number, { id: number; title: string }>();
  for (const c of chores) {
    const ext = c.external_id ?? '';
    const hit = /^error_item:(\d+)$/.exec(ext);
    if (hit) m.set(Number(hit[1]), { id: c.id, title: c.title });
  }
  return m;
};

/** The week-scope strip (OD pa-week-scope): this week's wrong / fixed / manual. */
const WeekScope: React.FC<{ items: ErrorItem[] }> = ({ items }) => {
  const { t } = useTranslation();
  const weekAgo = Date.now() - 7 * 86400000;
  const week = items.filter(it => new Date(it.created_at).getTime() >= weekAgo);
  const fixed = week.filter(it => it.mastery_level === 2).length;
  const manual = week.filter(it => it.mistake_status === 'unknown').length;
  return (
    <div className={s.weekScope} data-test="pa-week-scope" role="status">
      <span className={s.weekStep}>{t('admin.errorbookTab.weekScopeItems', { count: week.length })}</span>
      <span className={s.weekLine} aria-hidden />
      <span className={s.weekStep}>{t('admin.errorbookTab.weekScopeFixed', { count: fixed })}</span>
      <span className={s.weekLine} aria-hidden />
      <span className={s.weekStep}>{t('admin.errorbookTab.weekScopeManual', { count: manual })}</span>
    </div>
  );
};

export const ErrorbookTab: React.FC = () => {
  const { t } = useTranslation();
  const [users, setUsers] = useState<User[]>([]);
  const [kidId, setKidId] = useState<number | null>(null);
  const [items, setItems] = useState<ErrorItem[] | null>(null);
  // 任务②：一次拉全量任务建 external_id→错题映射，错题行据此显示
  // 「已关联订正任务」（live 真实存在的 14/16 号任务就是证据）。
  const [taskMap, setTaskMap] = useState<Map<number, { id: number; title: string }>>(new Map());
  const [failed, setFailed] = useState(false);
  const [subject, setSubject] = useState('');
  const [mastery, setMastery] = useState<'' | 0 | 1 | 2>('');
  const [errorType, setErrorType] = useState('');
  const [toast, setToast] = useState('');

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);

  useEffect(() => {
    api.users.list().then(list => {
      setUsers(list);
      const first = list.find(u => u.role === 'child');
      if (first) setKidId(first.id);
    }).catch(() => setFailed(true));
  }, []);

  useEffect(() => {
    api.chores.list().then(rows => setTaskMap(buildTaskMap(rows))).catch(() => setTaskMap(new Map()));
  }, []);

  const load = useCallback(async () => {
    if (kidId == null) return;
    setFailed(false);
    try {
      setItems(await api.errorItems.list({ user_id: kidId, limit: 500 }));
    } catch {
      setFailed(true);
      setItems([]);
    }
  }, [kidId]);

  useEffect(() => { load(); }, [load]);

  const subjects = useMemo(() => {
    const counts = new Map<string, number>();
    for (const it of items ?? []) counts.set(it.subject, (counts.get(it.subject) ?? 0) + 1);
    return [...counts.entries()].sort((a, b) => b[1] - a[1]);
  }, [items]);

  const shown = useMemo(() => (items ?? []).filter(it =>
    (!subject || it.subject === subject) &&
    (mastery === '' || it.mastery_level === mastery) &&
    (!errorType || it.error_type === errorType)), [items, subject, mastery, errorType]);

  const stats = useMemo(() => {
    const all = items ?? [];
    return {
      total: all.length,
      open: all.filter(it => it.mastery_level !== 2).length,
      mastered: all.filter(it => it.mastery_level === 2).length,
      unknown: all.filter(it => it.mistake_status === 'unknown').length,
    };
  }, [items]);

  const onDeleted = useCallback((id: number) => {
    setItems(prev => (prev ?? []).filter(it => it.id !== id));
  }, []);

  // A saved tag swap adopts the server's response: updateErrorItem re-reads
  // and returns the fresh item (tags hydrated), so the row shows the stored
  // truth, never a locally stitched list. The id-keyed map write (D2): a
  // late response for a removed/filtered row simply finds no match and is
  // dropped — it can never land on another item.
  const onUpdated = useCallback((fresh: ErrorItem) => {
    setItems(prev => (prev ?? []).map(it => it.id === fresh.id ? fresh : it));
  }, []);

  const masteryFilters: { key: '' | 0 | 1 | 2; label: string; test: string }[] = [
    { key: '', label: t('admin.errorbookTab.filterAll'), test: 'pe-filter-all' },
    { key: 0, label: t('admin.errorbookTab.filterNew'), test: 'pe-filter-new' },
    { key: 1, label: t('admin.errorbookTab.filterReviewing'), test: 'pe-filter-reviewing' },
    { key: 2, label: t('admin.errorbookTab.filterMastered'), test: 'pe-filter-mastered' },
  ];

  // The 讯飞 three-layer filter's third axis: error cause. Filter by
  // "方法有误/概念未掌握" before an exam; the four types the analyses tag.
  const errorTypeFilters: { key: string; label: string; test: string }[] = [
    { key: '', label: t('admin.errorbookTab.filterAll'), test: 'pe-filter-etype-all' },
    { key: 'calculation', label: t('admin.analysis.errorTypeCalculation'), test: 'pe-filter-etype-calculation' },
    { key: 'concept', label: t('admin.analysis.errorTypeConcept'), test: 'pe-filter-etype-concept' },
    { key: 'reading', label: t('admin.analysis.errorTypeReading'), test: 'pe-filter-etype-reading' },
    { key: 'method', label: t('admin.analysis.errorTypeMethod'), test: 'pe-filter-etype-method' },
  ];

  return (
    <div className={ui.page} data-test="admin-errorbook-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>📕 {t('admin.errorbookTab.heading')}</h2>
          <p className={ui.pageSub}>{t('admin.errorbookTab.subtitle')}</p>
        </div>
        {kids.length > 1 && (
          <label className={ui.field}>
            <select
              className={ui.input}
              value={kidId ?? ''}
              onChange={e => { setKidId(Number(e.target.value)); setSubject(''); setMastery(''); setErrorType(''); }}
              aria-label={t('admin.errorbookTab.pickKid')}
              data-test="pe-kid-select"
            >
              {kids.map(k => <option key={k.id} value={k.id}>{k.name}</option>)}
            </select>
          </label>
        )}
      </div>

      {kids.length === 0 && <p className={ui.emptyInline}>{t('admin.errorbookTab.noKids')}</p>}

      {kids.length > 0 && (
        <>
          <div className={s.statRow}>
            <span className={s.stat}><b>{stats.total}</b>{t('admin.errorbookTab.totalStat')}</span>
            <span className={s.stat}><b>{stats.open}</b>{t('admin.errorbookTab.openStat')}</span>
            <span className={s.stat}><b>{stats.mastered}</b>{t('admin.errorbookTab.masteredStat')}</span>
            <span className={s.stat}><b>{stats.unknown}</b>{t('admin.errorbookTab.unknownStat')}</span>
          </div>

          <WeekScope items={items ?? []} />

          {failed && <p className={ui.msgError} role="alert">{t('admin.errorbookTab.loadFailed')}</p>}
          {toast && <p className={ui.msgWaiting} role="status">{toast}</p>}

          <div className={s.filters} role="group" data-test="pe-filters">
            <div className={ui.chips} role="group">
              <button
                type="button"
                className={clsx(ui.chip, !subject && ui.chipOn)}
                aria-pressed={!subject}
                onClick={() => setSubject('')}
                data-test="pe-filter-subject-all"
              >
                {t('admin.errorbookTab.filterAll')}
              </button>
              {subjects.map(([subj, count]) => (
                <button
                  key={subj}
                  type="button"
                  className={clsx(ui.chip, subject === subj && ui.chipOn)}
                  aria-pressed={subject === subj}
                  onClick={() => setSubject(subj)}
                  data-test={`pe-filter-subject-${subj}`}
                >
                  {subjectLabel(t, subj)} {count}
                </button>
              ))}
            </div>
            <div className={ui.chips} role="group">
              {masteryFilters.map(f => (
                <button
                  key={f.test}
                  type="button"
                  className={clsx(ui.chip, mastery === f.key && ui.chipOn)}
                  aria-pressed={mastery === f.key}
                  onClick={() => setMastery(f.key)}
                  data-test={f.test}
                >
                  {f.label}
                </button>
              ))}
            </div>
            <div className={ui.chips} role="group">
              {errorTypeFilters.map(f => (
                <button
                  key={f.test}
                  type="button"
                  className={clsx(ui.chip, errorType === f.key && ui.chipOn)}
                  aria-pressed={errorType === f.key}
                  onClick={() => setErrorType(f.key)}
                  data-test={f.test}
                >
                  {f.label}
                </button>
              ))}
            </div>
          </div>

          {items === null && <p className={ui.emptyInline}>{t('admin.errorbookTab.loading')}</p>}

          {items !== null && shown.length === 0 && !failed && (
            <p className={ui.emptyInline} data-test="pe-empty">{t('admin.errorbookTab.empty')}</p>
          )}

          {items !== null && shown.length > 0 && (
            <ul className={s.list} data-test="pe-list">
              {shown.map(it => <ItemRow key={it.id} item={it} linkedTask={taskMap.get(it.id)} onDeleted={onDeleted} onUpdated={onUpdated} onToast={setToast} />)}
            </ul>
          )}
        </>
      )}
    </div>
  );
};
