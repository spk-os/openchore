// The measures workbench (034): every learning measure for one kid —
// auto-distilled from paper analyses, hand-written by the parent, or
// AI-strengthened — grouped by status, with the AI-strengthen button the
// user asked for (「家长也可以针对措施通过 AI 加强」). OD prototype
// parent-measures family; this tab is where the rules live and learn.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { LearningMeasure, User } from '../../types';
import ui from './ui.module.css';
import s from './ErrorbookTab.module.css';

const ERROR_TYPES = ['calculation', 'concept', 'reading', 'method'] as const;

const errorTypeLabel = (t: (k: string) => string, type: string) =>
  ERROR_TYPES.includes(type as (typeof ERROR_TYPES)[number])
    ? t(`admin.analysis.errorType${type.charAt(0).toUpperCase()}${type.slice(1)}`)
    : type;

/** The origin chip: where this measure came from. */
const OriginChip: React.FC<{ origin: LearningMeasure['origin'] }> = ({ origin }) => {
  const { t } = useTranslation();
  const key = origin === 'ai_strengthen' ? 'originStrengthen' : origin === 'manual' ? 'originManual' : 'originAnalysis';
  return <span className={clsx(s.itemMastery, origin === 'analysis' && s.itemMasteryNew)}>{t(`admin.measuresTab.${key}`)}</span>;
};

/** 任务②：措施的「为什么有这条」——创建日期 + 依据（哪份报告/哪个知识点
 *  蒸馏来的，origin_id 指向 paper_analyses）。 */
const originWhy = (t: ReturnType<typeof useTranslation>['t'], m: LearningMeasure): string => {
  const created = new Date(m.created_at).toLocaleDateString();
  if (m.origin === 'manual') return t('admin.measuresTab.whyManual', { date: created });
  if (m.origin === 'ai_strengthen') return t('admin.measuresTab.whyStrengthen', { date: created });
  return t('admin.measuresTab.whyAnalysis', { date: created, id: m.origin_id ?? 0 });
};

/** One measure card: title, detail, error types, and the actions. */
const MeasureCard: React.FC<{
  m: LearningMeasure;
  onToast: (msg: string) => void;
  onChanged: () => void;
}> = ({ m, onToast, onChanged }) => {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<'' | 'strengthen' | 'status' | 'delete'>('');

  const run = async (what: '' | 'strengthen' | 'status' | 'delete', p: Promise<unknown>, ok: string, fail: string) => {
    if (busy) return;
    setBusy(what);
    try {
      await p;
      if (ok) onToast(ok);
      onChanged();
    } catch {
      onToast(fail);
    }
    setBusy('');
  };

  const strengthen = () => run('strengthen',
    api.admin.strengthenMeasure(m.id),
    t('admin.measuresTab.strengthened'),
    t('admin.measuresTab.strengthenFailed'));

  // No dedicated success toast per status flip — the card moving groups is
  // the feedback; only failures speak.
  const setStatus = (status: 'active' | 'paused' | 'done') =>
    run('status', api.admin.setMeasureStatus(m.id, status), '', t('admin.measuresTab.statusFailed'));

  const del = () => {
    if (!window.confirm(t('admin.measuresTab.deleteConfirm'))) return;
    run('delete', api.admin.deleteMeasure(m.id), t('admin.measuresTab.deleted'), t('admin.measuresTab.statusFailed'));
  };

  return (
    <li className={s.item} data-test={`pm-measure-${m.id}`}>
      <div className={s.measureBody}>
        <div className={s.measureHead}>
          <span className={s.measureTitle}>{m.title}</span>
          <OriginChip origin={m.origin} />
          <span className={clsx(s.itemMastery, m.priority === 1 && s.itemMasteryNew, m.priority === 2 && s.itemMasteryReviewing)}>
            {t('admin.measuresTab.priorityLabel', { level: m.priority, label: t(`admin.measuresTab.priority${m.priority}`) })}
          </span>
        </div>
        <p className={s.measureDetail}>{m.detail}</p>
        {m.kc_name && <p className={s.itemMeta}>{t('admin.measuresTab.kcLabel', { kc: m.kc_name })}</p>}
        {m.error_types.length > 0 && (
          <p className={s.itemMeta}>{t('admin.measuresTab.errorTypesLabel', { types: m.error_types.map(x => errorTypeLabel(t, x)).join(' · ') })}</p>
        )}
        {/* 任务②：来历行——让家长知道这条措施为什么存在、依据什么。 */}
        <p className={s.itemMeta} data-test={`pm-why-${m.id}`}>{originWhy(t, m)}</p>
        <div className={s.itemActions}>
          <button type="button" className={ui.btnGhost} onClick={strengthen} disabled={!!busy} data-test={`pm-strengthen-${m.id}`}>
            {busy === 'strengthen' ? t('admin.measuresTab.strengthening') : t('admin.measuresTab.strengthen')}
          </button>
          {m.status !== 'done' && (
            <button type="button" className={ui.btnGhost} onClick={() => setStatus('done')} disabled={!!busy} data-test={`pm-finish-${m.id}`}>
              {t('admin.measuresTab.finish')}
            </button>
          )}
          {m.status === 'active' && (
            <button type="button" className={ui.btnGhost} onClick={() => setStatus('paused')} disabled={!!busy} data-test={`pm-pause-${m.id}`}>
              {t('admin.measuresTab.pause')}
            </button>
          )}
          {m.status === 'paused' && (
            <button type="button" className={ui.btnGhost} onClick={() => setStatus('active')} disabled={!!busy} data-test={`pm-resume-${m.id}`}>
              {t('admin.measuresTab.resume')}
            </button>
          )}
          <button type="button" className={ui.btnDanger} onClick={del} disabled={!!busy} data-test={`pm-delete-${m.id}`}>
            {t('admin.measuresTab.delete')}
          </button>
        </div>
      </div>
    </li>
  );
};

/** The hand-written measure form (OD createTitle family). */
const CreateForm: React.FC<{
  kidId: number;
  onToast: (msg: string) => void;
  onChanged: () => void;
}> = ({ kidId, onToast, onChanged }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState('');
  const [detail, setDetail] = useState('');
  const [subject, setSubject] = useState('math');
  const [priority, setPriority] = useState<1 | 2 | 3>(2);
  const [types, setTypes] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  const submit = async () => {
    if (busy || !title.trim() || !detail.trim()) return;
    setBusy(true);
    try {
      await api.admin.createMeasure({
        user_id: kidId, subject, title: title.trim(), detail: detail.trim(),
        error_types: types, priority,
      });
      onToast(t('admin.measuresTab.create', { defaultValue: t('admin.measuresTab.create') }));
      setTitle(''); setDetail(''); setTypes([]); setPriority(2);
      setOpen(false);
      onChanged();
    } catch {
      onToast(t('admin.measuresTab.createFailed'));
    }
    setBusy(false);
  };

  if (!open) {
    return (
      <button type="button" className={ui.btnPrimary} onClick={() => setOpen(true)} data-test="pm-create-open">
        ✍️ {t('admin.measuresTab.createTitle')}
      </button>
    );
  }

  return (
    <form
      className={s.createForm}
      onSubmit={e => { e.preventDefault(); submit(); }}
      data-test="pm-create-form"
    >
      <h3 className={ui.sectionTitle}>{t('admin.measuresTab.createTitle')}</h3>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.measuresTab.createSubject')}</span>
        <select className={ui.input} value={subject} onChange={e => setSubject(e.target.value)}>
          {['math', 'chinese', 'english', 'physics', 'chemistry'].map(x => (
            <option key={x} value={x}>{t(`admin.analysis.subject_${x}`)}</option>
          ))}
        </select>
      </label>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.measuresTab.createTitleLabel')}</span>
        <input className={ui.input} value={title} onChange={e => setTitle(e.target.value)} required />
      </label>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.measuresTab.createDetail')}</span>
        <textarea className={ui.input} rows={3} value={detail} onChange={e => setDetail(e.target.value)} required />
      </label>
      <div className={ui.field}>
        <span className={ui.label}>{t('admin.measuresTab.createErrorTypes')}</span>
        <div className={ui.chips} role="group">
          {ERROR_TYPES.map(x => (
            <button
              key={x}
              type="button"
              className={clsx(ui.chip, types.includes(x) && ui.chipOn)}
              aria-pressed={types.includes(x)}
              onClick={() => setTypes(prev => prev.includes(x) ? prev.filter(y => y !== x) : [...prev, x])}
            >
              {errorTypeLabel(t, x)}
            </button>
          ))}
        </div>
      </div>
      <label className={ui.field}>
        <span className={ui.label}>{t('admin.measuresTab.createPriority')}</span>
        <select className={ui.input} value={priority} onChange={e => setPriority(Number(e.target.value) as 1 | 2 | 3)}>
          <option value={1}>{t('admin.measuresTab.priority1')}</option>
          <option value={2}>{t('admin.measuresTab.priority2')}</option>
          <option value={3}>{t('admin.measuresTab.priority3')}</option>
        </select>
      </label>
      <div className={s.itemActions}>
        <button type="submit" className={ui.btnPrimary} disabled={busy}>
          {busy ? t('admin.measuresTab.creating') : t('admin.measuresTab.create')}
        </button>
        <button type="button" className={ui.btnGhost} onClick={() => setOpen(false)}>{t('admin.analysis.close')}</button>
      </div>
    </form>
  );
};

export const MeasuresTab: React.FC = () => {
  const { t } = useTranslation();
  const [users, setUsers] = useState<User[]>([]);
  const [kidId, setKidId] = useState<number | null>(null);
  const [measures, setMeasures] = useState<LearningMeasure[] | null>(null);
  const [failed, setFailed] = useState(false);
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
      setMeasures(await api.admin.listMeasures(kidId));
    } catch {
      setFailed(true);
      setMeasures([]);
    }
  }, [kidId]);

  useEffect(() => { load(); }, [load]);

  const groups = useMemo(() => {
    const all = measures ?? [];
    return {
      active: all.filter(m => m.status === 'active'),
      paused: all.filter(m => m.status === 'paused'),
      done: all.filter(m => m.status === 'done'),
    };
  }, [measures]);

  const renderGroup = (key: 'active' | 'paused' | 'done', list: LearningMeasure[]) => {
    if (list.length === 0) return null;
    return (
      <section key={key} className={ui.section}>
        <h3 className={ui.sectionTitle}>{t(`admin.measuresTab.group${key.charAt(0).toUpperCase()}${key.slice(1)}`)} ({list.length})</h3>
        <ul className={s.list}>
          {list.map(m => <MeasureCard key={m.id} m={m} onToast={setToast} onChanged={load} />)}
        </ul>
      </section>
    );
  };

  return (
    <div className={ui.page} data-test="admin-measures-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>🎯 {t('admin.measuresTab.heading')}</h2>
          <p className={ui.pageSub}>{t('admin.measuresTab.subtitle')}</p>
        </div>
        {kids.length > 1 && (
          <label className={ui.field}>
            <select
              className={ui.input}
              value={kidId ?? ''}
              onChange={e => setKidId(Number(e.target.value))}
              aria-label={t('admin.measuresTab.pickKid')}
              data-test="pm-kid-select"
            >
              {kids.map(k => <option key={k.id} value={k.id}>{k.name}</option>)}
            </select>
          </label>
        )}
      </div>

      {kids.length === 0 && <p className={ui.emptyInline}>{t('admin.measuresTab.noKids')}</p>}

      {kids.length > 0 && (
        <>
          <CreateForm kidId={kidId!} onToast={setToast} onChanged={load} />

          {failed && <p className={ui.msgError} role="alert">{t('admin.measuresTab.loadFailed')}</p>}
          {toast && <p className={ui.msgWaiting} role="status">{toast}</p>}

          {measures === null && <p className={ui.emptyInline}>{t('admin.measuresTab.loading')}</p>}

          {measures !== null && measures.length === 0 && !failed && (
            <p className={ui.emptyInline} data-test="pm-empty">{t('admin.measuresTab.empty')}</p>
          )}

          {measures !== null && measures.length > 0 && (
            <>
              {renderGroup('active', groups.active)}
              {renderGroup('paused', groups.paused)}
              {renderGroup('done', groups.done)}
            </>
          )}
        </>
      )}
    </div>
  );
};
