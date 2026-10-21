import React, { useState, useEffect, useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { ChoreTemplate, TemplateTask, TemplateTune, TemplateImportTask, TaskTemplateWithUsage, User } from '../../types';
import { useAIStatus } from '../../hooks/useAIStatus';
import { Avatar, CategoryMark, Icon, catFromCategory } from '../../design';
import { personColor } from './pickers';
import ui from './ui.module.css';
import styles from './TemplatesTab.module.css';

/** One pack task plus the state the preview collects around it. */
interface PreviewTask extends TemplateTask {
  /** Position in the pack, so tuner changes can find their task. */
  index: number;
  checked: boolean;
  tuned: boolean;
}

/** Human label for a task's days: expanded presets stay short ("每日"). */
function daysLabel(t: (k: string, opts?: any) => string, days?: string[]): string {
  if (!days || days.length === 0) return t('admin.templatesTab.daysDaily');
  if (days.includes('daily')) return t('admin.templatesTab.daysDaily');
  if (days.includes('weekdays')) return t('admin.templatesTab.daysWeekdays');
  if (days.includes('weekends')) return t('admin.templatesTab.daysWeekends');
  return days.map(d => t(`admin.templatesTab.day_${d}`)).join(' · ');
}

export const TemplatesTab: React.FC = () => {
  const { t } = useTranslation();
  const aiStatus = useAIStatus();

  const [packs, setPacks] = useState<ChoreTemplate[]>([]);
  const [users, setUsers] = useState<User[]>([]);
  const [activePack, setActivePack] = useState<ChoreTemplate | null>(null);
  const [tasks, setTasks] = useState<PreviewTask[]>([]);

  // AI tuning
  const [tweak, setTweak] = useState('');
  const [tuning, setTuning] = useState(false);
  const [tuneSummary, setTuneSummary] = useState('');
  const [tuneError, setTuneError] = useState('');

  // Import
  const [targetId, setTargetId] = useState<number | null>(null);
  const [stopDaily, setStopDaily] = useState(false);
  const [running, setRunning] = useState<number | null>(null);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState('');
  const [imported, setImported] = useState<number | null>(null);

  // L0 "my task templates": saved chores with usage counts. Instantiating
  // asks for the kid first; syncing pushes edits to open instances.
  const [myTemplates, setMyTemplates] = useState<TaskTemplateWithUsage[]>([]);
  const [showArchived, setShowArchived] = useState(false);
  const [tplBusy, setTplBusy] = useState<number | null>(null);
  const [tplNotice, setTplNotice] = useState('');
  const [instantiateFor, setInstantiateFor] = useState<TaskTemplateWithUsage | null>(null);
  const [instKid, setInstKid] = useState<number | null>(null);
  const [instDate, setInstDate] = useState('');

  const load = useCallback(async () => {
    const [p, u] = await Promise.all([api.admin.listTemplates(), api.users.list()]);
    setPacks(p);
    setUsers(u);
  }, []);

  const loadMyTemplates = useCallback(async (archived: boolean) => {
    try {
      setMyTemplates(await api.admin.listTaskTemplates(archived));
    } catch {
      setMyTemplates([]);
    }
  }, []);

  useEffect(() => { loadMyTemplates(showArchived); }, [loadMyTemplates, showArchived]);

  useEffect(() => { load(); }, [load]);

  const kids = useMemo(() => users.filter(u => u.role === 'child'), [users]);
  const aiReady = aiStatus.ai.configured;

  // Default the target to the only kid, or keep whatever is chosen.
  useEffect(() => {
    if (targetId === null && kids.length > 0) {
      setTargetId(kids.length === 1 ? kids[0].id : null);
    }
  }, [kids, targetId]);

  const openPack = useCallback((pack: ChoreTemplate) => {
    setActivePack(pack);
    setTasks(pack.tasks.map((task, index) => ({ ...task, index, checked: true, tuned: false })));
    setTweak('');
    setTuneSummary('');
    setTuneError('');
    setImportError('');
    setImported(null);
    setRunning(null);
    setStopDaily(false);
  }, []);

  // Conflict preview: how many of the kid's open-ended dailies a swap would
  // stop (needs both the kid and the pack selected).
  useEffect(() => {
    if (!activePack || targetId === null) { setRunning(null); return; }
    let live = true;
    api.admin.runningDaily(targetId, activePack.id)
      .then(r => { if (live) setRunning(r.running); })
      .catch(() => { if (live) setRunning(null); });
    return () => { live = false; };
  }, [activePack, targetId, imported]);

  const handleTune = async () => {
    if (!activePack || !tweak.trim()) return;
    setTuning(true);
    setTuneError('');
    try {
      const tune: TemplateTune = await api.admin.tuneTemplate(
        activePack.id,
        tasks.map(task => ({
          title: task.title,
          category: task.category,
          points: task.points,
          estimated_minutes: task.estimated_minutes,
        })),
        tweak.trim(),
      );
      setTuneSummary(tune.summary);
      const changesByIndex = new Map(tune.changes.map(c => [c.index, c]));
      setTasks(prev => prev.map(task => {
        const change = changesByIndex.get(task.index);
        if (!change) return task;
        const next: PreviewTask = { ...task, tuned: true };
        if (change.title) next.title = change.title;
        if (change.description) next.description = change.description;
        if (typeof change.points === 'number') next.points = change.points;
        if (typeof change.estimated_minutes === 'number') next.estimated_minutes = change.estimated_minutes;
        return next;
      }));
    } catch (err: any) {
      setTuneError(err?.message || t('admin.templatesTab.tuneFailed'));
    }
    setTuning(false);
  };

  const toggleTask = (index: number) => {
    setTasks(prev => prev.map(task => task.index === index ? { ...task, checked: !task.checked } : task));
  };

  const selected = tasks.filter(task => task.checked);
  const totalPoints = selected.reduce((sum, task) => sum + (task.points || 0), 0);

  const tplNoticeFor = (msg: string) => {
    setTplNotice(msg);
    setTimeout(() => setTplNotice(''), 4000);
  };

  // Turn a saved template into a live chore for one kid; the date is
  // optional (the template's cadence snapshot fills it, or it lands today).
  const handleInstantiate = async () => {
    if (!instantiateFor || instKid === null) return;
    setTplBusy(instantiateFor.id);
    try {
      await api.admin.instantiateTaskTemplate(instantiateFor.id, {
        assigned_to: instKid,
        specific_date: instDate || undefined,
      });
      setInstantiateFor(null);
      setInstDate('');
      tplNoticeFor(t('admin.templatesTab.instantiated', { name: instantiateFor.name }));
      loadMyTemplates(showArchived);
    } catch (e) {
      alert(e instanceof Error ? e.message : t('admin.templatesTab.instantiateFailed'));
    }
    setTplBusy(null);
  };

  // Push the template's fields to every instance that hasn't started.
  const handleSync = async (tpl: TaskTemplateWithUsage) => {
    setTplBusy(tpl.id);
    try {
      const r = await api.admin.syncTaskTemplate(tpl.id);
      tplNoticeFor(t('admin.templatesTab.synced', { count: r.updated }));
    } catch (e) {
      alert(e instanceof Error ? e.message : t('admin.templatesTab.syncFailed'));
    }
    setTplBusy(null);
  };

  const handleArchive = async (tpl: TaskTemplateWithUsage) => {
    if (!confirm(t('admin.templatesTab.archiveConfirm', { name: tpl.name }))) return;
    setTplBusy(tpl.id);
    try {
      await api.admin.archiveTaskTemplate(tpl.id);
      loadMyTemplates(showArchived);
    } catch (e) {
      alert(e instanceof Error ? e.message : t('admin.templatesTab.archiveFailed'));
    }
    setTplBusy(null);
  };

  const handleImport = async () => {
    if (!activePack || targetId === null || selected.length === 0) return;
    setImporting(true);
    setImportError('');
    try {
      const payloadTasks: TemplateImportTask[] = selected.map(task => ({
        title: task.title,
        description: task.description ?? '',
        icon: task.icon ?? '',
        category: task.category,
        points: task.points ?? 0,
        missed_penalty: task.missed_penalty ?? 0,
        estimated_minutes: task.estimated_minutes ?? 0,
        days: task.days ?? ['daily'],
        available_at: task.available_at ?? '',
        due_by: task.due_by ?? '',
        expiry: task.expiry ?? 'block',
      }));
      const result = await api.admin.importTemplate({
        template_id: activePack.id,
        assigned_to: targetId,
        tasks: payloadTasks,
        stop_daily: stopDaily,
      });
      setImported(result.imported);
      setStopDaily(false);
    } catch (err: any) {
      setImportError(err?.message || t('admin.templatesTab.importFailed'));
    }
    setImporting(false);
  };

  return (
    <div className={ui.page} data-test="admin-templates-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>{t('admin.templatesTab.heading')}</h2>
          <p className={ui.pageSub}>{t('admin.templatesTab.subtitle')}</p>
        </div>
      </div>

      <div className={styles.templateWrap}>
        <main>
          <div className={styles.packGrid}>
            {packs.map(pack => (
              <button
                key={pack.id}
                type="button"
                className={clsx(ui.card, styles.pack, activePack?.id === pack.id && styles.packOn)}
                onClick={() => openPack(pack)}
                aria-pressed={activePack?.id === pack.id}
              >
                <span className={styles.packHead}>
                  <span className={styles.packIcon} aria-hidden>{pack.icon}</span>
                  <span style={{ flex: 1, minWidth: 0 }}>
                    <span className={styles.packName}>{pack.name}</span>
                    <span className={styles.packMeta}>
                      {t('admin.templatesTab.packMeta', { count: pack.tasks.length, scenario: pack.scenario })}
                    </span>
                  </span>
                </span>
                <ul className={styles.packTasks}>
                  {pack.tasks.slice(0, 4).map((task, i) => (
                    <li key={i}>{task.title}</li>
                  ))}
                  {pack.tasks.length > 4 && (
                    <li className={styles.packMore}>
                      {t('admin.templatesTab.moreTasks', { count: pack.tasks.length - 4 })}
                    </li>
                  )}
                </ul>
                {pack.bonus_paused && (
                  <span className={clsx(ui.badge, ui.badgeOutline)}>
                    {t('admin.templatesTab.bonusPaused')}
                  </span>
                )}
                <span className={styles.packFooter}>
                  <span className={styles.packMeta}>{pack.description}</span>
                  <span className={clsx(ui.badge, activePack?.id === pack.id ? ui.badgeWaiting : ui.badgeOutline)}>
                    {activePack?.id === pack.id ? t('admin.templatesTab.previewing') : t('admin.templatesTab.preview')}
                  </span>
                </span>
              </button>
            ))}
          </div>

          {/* L0 "my task templates": the chores saved from the chores tab,
              with instantiate / sync / archive (Habitica-style templates). */}
          <section className={styles.myTemplates} data-test="my-task-templates">
            <div className={ui.sectionHead}>
              <h3 className={ui.sectionTitle}>{t('admin.templatesTab.myTemplatesTitle')}</h3>
              <label className={styles.archivedToggle}>
                <input
                  type="checkbox"
                  checked={showArchived}
                  onChange={e => setShowArchived(e.target.checked)}
                />
                <span>{t('admin.templatesTab.showArchived')}</span>
              </label>
            </div>
            <p className={ui.sectionDesc}>{t('admin.templatesTab.myTemplatesDesc')}</p>
            {tplNotice && <p className={ui.msg} role="status">{tplNotice}</p>}

            {myTemplates.length === 0 ? (
              <div className={ui.empty}>
                <Icon name="star" />
                <p>{showArchived ? t('admin.templatesTab.noArchived') : t('admin.templatesTab.noTemplates')}</p>
              </div>
            ) : (
              <div className={styles.myTplGrid}>
                {myTemplates.map(tpl => {
                  const cat = catFromCategory(tpl.category);
                  return (
                    <div key={tpl.id} className={clsx(ui.card, styles.myTplCard)} data-test="task-template-card">
                      <div className={styles.myTplHead}>
                        <span className={ui.badge}><CategoryMark cat={cat} /></span>
                        <span className={styles.myTplName}>{tpl.name}</span>
                        {tpl.is_archived && (
                          <span className={clsx(ui.badge, ui.badgeOutline)}>{t('admin.templatesTab.archivedTag')}</span>
                        )}
                      </div>
                      {tpl.description && <p className={styles.myTplDesc}>{tpl.description}</p>}
                      <div className={styles.myTplMeta}>
                        <span><Icon name="star" /> {t('admin.templatesTab.pts', { count: tpl.points })}</span>
                        <span>{t('admin.templatesTab.uses', { count: tpl.instance_count })}</span>
                        {tpl.open_instances > 0 && (
                          <span>{t('admin.templatesTab.openInstances', { count: tpl.open_instances })}</span>
                        )}
                        {tpl.requires_photo && <span><Icon name="camera" /> {t('admin.templatesTab.photoTag')}</span>}
                      </div>
                      <div className={ui.actions}>
                        <button
                          type="button"
                          className={ui.btnPrimary}
                          onClick={() => {
                            setInstantiateFor(tpl);
                            setInstKid(kids.length === 1 ? kids[0].id : null);
                          }}
                          disabled={tplBusy === tpl.id || tpl.is_archived}
                        >
                          <Icon name="plus" /> {t('admin.templatesTab.instantiateBtn')}
                        </button>
                        <button
                          type="button"
                          className={ui.btnGhost}
                          onClick={() => handleSync(tpl)}
                          disabled={tplBusy === tpl.id || tpl.open_instances === 0}
                          title={tpl.open_instances === 0 ? t('admin.templatesTab.syncNoneTitle') : undefined}
                        >
                          <Icon name="check" /> {t('admin.templatesTab.syncBtn')}
                        </button>
                        <button
                          type="button"
                          className={ui.btnGhost}
                          onClick={() => handleArchive(tpl)}
                          disabled={tplBusy === tpl.id}
                        >
                          {t('admin.templatesTab.archiveBtn')}
                        </button>
                      </div>
                    </div>
                  );
                })}
              </div>
            )}

            {/* Instantiate dialog: pick the kid (and an optional one-off date). */}
            {instantiateFor && (
              <div className={styles.instDialog} role="dialog" aria-modal="true" aria-label={t('admin.templatesTab.instantiateTitle', { name: instantiateFor.name })}>
                <div className={styles.instDialogCard}>
                  <h4 className={styles.instDialogTitle}>{t('admin.templatesTab.instantiateTitle', { name: instantiateFor.name })}</h4>
                  <p className={ui.sectionDesc}>{t('admin.templatesTab.instantiateDesc')}</p>
                  {kids.length === 0 ? (
                    <p className={ui.help}>{t('admin.templatesTab.noKids')}</p>
                  ) : (
                    <div className={styles.targetRow}>
                      {kids.map(kid => (
                        <button
                          key={kid.id}
                          type="button"
                          className={clsx(ui.personChip, instKid === kid.id && ui.chipOn)}
                          onClick={() => setInstKid(kid.id)}
                          aria-pressed={instKid === kid.id}
                        >
                          <Avatar name={kid.name} color={personColor(kid)} size="sm" />
                          {kid.name}
                        </button>
                      ))}
                    </div>
                  )}
                  <label className={styles.instDateRow}>
                    <span>{t('admin.templatesTab.instantiateDate')}</span>
                    <input
                      type="date"
                      value={instDate}
                      onChange={e => setInstDate(e.target.value)}
                    />
                  </label>
                  <div className={ui.actions}>
                    <button type="button" className={ui.btnPrimary} onClick={handleInstantiate} disabled={tplBusy !== null || instKid === null}>
                      <Icon name="check" /> {t('admin.templatesTab.instantiateConfirm')}
                    </button>
                    <button type="button" className={ui.btnGhost} onClick={() => setInstantiateFor(null)} disabled={tplBusy !== null}>
                      {t('admin.templatesTab.instantiateCancel')}
                    </button>
                  </div>
                </div>
              </div>
            )}
          </section>

          <section className={clsx(ui.card, styles.rules)}>
            <div className={ui.sectionHead}>
              <h3 className={ui.sectionTitle}>{t('admin.templatesTab.rulesTitle')}</h3>
            </div>
            <div className={styles.rulesList}>
              {([1, 2, 3] as const).map(n => (
                <div key={n} className={styles.ruleRow}>
                  <span className={clsx(ui.badge, ui.badgeOutline, styles.ruleNum)}>{n}</span>
                  <span className={styles.ruleBody}>
                    <span className={styles.ruleTitle}>{t(`admin.templatesTab.rule${n}Title`)}</span>
                    <span className={styles.ruleText}>{t(`admin.templatesTab.rule${n}Text`)}</span>
                  </span>
                </div>
              ))}
            </div>
          </section>
        </main>

        <aside className={styles.rail}>
          {!activePack ? (
            <div className={clsx(ui.card, ui.emptyInline)}>
              <p>{t('admin.templatesTab.pickFirst')}</p>
            </div>
          ) : (
            <section className={ui.card}>
              <div className={styles.railHead}>
                <span className={styles.packIcon} aria-hidden>{activePack.icon}</span>
                <span className={styles.railTitle}>
                  {activePack.name}
                  <span className={clsx(ui.badge, ui.badgeOutline)}>{t('admin.templatesTab.previewBadge')}</span>
                </span>
              </div>

              {/* AI tuning */}
              <div className={styles.tuneBox} style={{ marginTop: 12 }}>
                <label className={styles.tuneLabel} htmlFor="tpl-tune">
                  {t('admin.templatesTab.tuneLabel')}
                </label>
                <textarea
                  id="tpl-tune"
                  className={styles.tuneArea}
                  rows={2}
                  value={tweak}
                  onChange={e => setTweak(e.target.value)}
                  placeholder={t('admin.templatesTab.tunePlaceholder')}
                  disabled={!aiReady}
                />
                {!aiReady && <p className={ui.help}>{t('admin.templatesTab.tuneNeedsAI')}</p>}
                <button
                  type="button"
                  className={ui.btnPrimary}
                  onClick={handleTune}
                  disabled={!aiReady || tuning || !tweak.trim()}
                >
                  <Icon name="spark" /> {tuning ? t('admin.templatesTab.tuning') : t('admin.templatesTab.tuneBtn')}
                </button>
                {tuneError && <p className={clsx(ui.msg, ui.msgError)}>{tuneError}</p>}
                {tuneSummary && <p className={styles.tuneNote}>✨ {tuneSummary}</p>}
              </div>

              {/* Checklist */}
              <div className={styles.checkHead}>
                <span className={styles.checkTitle}>
                  {t('admin.templatesTab.checklist', { count: selected.length })}
                </span>
                <span className={styles.checkTotal}>
                  {t('admin.templatesTab.totalPoints', { count: totalPoints })}
                </span>
              </div>
              <div className={styles.taskList}>
                {tasks.map(task => {
                  const cat = catFromCategory(task.category);
                  return (
                    <button
                      key={task.index}
                      type="button"
                      className={styles.taskRow}
                      onClick={() => toggleTask(task.index)}
                      aria-pressed={task.checked}
                    >
                      <span className={clsx(styles.taskCheck, !task.checked && styles.taskCheckOff)} aria-hidden>
                        {task.checked ? '✓' : ''}
                      </span>
                      <span className={styles.taskBody}>
                        <span className={clsx(styles.taskTitle, !task.checked && styles.taskTitleOff)}>
                          {task.title}
                        </span>
                        <span className={styles.taskMeta}>
                          <CategoryMark cat={cat} />
                          <span>{t('admin.templatesTab.pts', { count: task.points ?? 0 })}</span>
                          {!!task.estimated_minutes && (
                            <span>⏱ {t('admin.templatesTab.minutes', { count: task.estimated_minutes })}</span>
                          )}
                          <span>{daysLabel(t, task.days)}</span>
                          {task.tuned && <span className={styles.tuned}>✨ {t('admin.templatesTab.tunedTag')}</span>}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>

              {/* Target kid */}
              <div style={{ marginTop: 12 }}>
                <p className={styles.tuneLabel}>{t('admin.templatesTab.targetLabel')}</p>
                {kids.length === 0 ? (
                  <p className={ui.help}>{t('admin.templatesTab.noKids')}</p>
                ) : (
                  <div className={styles.targetRow}>
                    {kids.map(kid => (
                      <button
                        key={kid.id}
                        type="button"
                        className={clsx(ui.personChip, targetId === kid.id && ui.chipOn)}
                        onClick={() => setTargetId(kid.id)}
                        aria-pressed={targetId === kid.id}
                      >
                        <Avatar name={kid.name} color={personColor(kid)} size="sm" />
                        {kid.name}
                      </button>
                    ))}
                  </div>
                )}
              </div>

              {/* Conflict warning */}
              {running !== null && running > 0 && (
                <div className={styles.warnbar} style={{ marginTop: 12 }}>
                  <span aria-hidden>⚠</span>
                  <span>
                    {t('admin.templatesTab.conflictWarning', { count: running })}
                    <label className={ui.check} style={{ marginTop: 8 }}>
                      <input
                        type="checkbox"
                        checked={stopDaily}
                        onChange={e => setStopDaily(e.target.checked)}
                      />
                      <span className={ui.checkLabel}>{t('admin.templatesTab.stopDailyOpt')}</span>
                    </label>
                  </span>
                </div>
              )}

              {importError && <p className={clsx(ui.msg, ui.msgError)} style={{ marginTop: 10 }}>{importError}</p>}
              {imported !== null && (
                <p className={clsx(ui.msg)} style={{ marginTop: 10 }}>
                  {t('admin.templatesTab.importedMsg', { count: imported })}
                </p>
              )}

              <button
                type="button"
                className={clsx(ui.btnPrimary, styles.importBtn)}
                style={{ marginTop: 11 }}
                onClick={handleImport}
                disabled={importing || targetId === null || selected.length === 0 || (running !== null && running > 0 && !stopDaily)}
              >
                <Icon name="check" /> {importing ? t('admin.templatesTab.importing') : t('admin.templatesTab.importBtn')}
              </button>
              <p className={styles.afterNote} style={{ marginTop: 9 }}>
                {t('admin.templatesTab.afterNote')}
              </p>
            </section>
          )}
        </aside>
      </div>
    </div>
  );
};
