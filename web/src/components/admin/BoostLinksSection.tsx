import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { HandHeart, QrCode, Trash2 } from 'lucide-react';
import { api, APIError } from '../../api';
import type { BoostLink } from '../../types';
import { Icon } from '../../design';
import ui from './ui.module.css';
import styles from './BoostLinksSection.module.css';

// Only the share card draws a QR code; keep the encoder out of the main bundle.
const QRCodeSVG = React.lazy(() => import('qrcode.react').then(m => ({ default: m.QRCodeSVG })));

/**
 * Grandparent boost links for one kid (P4-16): mint a token-addressed page
 * for a grown-up outside the household, share it once (QR + copy), then
 * watch it from the list and revoke it when the sharing is over. The token
 * comes back from the create call only — after that the list knows a link
 * by its label, never by its secret.
 */
export const BoostLinksSection: React.FC<{ userId: number }> = ({ userId }) => {
  const { t, i18n } = useTranslation();
  const [links, setLinks] = useState<BoostLink[] | null>(null);
  const [label, setLabel] = useState('');
  const [creating, setCreating] = useState(false);
  // The freshly minted link, with its token: shown once, then gone.
  const [freshLink, setFreshLink] = useState<BoostLink | null>(null);
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState('');
  const [baseUrl, setBaseUrl] = useState('');

  const load = async () => {
    try {
      setLinks(await api.admin.listBoostLinks(userId));
    } catch (err) {
      setError(err instanceof APIError ? err.message : String(err));
    }
  };

  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [userId]);

  // The household's public URL for QR links (a grown-up setting; falls back
  // to this device's origin).
  useEffect(() => {
    api.admin.getSetting('base_url')
      .then(d => setBaseUrl(d.value))
      .catch(() => {});
  }, []);

  const handleCreate = async () => {
    setCreating(true);
    setError('');
    try {
      const link = await api.admin.createBoostLink(userId, label.trim());
      setFreshLink(link);
      setLabel('');
      load();
    } catch (err) {
      setError(err instanceof APIError ? err.message : String(err));
    }
    setCreating(false);
  };

  const handleRevoke = async (link: BoostLink) => {
    if (!confirm(t('admin.usersTab.boostConfirmRevoke', { label: link.label }))) return;
    setError('');
    try {
      await api.admin.revokeBoostLink(userId, link.id);
      load();
    } catch (err) {
      setError(err instanceof APIError ? err.message : String(err));
    }
  };

  const freshUrl = freshLink?.token
    ? `${baseUrl || window.location.origin}/boost/${freshLink.token}`
    : '';
  const freshExpires = freshLink
    ? new Date(freshLink.expires_at).toLocaleDateString(i18n.language)
    : '';

  const handleCopy = async () => {
    if (!freshUrl) return;
    try {
      await navigator.clipboard.writeText(freshUrl);
    } catch {
      // Older browsers: copy through a temporary input.
      const input = document.createElement('input');
      input.value = freshUrl;
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      document.body.removeChild(input);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  // This month's tally only: a stale month's granted count reads as zero.
  const grantedThisMonth = (link: BoostLink) =>
    link.granted_month === new Date().toISOString().slice(0, 7) ? link.granted : 0;

  const fmtDate = (iso: string) => new Date(iso).toLocaleDateString(i18n.language);

  if (links === null) {
    return (
      <div className={ui.inset}>
        <p className={ui.emptyInline}>{t('admin.usersTab.loading')}</p>
      </div>
    );
  }

  return (
    <div className={ui.inset}>
      <h4 className={ui.eyebrow}>{t('admin.usersTab.boostTitle')}</h4>
      <p className={ui.help}>{t('admin.usersTab.boostHelp')}</p>

      {/* The one-time share card: the token's only appearance. */}
      {freshLink && (
        <div className={styles.shareCard} data-testid="boost-share-card">
          <div className={styles.qrPaper}>
            <React.Suspense fallback={<span className={styles.spinner} role="status" />}>
              <QRCodeSVG value={freshUrl} size={168} marginSize={2} bgColor="transparent" fgColor="currentColor" />
            </React.Suspense>
          </div>
          <div className={styles.shareBody}>
            <p className={styles.shareHead}>
              <QrCode aria-hidden /> {t('admin.usersTab.boostShareTitle', { label: freshLink.label })}
            </p>
            <p className={ui.help}>{t('admin.usersTab.boostShareOnce')}</p>
            <p className={styles.shareMeta}>{t('admin.usersTab.boostShareExpiry', { date: freshExpires })}</p>
            <div className={styles.shareActions}>
              <button type="button" className={ui.btnPrimary} onClick={handleCopy}>
                {copied ? t('admin.usersTab.boostCopied') : t('admin.usersTab.boostCopyLink')}
              </button>
              <button type="button" className={ui.btnGhost} onClick={() => setFreshLink(null)}>
                {t('admin.usersTab.boostShareDone')}
              </button>
            </div>
          </div>
        </div>
      )}

      <div className={ui.formRow}>
        <label className={ui.field}>
          <span className={ui.label}>{t('admin.usersTab.boostLabelField')}</span>
          <input
            className={ui.input}
            value={label}
            maxLength={60}
            placeholder={t('admin.usersTab.boostLabelPlaceholder')}
            onChange={e => setLabel(e.target.value)}
          />
          <p className={ui.help}>{t('admin.usersTab.boostLabelHelp')}</p>
        </label>
        <div className={styles.createWrap}>
          <button type="button" className={ui.btnPrimary} onClick={handleCreate} disabled={creating}>
            <HandHeart aria-hidden /> {creating ? t('admin.usersTab.boostCreating') : t('admin.usersTab.boostCreateBtn')}
          </button>
        </div>
      </div>

      {error && <p className={ui.msgError} role="alert">{error}</p>}

      {links.length > 0 && (
        <ul className={styles.linkList}>
          {links.map(link => (
            <li key={link.id} className={styles.linkRow}>
              <span className={styles.linkIcon} aria-hidden>
                <Icon name={link.revoked ? 'lock' : 'star'} />
              </span>
              <div className={styles.linkMain}>
                <span className={styles.linkLabel}>{link.label || t('admin.usersTab.boostUnnamed')}</span>
                <span className={styles.linkMeta}>
                  {link.revoked
                    ? t('admin.usersTab.boostRevokedBadge')
                    : t('admin.usersTab.boostMeta', {
                        granted: grantedThisMonth(link),
                        cap: link.monthly_cap,
                        date: fmtDate(link.expires_at),
                      })}
                </span>
              </div>
              {!link.revoked && (
                <button
                  type="button"
                  className={ui.iconBtn}
                  onClick={() => handleRevoke(link)}
                  title={t('admin.usersTab.boostRevokeTitle')}
                  aria-label={t('admin.usersTab.boostRevokeTitle')}
                >
                  <Trash2 aria-hidden />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}

      {links.length === 0 && !freshLink && (
        <p className={ui.help}>{t('admin.usersTab.boostEmpty')}</p>
      )}
    </div>
  );
};
