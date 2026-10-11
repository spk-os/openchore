import React, { useEffect, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { api, APIError } from '../api';
import type { BoostSnapshot } from '../types';
import { Icon, HouseScope } from '../design';
import styles from './BoostPage.module.css';

// Emoji per boost tier — a garnish of encouragement, not a price list.
const AMOUNT_EMOJI: Record<number, string> = { 5: '💪', 10: '🌟', 20: '🎁' };

/** The state of a boost tap, when the server said no. */
interface CapState {
  /** The monthly allowance is used up. */
  monthly: boolean;
  /** Today's boosts are spent; come back tomorrow. */
  daily: boolean;
}

/**
 * The grandparent's page (P4-16): a token-addressed, read-only window into
 * one kid's week, opened by QR with zero registration. The token is the
 * only credential — it shows a curated snapshot (streak, this week's
 * completions, the coach's line) and three boost buttons. No chore
 * details, no balance, no photos: only what a family would share over
 * the phone.
 */
export const BoostPage: React.FC = () => {
  const { t } = useTranslation();
  const { token = '' } = useParams<{ token: string }>();

  const [snap, setSnap] = useState<BoostSnapshot | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [loading, setLoading] = useState(true);
  const [sending, setSending] = useState(0);
  const [justSent, setJustSent] = useState<number | null>(null);
  const [cap, setCap] = useState<CapState | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let alive = true;
    api.boost.page(token)
      .then(s => { if (alive) setSnap(s); })
      .catch(err => {
        if (!alive) return;
        if (err instanceof APIError && err.status === 404) setNotFound(true);
        else setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => { if (alive) setLoading(false); });
    return () => { alive = false; };
  }, [token]);

  const handleBoost = async (amount: number) => {
    setSending(amount);
    setError('');
    setCap(null);
    try {
      const fresh = await api.boost.send(token, amount);
      setSnap(fresh);
      setJustSent(amount);
      setTimeout(() => setJustSent(null), 2500);
    } catch (err) {
      if (err instanceof APIError) {
        if (err.status === 404) setNotFound(true);
        else if (err.status === 422) {
          // The caps: which one ran out decides the message.
          setCap({ monthly: /monthly/i.test(err.message), daily: !/monthly/i.test(err.message) });
        } else setError(err.message);
      } else {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
    setSending(0);
  };

  if (loading) {
    return <HouseScope persistent className={styles.root}><main className={styles.page} /></HouseScope>;
  }

  // Revoked, expired or unknown: all read the same — "no longer available".
  if (notFound || !snap) {
    return (
      <HouseScope persistent className={styles.root}>
        <main className={styles.page}>
          <div className={styles.card}>
            <span className={styles.well} data-kind="error"><Icon name="lock" size={30} /></span>
            <h1 className={styles.title}>{t('boost.invalidTitle')}</h1>
            <p className={styles.text}>{t('boost.invalidBody')}</p>
          </div>
        </main>
      </HouseScope>
    );
  }

  const leftThisMonth = Math.max(0, snap.monthly_cap - snap.granted_this_month);

  return (
    <HouseScope persistent className={styles.root}>
      <main className={styles.page}>
        <header className={styles.head}>
          <span className={styles.well}><Icon name="spark" size={30} /></span>
          <div>
            <p className={styles.kicker}>{t('boost.kicker')}</p>
            <p className={styles.for}>{t('boost.for', { name: snap.kid_name })}</p>
          </div>
        </header>

        {/* The curated snapshot: only what a family would tell over the phone. */}
        <div className={styles.card}>
          <h1 className={styles.title}>{t('boost.helloTitle', { label: snap.label || t('boost.defaultLabel') })}</h1>
          <ul className={styles.stats}>
            <li>
              <span className={styles.statNum}>🔥 {snap.current_streak}</span>
              <span className={styles.statLabel}>{t('boost.statStreak')}</span>
            </li>
            <li>
              <span className={styles.statNum}>✅ {snap.week_completed}</span>
              <span className={styles.statLabel}>{t('boost.statWeek')}</span>
            </li>
          </ul>
          {snap.coach_line && <p className={styles.coachLine}>"{snap.coach_line}"</p>}
        </div>

        {/* The boost buttons: three tiers, sized for any finger. */}
        <div className={styles.card}>
          <p className={styles.sectionLabel}>{t('boost.cheerTitle')}</p>
          <div className={styles.amounts}>
            {snap.amounts.map(amount => (
              <button
                key={amount}
                type="button"
                className={styles.amountBtn}
                onClick={() => handleBoost(amount)}
                disabled={sending !== 0 || leftThisMonth < amount}
                aria-busy={sending === amount}
              >
                <span className={styles.amountEmoji} aria-hidden>{AMOUNT_EMOJI[amount] ?? '⭐'}</span>
                <span className={styles.amountText}>+{amount}</span>
              </button>
            ))}
          </div>
          <p className={styles.quota} role="status">
            {t('boost.quotaLine', { left: leftThisMonth, cap: snap.monthly_cap })}
          </p>

          {justSent !== null && (
            <p className={styles.sent} role="status" data-testid="boost-sent">
              {t('boost.sentToast', { amount: justSent })}
            </p>
          )}
          {cap?.monthly && <p className={styles.capNote} role="alert">{t('boost.capMonthly')}</p>}
          {cap?.daily && <p className={styles.capNote} role="alert">{t('boost.capDaily')}</p>}
          {error && <p className={styles.capNote} role="alert">{error}</p>}
        </div>

        <p className={styles.footnote}>{t('boost.footnote')}</p>
      </main>
    </HouseScope>
  );
};
