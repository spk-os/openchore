// The parent's mistake-management workbench (L1+): the kid's whole error
// book with subject chips and mastery filters, per-question fix/delete and
// the one-tap correction task. OD prototype parent-analysis.html's
// pa-week-scope / pa-error-strip family is the contract; measures (the
// 034 engine) live in their own tab, this one stays the questions.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { ErrorItem, KnowledgeTag, User } from '../../types';
import { MathText } from '../MathText';
import { Icon } from '../../design';
import ui from './ui.module.css';
import s from './ErrorbookTab.module.css';

/** The five subjects a paper analysis can be tagged with. */
const SUBJECTS = ['math', 'chinese', 'english', 'physics', 'chemistry'] as const;

const subjectLabel = (t: (k: string) => string, subject: string) =>
  SUBJECTS.includes(subject as (typeof SUBJECTS)[number])
    ? t(`admin.analysis.subject_${subject}`)
    : subject;

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

/** One wrong question row: verdict, stem, AI note, and the actions. */
const ItemRow: React.FC<{
  item: ErrorItem;
  onDeleted: (id: number) => void;
  onToast: (msg: string) => void;
}> = ({ item, onDeleted, onToast }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [hasTask, setHasTask] = useState(false);

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
      setHasTask(true);
      onToast(t('admin.errorbookTab.taskCreated'));
    } catch {
      onToast(t('admin.errorbookTab.taskFailed'));
    }
    setBusy(false);
  };

  const tags = (item.tags ?? []).map((x: KnowledgeTag) => x.name);
  return (
    <li className={s.item} data-test={`pe-item-${item.id}`}>
      <button type="button" className={s.itemHead} onClick={() => setOpen(v => !v)} aria-expanded={open}>
        <span className={s.itemVerdict} data-test={`pe-item-${item.id}-cause`}>
          {errorTypeLabel(t, item.error_type)}
        </span>
        <span className={s.itemStem}>{item.question_text || item.ocr_text || '—'}</span>
        <span className={clsx(s.itemMastery, s['itemMastery' + masteryKey(item.mastery_level)])}>
          {t(`admin.errorbookTab.mastery${masteryKey(item.mastery_level).charAt(0).toUpperCase()}${masteryKey(item.mastery_level).slice(1)}`)}
        </span>
        <Icon name="chev" className={clsx(s.itemChev, open && s.itemChevOpen)} />
      </button>
      {open && (
        <div className={s.itemBody}>
          {item.source && (
            <p className={s.itemMeta}>{t('admin.errorbookTab.itemTitle', { subject: subjectLabel(t, item.subject), source: item.source })}</p>
          )}
          {!item.source && (
            <p className={s.itemMeta}>{t('admin.errorbookTab.itemNoSource', { subject: subjectLabel(t, item.subject) })}</p>
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
            <p className={s.itemTags}>{t('admin.analysis.qTags', { tags: tags.join(' · ') })}</p>
          )}
          <div className={s.itemActions}>
            <button type="button" className={ui.btnGhost} onClick={makeTask} disabled={busy || hasTask} data-test="pe-make-task">
              {hasTask ? <><Icon name="check" /> {t('admin.errorbookTab.taskDone')}</> : t('admin.errorbookTab.makeTask')}
            </button>
            <button type="button" className={ui.btnDanger} onClick={del} disabled={busy} data-test="pe-delete">
              {t('admin.errorbookTab.delete')}
            </button>
          </div>
        </div>
      )}
    </li>
  );
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
  const [failed, setFailed] = useState(false);
  const [subject, setSubject] = useState('');
  const [mastery, setMastery] = useState<'' | 0 | 1 | 2>('');
  const [toast, setToast] = useState('');

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);

  useEffect(() => {
    api.users.list().then(list => {
      setUsers(list);
      const first = list.find(u => u.role === 'child');
      if (first) setKidId(first.id);
    }).catch(() => setFailed(true));
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
    (mastery === '' || it.mastery_level === mastery)), [items, subject, mastery]);

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

  const masteryFilters: { key: '' | 0 | 1 | 2; label: string; test: string }[] = [
    { key: '', label: t('admin.errorbookTab.filterAll'), test: 'pe-filter-all' },
    { key: 0, label: t('admin.errorbookTab.filterNew'), test: 'pe-filter-new' },
    { key: 1, label: t('admin.errorbookTab.filterReviewing'), test: 'pe-filter-reviewing' },
    { key: 2, label: t('admin.errorbookTab.filterMastered'), test: 'pe-filter-mastered' },
  ];

  return (
    <div className={ui.page}>
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
              onChange={e => { setKidId(Number(e.target.value)); setSubject(''); setMastery(''); }}
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
          </div>

          {items === null && <p className={ui.emptyInline}>{t('admin.errorbookTab.loading')}</p>}

          {items !== null && shown.length === 0 && !failed && (
            <p className={ui.emptyInline} data-test="pe-empty">{t('admin.errorbookTab.empty')}</p>
          )}

          {items !== null && shown.length > 0 && (
            <ul className={s.list} data-test="pe-list">
              {shown.map(it => <ItemRow key={it.id} item={it} onDeleted={onDeleted} onToast={setToast} />)}
            </ul>
          )}
        </>
      )}
    </div>
  );
};
