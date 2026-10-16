import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import clsx from 'clsx';
import { api } from '../../api';
import type { FSRSParams, ReviewStats, User } from '../../types';
import { FormMessage, type Msg } from './FormMessage';
import ui from './ui.module.css';
import styles from './SettingsTab.module.css';

// The L0-L4 parent switchboard, per OD prototype parent-settings.html's
// settings-l0l4 block: the three feature rows (error analysis L1,
// spaced review L4, profile & AI planning L3) each with their own
// master flag, plus the L4 dial underneath — the retention slider
// (0.80–0.95, Anki's recommended 0.90 in the middle) and the
// daily-new-cards stepper (Anki's default 20 — new cards amplify ~10×
// into future reviews). One kid at a time; the stats line beneath is
// the kid's live numbers, not decoration.

/** The retention band the backend accepts (wider is rejected there). */
const RETENTION_MIN = 0.80;
const RETENTION_MAX = 0.95;
const RETENTION_STEP = 0.01;
/** Anki's default daily new-card limit. */
const NEW_CARDS_DEFAULT = 20;
const NEW_CARDS_STEP = 5;

/** One OD feature row: title + L-tier tag, description, the "what if I
 *  turn it off" popover, the deep link, and the master switch. Tapping
 *  the switch saves immediately (each flag is its own setting row). */
const FeatureRow: React.FC<{
  test: string;
  title: string;
  tier: string;
  desc: string;
  descTest?: string;
  popover?: string;
  linkLabel?: string;
  linkHref?: string;
  linkTest?: string;
  flagTest: string;
  flagAria: string;
  on: boolean;
  onToggle: () => void;
}> = ({ test, title, tier, desc, descTest, popover, linkLabel, linkHref, linkTest, flagTest, flagAria, on, onToggle }) => (
  <div className={styles.l0l4Row} data-test={test}>
    <div className={styles.l0l4Body}>
      <div className={styles.l0l4Title}>
        {title}
        <span className={styles.l0l4Tier}>{tier}</span>
      </div>
      <p className={styles.l0l4Desc} data-test={descTest}>{desc}</p>
      {popover && (
        <p className={styles.l0l4Popover} data-test={`${test.replace(/^feature-/, '')}-popover`}>{popover}</p>
      )}
    </div>
    <div className={styles.l0l4Actions}>
      {linkLabel && linkHref && (
        <a className={styles.l0l4Link} href={linkHref} data-test={linkTest}>{linkLabel}</a>
      )}
      <button
        type="button"
        className={clsx(styles.swSwitch, on && styles.swOn)}
        data-test={flagTest}
        role="switch"
        aria-checked={on}
        aria-label={flagAria}
        onClick={onToggle}
      >
        <i aria-hidden />
      </button>
    </div>
  </div>
);

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

  // The three OD feature flags — each one its own setting row, toggled
  // in place (the OD prototype's three role="switch" buttons).
  const [errorbookOn, setErrorbookOn] = useState(true);
  const [fsrsOn, setFsrsOn] = useState(true);
  const [profileOn, setProfileOn] = useState(true);

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

  // Load the three feature flags (missing keys default to on — the
  // features are the product's core, opt-out not opt-in).
  useEffect(() => {
    const read = (key: string) => api.admin.getSetting(key).then(d => d.value !== 'false').catch(() => true);
    read('feature_errorbook').then(setErrorbookOn);
    read('feature_fsrs').then(setFsrsOn);
    read('feature_profile').then(setProfileOn);
  }, []);

  // One shared toggle: write the flag, keep the local state in sync, and
  // surface nothing on success (a switch that stays where you put it is
  // the feedback).
  const toggleFlag = async (key: string, on: boolean, set: (v: boolean) => void) => {
    set(!on);
    try {
      await api.admin.setSetting(key, on ? 'false' : 'true');
    } catch {
      set(on); // flip back — the write is the source of truth
    }
  };

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
  const L = 'admin.settingsTab.learning';

  return (
    <form className={clsx(ui.card, ui.section)} onSubmit={handleSaveParams} data-test="settings-l0l4">
      <h3 className={ui.sectionTitle}>📚 {t(`${L}.l0l4Title`)}
        <span className={styles.l0l4Tier}>{t(`${L}.l0l4Tag`)}</span>
      </h3>

      {/* L1: error analysis — flag-errorbook is the master switch; the
          popover answers "what breaks when it's off" before the parent
          flips it. */}
      <FeatureRow
        test="feature-errorbook"
        title={t(`${L}.errorbookTitle`)}
        tier={t(`${L}.errorbookTier`)}
        desc={t(`${L}.errorbookDesc`)}
        descTest="errorbook-desc"
        popover={t(`${L}.errorbookPopover`)}
        linkLabel={t(`${L}.errorbookLink`)}
        linkHref="#tab=errorbook"
        linkTest="errorbook-open-analysis"
        flagTest="flag-errorbook"
        flagAria={t(`${L}.errorbookFlag`)}
        on={errorbookOn}
        onToggle={() => toggleFlag('feature_errorbook', errorbookOn, setErrorbookOn)}
      />

      {/* L4: spaced review — the per-kid dial lives inside its feature
          row (retention slider + daily-new-cards stepper + live stats). */}
      <div className={styles.l0l4Row} data-test="feature-fsrs">
        <div className={styles.l0l4Body}>
          <div className={styles.l0l4Title}>{t(`${L}.fsrsTitle`)}
            <span className={styles.l0l4Tier}>{t(`${L}.fsrsTier`)}</span>
          </div>

          {/* Per-kid: the dial is per kid (Anki's options profile). */}
          {kids.length > 1 && (
            <label className={ui.field}>
              <span className={ui.label}>{t(`${L}.kidLabel`)}</span>
              <select
                className={ui.input}
                value={kidId ?? ''}
                onChange={e => setKidId(Number(e.target.value))}
                aria-label={t(`${L}.kidLabel`)}
                data-test="learning-kid-select"
              >
                {kids.map(k => (
                  <option key={k.id} value={k.id}>{k.name}</option>
                ))}
              </select>
            </label>
          )}

          <label className={ui.field}>
            <span className={ui.label}>{t(`${L}.retentionLabel`)}</span>
            <span className={styles.rangeRow}>
              <input
                type="range"
                className={ui.range}
                min={RETENTION_MIN}
                max={RETENTION_MAX}
                step={RETENTION_STEP}
                value={retention}
                onChange={e => setParams(p => (p ? { ...p, request_retention: Number(e.target.value) } : p))}
                aria-label={t(`${L}.retentionLabel`)}
                data-test="fsrs-retention-slider"
              />
              <output className={styles.rangeValue} data-test="fsrs-retention-value">
                {retention.toFixed(2)}
              </output>
            </span>
            <span className={ui.help}>{t(`${L}.retentionHelp`)}</span>
          </label>

          <FormMessage msg={message} />

          <div className={ui.actionsEnd}>
            <button type="submit" className={ui.btnPrimary} disabled={saving} data-test="learning-save-btn">
              {t(`${L}.saveButton`)}
            </button>
          </div>

          {/* The daily new-card cap: a household-wide setting (all kids
              share the queue's on-ramp), saved separately. */}
          <div className={styles.limitRow}>
            <form onSubmit={handleSaveLimit} className={styles.limitForm}>
              <span className={ui.label}>{t(`${L}.limitLabel`)}</span>
              <span className={styles.stepper} role="group" aria-label={t(`${L}.limitLabel`)}>
                <button
                  type="button"
                  className={styles.stepperBtn}
                  aria-label={t(`${L}.limitDown`)}
                  onClick={() => setNewLimit(v => String(Math.max(0, (Number(v) || 0) - NEW_CARDS_STEP)))}
                  data-test="fsrs-new-limit-down"
                >
                  −
                </button>
                <output className={styles.stepperValue} data-test="fsrs-new-limit-value">{newLimit}</output>
                <button
                  type="button"
                  className={styles.stepperBtn}
                  aria-label={t(`${L}.limitUp`)}
                  onClick={() => setNewLimit(v => String((Number(v) || 0) + NEW_CARDS_STEP))}
                  data-test="fsrs-new-limit-up"
                >
                  ＋
                </button>
              </span>
              <button type="submit" className={ui.btnGhost} disabled={savingLimit}>
                {t(`${L}.limitSave`)}
              </button>
            </form>
            <span className={ui.help}>{t(`${L}.limitHelp`)}</span>
            <FormMessage msg={limitMessage} />
          </div>

          {/* The kid's live numbers: streak, total cards, due now. */}
          {kidStats && (
            <p className={styles.statsLine} data-test="learning-stats">
              {t(`${L}.statsLine`, {
                name: kidStats.name,
                streak: kidStats.streak,
                total: kidStats.total,
                due: kidStats.due_now,
              })}
            </p>
          )}
        </div>
        <div className={styles.l0l4Actions}>
          <button
            type="button"
            className={styles.l0l4Link}
            data-test="fsrs-preview-kid"
            aria-label={t(`${L}.fsrsLink`)}
          >
            {t(`${L}.fsrsLink`)}
          </button>
          <button
            type="button"
            className={clsx(styles.swSwitch, fsrsOn && styles.swOn)}
            data-test="flag-fsrs"
            role="switch"
            aria-checked={fsrsOn}
            aria-label={t(`${L}.fsrsFlag`)}
            onClick={() => toggleFlag('feature_fsrs', fsrsOn, setFsrsOn)}
          >
            <i aria-hidden />
          </button>
        </div>
      </div>

      {/* L3: profile & AI planning — the parent's final-approval gate. */}
      <FeatureRow
        test="feature-profile"
        title={t(`${L}.profileTitle`)}
        tier={t(`${L}.profileTier`)}
        desc={t(`${L}.profileDesc`)}
        descTest="profile-desc"
        linkLabel={t(`${L}.profileLink`)}
        linkHref="#tab=profile"
        linkTest="profile-config-link"
        flagTest="flag-profile"
        flagAria={t(`${L}.profileFlag`)}
        on={profileOn}
        onToggle={() => toggleFlag('feature_profile', profileOn, setProfileOn)}
      />
    </form>
  );
};
