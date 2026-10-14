import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { FSRSParams, ReviewStats, User } from '../../types';
import { FormMessage, type Msg } from './FormMessage';
import ui from './ui.module.css';
import styles from './SettingsTab.module.css';

// The L4 parent dial, per OD prototype parent-settings.html's
// settings-l0l4 feature-fsrs row: the retention slider (0.80–0.95,
// Anki's recommended 0.90 in the middle) and the daily-new-cards
// stepper (Anki's default 20 — new cards amplify ~10× into future
// reviews). One kid at a time; the stats line beneath is the kid's
// live numbers, not decoration.

/** The retention band the backend accepts (wider is rejected there). */
const RETENTION_MIN = 0.80;
const RETENTION_MAX = 0.95;
const RETENTION_STEP = 0.01;
/** Anki's default daily new-card limit. */
const NEW_CARDS_DEFAULT = 20;
const NEW_CARDS_STEP = 5;

export const LearningSection: React.FC = () => {
  const { t } = useTranslation();

  const [kids, setKids] = useState<User[]>([]);
  const [kidId, setKidId] = useState<number | null>(null);
  const [params, setParams] = useState<FSRSParams | null>(null);
  const [stats, setStats] = useState<ReviewStats | null>(null);
  const [newLimit, setNewLimit] = useState(NEW_CARDS_DEFAULT.toString());
  const [saving, setSaving] = useState(false);
  const [savingLimit, setSavingLimit] = useState(false);
  const [message, setMessage] = useState<Msg>(null);
  const [limitMessage, setLimitMessage] = useState<Msg>(null);

  const loadKid = useCallback(async (id: number) => {
    const [p, s] = await Promise.all([
      api.admin.getFSRSParams(id).catch(() => null),
      api.admin.reviewStats(id).catch(() => null),
    ]);
    setParams(p);
    setStats(s);
  }, []);

  useEffect(() => {
    api.users.list()
      .then(users => {
        // Only kids with review data matter for the dial; paused kids
        // keep theirs too (they come back).
        const children = users.filter(u => u.role === 'child');
        setKids(children);
        if (children.length > 0) setKidId(children[0].id);
      })
      .catch(() => setKids([]));
  }, []);

  useEffect(() => {
    if (kidId == null) return;
    api.admin.getSetting('fsrs_daily_new_cards')
      .then(d => setNewLimit(d.value || String(NEW_CARDS_DEFAULT)))
      .catch(() => setNewLimit(String(NEW_CARDS_DEFAULT)));
    loadKid(kidId);
  }, [kidId, loadKid]);

  const handleSaveParams = async (e: React.FormEvent) => {
    e.preventDefault();
    if (kidId == null) return;
    setSaving(true);
    setMessage(null);
    try {
      const p = await api.admin.updateFSRSParams(kidId, {
        request_retention: Number(params?.request_retention ?? 0.90),
        enable_fuzz: params?.enable_fuzz ?? true,
      });
      setParams(p);
      setMessage({ type: 'success', text: t('admin.settingsTab.learning.saveSuccess') });
    } catch {
      setMessage({ type: 'error', text: t('admin.settingsTab.learning.saveError') });
    }
    setSaving(false);
  };

  const handleSaveLimit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSavingLimit(true);
    setLimitMessage(null);
    try {
      await api.admin.setSetting('fsrs_daily_new_cards', newLimit);
      setLimitMessage({ type: 'success', text: t('admin.settingsTab.learning.saveSuccess') });
    } catch {
      setLimitMessage({ type: 'error', text: t('admin.settingsTab.learning.saveError') });
    }
    setSavingLimit(false);
  };

  if (kids.length === 0) return null;

  const retention = params?.request_retention ?? 0.90;
  const kidStats = stats?.per_kid?.find(k => k.user_id === kidId);

  return (
    <form className={clsx(ui.card, ui.section)} onSubmit={handleSaveParams} data-test="settings-learning">
      <h3 className={ui.sectionTitle}>📚 {t('admin.settingsTab.learning.title')}</h3>
      <p className={ui.sectionDesc}>{t('admin.settingsTab.learning.description')}</p>

      {/* Per-kid: the dial is per kid (Anki's options profile). */}
      {kids.length > 1 && (
        <label className={ui.field}>
          <span className={ui.label}>{t('admin.settingsTab.learning.kidLabel')}</span>
          <select
            className={ui.input}
            value={kidId ?? ''}
            onChange={e => setKidId(Number(e.target.value))}
            aria-label={t('admin.settingsTab.learning.kidLabel')}
            data-test="learning-kid-select"
          >
            {kids.map(k => (
              <option key={k.id} value={k.id}>{k.name}</option>
            ))}
          </select>
        </label>
      )}

      <label className={ui.field}>
        <span className={ui.label}>{t('admin.settingsTab.learning.retentionLabel')}</span>
        <span className={styles.rangeRow}>
          <input
            type="range"
            className={ui.range}
            min={RETENTION_MIN}
            max={RETENTION_MAX}
            step={RETENTION_STEP}
            value={retention}
            onChange={e => setParams(p => (p ? { ...p, request_retention: Number(e.target.value) } : p))}
            aria-label={t('admin.settingsTab.learning.retentionLabel')}
            data-test="fsrs-retention-slider"
          />
          <output className={styles.rangeValue} data-test="fsrs-retention-value">
            {retention.toFixed(2)}
          </output>
        </span>
        <span className={ui.help}>{t('admin.settingsTab.learning.retentionHelp')}</span>
      </label>

      <FormMessage msg={message} />

      <div className={ui.actionsEnd}>
        <button type="submit" className={ui.btnPrimary} disabled={saving} data-test="learning-save-btn">
          {t('admin.settingsTab.learning.saveButton')}
        </button>
      </div>

      {/* The daily new-card cap: a household-wide setting (all kids
          share the queue's on-ramp), saved separately. */}
      <div className={styles.limitRow}>
        <form onSubmit={handleSaveLimit} className={styles.limitForm}>
          <span className={ui.label}>{t('admin.settingsTab.learning.limitLabel')}</span>
          <span className={styles.stepper} role="group" aria-label={t('admin.settingsTab.learning.limitLabel')}>
            <button
              type="button"
              className={styles.stepperBtn}
              aria-label={t('admin.settingsTab.learning.limitDown')}
              onClick={() => setNewLimit(v => String(Math.max(0, (Number(v) || 0) - NEW_CARDS_STEP)))}
              data-test="fsrs-new-limit-down"
            >
              −
            </button>
            <output className={styles.stepperValue} data-test="fsrs-new-limit-value">{newLimit}</output>
            <button
              type="button"
              className={styles.stepperBtn}
              aria-label={t('admin.settingsTab.learning.limitUp')}
              onClick={() => setNewLimit(v => String((Number(v) || 0) + NEW_CARDS_STEP))}
              data-test="fsrs-new-limit-up"
            >
              ＋
            </button>
          </span>
          <button type="submit" className={ui.btnGhost} disabled={savingLimit}>
            {t('admin.settingsTab.learning.limitSave')}
          </button>
        </form>
        <span className={ui.help}>{t('admin.settingsTab.learning.limitHelp')}</span>
        <FormMessage msg={limitMessage} />
      </div>

      {/* The kid's live numbers: streak, total cards, due now. */}
      {kidStats && (
        <p className={styles.statsLine} data-test="learning-stats">
          {t('admin.settingsTab.learning.statsLine', {
            name: kidStats.name,
            streak: kidStats.streak,
            total: kidStats.total,
            due: kidStats.due_now,
          })}
        </p>
      )}
    </form>
  );
};
