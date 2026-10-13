import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { X } from 'lucide-react';
import { api } from '../../api';
import { Icon, catFromCategory } from '../../design';
import type { CompletionAttachment, PendingCompletion, User } from '../../types';
import { CategoryLabel, IconWell, PersonName } from './pickers';
import ui from './ui.module.css';
import styles from './ApprovalList.module.css';

// Before/after pair: the parent's example photo vs. the kid's completion
// shot, each taking half the card width.

interface Props {
  pending: PendingCompletion[];
  users: User[];
  /** Called after an approve or reject so the parent can reload. */
  onChanged: () => void;
}

function formatDay(date: string, lang: string): string {
  return new Date(date + 'T00:00:00').toLocaleDateString(lang, { weekday: 'short', month: 'short', day: 'numeric' });
}

/** Cells shown before the "+N" fold; the rest expand on tap (OD: 3 + fold). */
const FOLDED_AT = 4;

/**
 * The kid's whole proof set: a grid of attachment cells, each with its own
 * AI verdict badge (✅ 92% / ⚠ 76% 存疑) when a per-attachment review has
 * run — Canvas's per-submission-comment model. Past FOLDED_AT the rest
 * collapse behind a "+N" cell.
 */
const AttachmentGrid: React.FC<{ completionId: number }> = ({ completionId }) => {
  const { t } = useTranslation();
  const [atts, setAtts] = useState<CompletionAttachment[] | null>(null);
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    let live = true;
    api.chores.listAttachments(completionId)
      .then(a => { if (live) setAtts(a); })
      .catch(() => { if (live) setAtts([]); });
    return () => { live = false; };
  }, [completionId]);

  if (atts === null || atts.length === 0) return null;
  const shown = expanded ? atts : atts.slice(0, FOLDED_AT);
  const folded = atts.length - shown.length;

  return (
    <div>
      <p className={styles.gridHead}>
        {t('admin.approvalsTab.allAttachments', { count: atts.length })}
        <span>{t('admin.approvalsTab.perAttachmentHint')}</span>
      </p>
      <div className={styles.attGrid} data-test="approval-grid" role="group" aria-label={t('admin.approvalsTab.gridLabel', { count: atts.length })}>
        {shown.map((a, i) => {
          const reviewed = a.ai_complete != null;
          const good = a.ai_complete === true;
          return (
            <a
              key={a.id}
              className={styles.attCell}
              data-test={`approval-att-${i + 1}`}
              href={a.file_path}
              target="_blank"
              rel="noreferrer"
              aria-label={t('admin.approvalsTab.attAria', { index: i + 1, name: a.display_name })}
            >
              {a.mime_class === 'image' && <img src={a.file_path} alt={a.display_name} loading="lazy" />}
              {a.mime_class === 'video' && <span className={styles.attDoc}><Icon name="film" /></span>}
              {a.mime_class === 'pdf' && <span className={styles.attDoc}><Icon name="book" /></span>}
              {reviewed && (
                <span
                  className={`${styles.attBadge} ${good ? styles.attBadgeGood : styles.attBadgeWarn}`}
                  data-test="approval-att-badge"
                  title={a.ai_feedback}
                >
                  {good ? '✅' : '⚠'} {Math.round(a.ai_confidence * 100)}%{!good ? ` ${t('admin.approvalsTab.flagged')}` : ''}
                </span>
              )}
              <span className={styles.attName}>{a.display_name}</span>
            </a>
          );
        })}
        {folded > 0 && (
          <button
            type="button"
            className={styles.attFold}
            data-test="approval-att-fold"
            onClick={() => setExpanded(true)}
            aria-label={t('admin.approvalsTab.expandFold', { count: folded })}
          >
            <b>＋{folded}</b>
            <span>{t('admin.approvalsTab.expand')}</span>
          </button>
        )}
      </div>
    </div>
  );
};

/**
 * Chores waiting for a grown-up: who, what, the proof (before/after pair
 * plus the full attachment grid) and the AI review's advisory note, with
 * Approve / Reject. Parents open the app mostly for this.
 */
export const ApprovalList: React.FC<Props> = ({ pending, users, onChanged }) => {
  const { t, i18n } = useTranslation();
  const [busy, setBusy] = useState<number | null>(null);
  const [error, setError] = useState('');

  const act = async (id: number, action: 'approve' | 'reject') => {
    if (action === 'reject' && !confirm(t('admin.approvalsTab.rejectConfirm'))) return;
    setBusy(id);
    setError('');
    try {
      if (action === 'approve') await api.chores.approve(id);
      else await api.chores.reject(id);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
    setBusy(null);
    onChanged();
  };

  return (
    <div className={styles.list}>
      {error && <p className={ui.msgError} role="alert">{error}</p>}
      {pending.map(p => {
        const assignee = users.find(u => u.id === p.assigned_user_id);
        const byOther = p.completed_by != null && p.completed_by !== p.assigned_user_id;
        const cat = catFromCategory(p.category);
        return (
          <article key={p.id} className={styles.card} aria-label={t('admin.approvalsTab.cardLabel', { title: p.chore_title, name: assignee?.name ?? p.child_name })}>
            <div className={styles.head}>
              <PersonName user={assignee} name={p.child_name} />
              <span className={styles.when}>{formatDay(p.completion_date, i18n.language)}</span>
            </div>

            <div className={styles.chore}>
              <IconWell icon={p.icon} cat={cat} />
              <div className={ui.rowMain}>
                <h3 className={styles.title}>{p.chore_title}</h3>
                <div className={ui.rowMeta}>
                  {p.category && <span><CategoryLabel category={p.category} /></span>}
                  {p.points_value != null && (
                    <span><Icon name="star" />{t('admin.choresTab.points', { count: p.points_value })}</span>
                  )}
                  {byOther && <span>{t('admin.approvalsTab.tickedOffBy', { name: p.child_name })}</span>}
                </div>
              </div>
            </div>

            {/* The full proof set first: every attachment in a grid, each
                with its own AI verdict. Falls back to the legacy single
                photo pair below when the completion predates attachments. */}
            <AttachmentGrid completionId={p.id} />

            {!p.photo_url && p.reference_photo_url ? (
              <a className={styles.photo} href={p.reference_photo_url} target="_blank" rel="noreferrer">
                <img src={p.reference_photo_url} alt={t('admin.approvalsTab.beforeAfterRef')} loading="lazy" />
                <span className={styles.photoHint}><Icon name="book" />{t('admin.approvalsTab.beforeAfterRef')}</span>
              </a>
            ) : null}

            {p.requires_photo && !p.photo_url && !p.reference_photo_url && (
              // A "child" photo chore pending without a photo was finished
              // with "No photo? Finish anyway"; otherwise it may still come.
              <div className={styles.ai}>
                <Icon name="camera" />
                <div>
                  <span className={styles.aiLabel}>
                    {p.photo_source === 'child' || !p.photo_source
                      ? t('admin.approvalsTab.noPhotoSkipped')
                      : t('admin.approvalsTab.noPhotoYet')}
                  </span>
                  <p className={styles.aiText}>
                    {p.photo_source === 'child' || !p.photo_source
                      ? t('admin.approvalsTab.noPhotoSkippedHint', { name: p.child_name })
                      : t('admin.approvalsTab.noPhotoYetHint')}
                  </p>
                </div>
              </div>
            )}

            {p.ai_feedback && p.ai_complete != null && (
              // The photo check only advises; the parent still decides.
              <div className={styles.ai}>
                <Icon name="spark" />
                <div>
                  <span className={styles.aiLabel}>
                    {p.ai_complete ? t('admin.approvalsTab.aiLooksDone') : t('admin.approvalsTab.aiLooksNotDone')}
                    {' · '}
                    {t('admin.approvalsTab.aiConfidence', { value: Math.round((p.ai_confidence ?? 0) * 100) })}
                  </span>
                  <p className={styles.aiText}>{p.ai_feedback}</p>
                </div>
              </div>
            )}

            <div className={styles.actions}>
              <button
                type="button"
                className={ui.btnPrimary}
                onClick={() => act(p.id, 'approve')}
                disabled={busy === p.id}
              >
                <Icon name="check" /> {t('admin.approvalsTab.approveButton')}
              </button>
              <button
                type="button"
                className={ui.btnGhost}
                onClick={() => act(p.id, 'reject')}
                disabled={busy === p.id}
              >
                <X aria-hidden /> {t('admin.approvalsTab.rejectButton')}
              </button>
            </div>
          </article>
        );
      })}
    </div>
  );
};
