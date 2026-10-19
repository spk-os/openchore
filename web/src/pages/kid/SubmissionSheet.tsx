// 问题①: the kid's own record of what they handed in. Completing a chore
// used to be a one-way door — the photo/recording/content disappeared into
// the parent's approvals, and the kid had no way to look back at their own
// submission. This sheet is that look-back: the completion's attachments
// (images inline, video/audio/pdf as tappable previews), the finish time
// and the AI's feedback note, all from data the API already serves.
import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { CompletionAttachment, MimeClass, ScheduledChore } from '../../types';
import { Icon } from '../../design';
import { Sheet } from './Sheet';
import s from './kid.module.css';

/** An image/preview pair, ordered the way the sheet shows them. */
type Preview =
  | { kind: 'image'; url: string; name: string }
  | { kind: 'video'; url: string; name: string }
  | { kind: 'audio'; url: string; name: string }
  | { kind: 'pdf'; url: string; name: string };

const previewOf = (a: CompletionAttachment): Preview => ({
  kind: a.mime_class as Preview['kind'],
  url: a.file_path,
  name: a.display_name,
});

interface SubmissionSheetProps {
  chore: ScheduledChore;
  onClose: () => void;
}

export function SubmissionSheet({ chore, onClose }: SubmissionSheetProps) {
  const { t } = useTranslation();
  const [attachments, setAttachments] = useState<CompletionAttachment[] | null>(null);
  const [failed, setFailed] = useState(false);
  // The tapped attachment, shown full-screen (an image lightbox; other
  // classes open their inline player below the grid instead).
  const [zoomed, setZoomed] = useState<Preview | null>(null);

  useEffect(() => {
    if (!chore.completion_id) {
      setAttachments([]);
      return;
    }
    let live = true;
    api.chores.listAttachments(chore.completion_id)
      .then(atts => { if (live) setAttachments(atts); })
      .catch(() => { if (live) { setFailed(true); setAttachments([]); } });
    return () => { live = false; };
  }, [chore.completion_id]);

  const previews = useMemo(() => (attachments ?? []).map(previewOf), [attachments]);

  const doneAt = chore.completed_at ? new Date(chore.completed_at) : null;
  const doneAtLine = doneAt && !Number.isNaN(doneAt.getTime())
    ? doneAt.toLocaleString()
    : null;

  // The legacy single photo (pre-L0 completions) still counts as proof.
  const legacyPhoto = chore.photo_url && !previews.some(p => p.url === chore.photo_url)
    ? chore.photo_url
    : null;

  return (
    <Sheet title={t('kid.submission.title')} onClose={onClose}>
      <p className={s.sheetLead}>
        <strong>{chore.title}</strong>
        <span>
          {doneAtLine
            ? t('kid.submission.doneAt', { time: doneAtLine })
            : t('kid.submission.doneLabel')}
        </span>
      </p>

      {chore.ai_feedback && (
        <div className={s.subAiNote} data-test="submission-ai-note">
          <span className={s.subAiTitle}><Icon name="spark" size={16} /> {t('kid.submission.aiNote')}</span>
          <p>{chore.ai_feedback}</p>
        </div>
      )}

      <p className={s.attsCaption}>{t('kid.submission.attsCaption')}</p>

      {attachments === null && (
        <p className={s.helpLine} role="status">{t('kid.submission.loading')}</p>
      )}

      {attachments !== null && failed && (
        <p className={s.errorLine} role="alert">{t('kid.submission.loadFailed')}</p>
      )}

      {attachments !== null && previews.length === 0 && !legacyPhoto && (
        <div className={s.subEmpty} data-test="submission-empty">
          <Icon name="camera" />
          <p>{t('kid.submission.emptyText')}</p>
        </div>
      )}

      {attachments !== null && (previews.length > 0 || !!legacyPhoto) && (
        <div className={s.atts} data-test="submission-grid" role="group">
          {legacyPhoto && (
            <button
              type="button"
              className={s.att}
              data-test="submission-att-legacy"
              onClick={() => setZoomed({ kind: 'image', url: chore.photo_url!, name: t('kid.submission.legacyPhoto') })}
              aria-label={t('kid.submission.legacyPhoto')}
            >
              <span className={s.attBadge}>{t('kid.photo.cls_image')}</span>
              <img src={legacyPhoto} alt="" className={s.attThumb} loading="lazy" />
            </button>
          )}
          {previews.map((p, i) => (
            <button
              key={i}
              type="button"
              className={s.att}
              data-test={`submission-att-${i + 1}`}
              onClick={() => setZoomed(p)}
              aria-label={t('kid.photo.attAria', { index: i + 1, name: p.name, cls: t(`kid.photo.cls_${p.kind}` as MimeClass) })}
            >
              <span className={s.attBadge}>{t(`kid.photo.cls_${p.kind}` as MimeClass)}</span>
              {p.kind === 'image' && <img src={p.url} alt="" className={s.attThumb} loading="lazy" />}
              {p.kind === 'video' && <span className={s.attDocIcon}><Icon name="film" /></span>}
              {p.kind === 'audio' && <span className={s.attDocIcon}><Icon name="mic" /></span>}
              {p.kind === 'pdf' && <span className={s.attDocIcon}><Icon name="book" /></span>}
              <span className={s.attName}>{p.name}</span>
            </button>
          ))}
        </div>
      )}

      {/* Non-image previews play inline under the grid; images zoom. */}
      {zoomed && zoomed.kind !== 'image' && (
        <div className={s.subPlayer} data-test="submission-player">
          {zoomed.kind === 'video' && <video src={zoomed.url} controls playsInline />}
          {zoomed.kind === 'audio' && <audio src={zoomed.url} controls />}
          {zoomed.kind === 'pdf' && (
            <object data={zoomed.url} type="application/pdf" aria-label={zoomed.name}>
              <a href={zoomed.url} target="_blank" rel="noreferrer">{t('kid.submission.openPdf')}</a>
            </object>
          )}
          <p className={s.subPlayerName}>{zoomed.name}</p>
        </div>
      )}

      <p className={s.helpLine}>{t('kid.submission.hint')}</p>

      {zoomed && zoomed.kind === 'image' && (
        <div
          className={s.refZoom}
          role="dialog"
          aria-modal="true"
          aria-label={zoomed.name}
          onClick={() => setZoomed(null)}
        >
          <img src={zoomed.url} alt={zoomed.name} />
          <p>{t('kid.photo.referenceZoomHint')}</p>
        </div>
      )}
    </Sheet>
  );
}
