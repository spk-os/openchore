import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { CompletionAttachment, MimeClass, ScheduledChore } from '../../types';
import { Icon } from '../../design';
import { Sheet } from './Sheet';
import s from './kid.module.css';
// Only this sheet draws a QR code; keep the encoder out of the main bundle.
const QRCodeSVG = React.lazy(() => import('qrcode.react').then(m => ({ default: m.QRCodeSVG })));

/** Product caps: 9 photos, or 1 video, or 1 PDF — one proof stream. */
const MAX_IMAGES = 9;
const MAX_VIDEOS = 1;
const MAX_PDFS = 1;
/** Grid cells always shown (3×3); the rest of the proof rides in the same list. */
const GRID_CELLS = 9;

type Att = { id: number; file: File; mimeClass: MimeClass };

function mimeClassOf(file: File): MimeClass {
  if (file.type.startsWith('video/')) return 'video';
  if (file.type === 'application/pdf') return 'pdf';
  return 'image';
}

/**
 * One queued proof file. Until it is uploaded it has a temporary negative
 * id; after the batch POST the real attachment replaces it. Names cap at
 * one short line — the grid cell is small.
 */
function makeAtt(id: number, file: File): Att {
  return { id, file, mimeClass: mimeClassOf(file) };
}

interface PhotoSheetProps {
  chore: ScheduledChore;
  userId: number;
  /** The household's public base URL (admin setting), for the QR link. */
  baseUrl?: string;
  onClose: () => void;
  /** The photo arrived and the chore is finished (or the proof attached). */
  onComplete: () => void;
}

/**
 * Photo proof for a chore: a 3×3 grid of attachments (Canvas's
 * submission[file_ids][] model) picked through the camera, the album or
 * the file picker, sent in one batch. Scan a QR code with another device
 * for the single-photo flow; with no photo to hand, the chore can be
 * finished anyway and waits for a grown-up. AI reviews each image on its
 * own once the parent's side sees it.
 */
export function PhotoSheet({ chore, userId, baseUrl, onClose, onComplete }: PhotoSheetProps) {
  const { t } = useTranslation();
  const [uploading, setUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  // The phone that scans the code isn't signed in; the link carries a
  // short-lived token that can only upload a photo and complete this chore.
  const [uploadToken, setUploadToken] = useState<string | null>(null);
  // The multi-file queue: local picks waiting for the submit, plus what is
  // already attached when the chore was completed earlier (photo_source
  // "both"/"external" lets proof arrive after the fact).
  const [queue, setQueue] = useState<Att[]>([]);
  const [attached, setAttached] = useState<CompletionAttachment[]>([]);
  const alreadyCompleted = chore.completed;
  const fileRef = useRef<HTMLInputElement>(null);
  const videoRef = useRef<HTMLInputElement>(null);
  const docRef = useRef<HTMLInputElement>(null);
  const [refZoom, setRefZoom] = useState(false);
  const tempId = useRef(-1);

  useEffect(() => {
    api.auth.uploadLink(chore.schedule_id)
      .then(r => setUploadToken(r.token))
      .catch(() => setUploadToken(null));
  }, [chore.schedule_id]);

  // When the chore already has a completion, show its live attachments so
  // the kid sees what they already handed in instead of starting blind.
  useEffect(() => {
    if (!chore.completion_id) return;
    let live = true;
    api.chores.listAttachments(chore.completion_id)
      .then(atts => { if (live) setAttached(atts); })
      .catch(() => { /* the completion may predate attachments; the queue is still usable */ });
    return () => { live = false; };
  }, [chore.completion_id]);

  // Poll for the photo (or the completion) arriving from the other device.
  useEffect(() => {
    const interval = setInterval(async () => {
      try {
        const chores = await api.users.getChores(userId, 'daily', chore.date);
        const updated = chores.find(c => c.schedule_id === chore.schedule_id);
        if (alreadyCompleted ? updated?.photo_url : updated?.completed) onComplete();
      } catch (e) {
        console.error('Polling failed:', e);
      }
    }, 3000);
    return () => clearInterval(interval);
  }, [chore, userId, alreadyCompleted, onComplete]);

  const origin = baseUrl || window.location.origin;
  const uploadUrl = `${origin}/upload?scheduleId=${chore.schedule_id}&date=${chore.date}&userId=${userId}` +
    (uploadToken ? `&t=${encodeURIComponent(uploadToken)}` : '');

  const attachedByClass = useMemo(() => {
    const counts = { image: 0, video: 0, pdf: 0 } as Record<MimeClass, number>;
    for (const a of attached) counts[a.mime_class]++;
    for (const q of queue) counts[q.mimeClass]++;
    return counts;
  }, [attached, queue]);

  /** Room left per class, counting both the queued and the already-attached. */
  const roomFor = useCallback((cls: MimeClass): number => {
    switch (cls) {
      case 'image': return MAX_IMAGES - attachedByClass.image;
      case 'video': return MAX_VIDEOS - attachedByClass.video;
      case 'pdf': return MAX_PDFS - attachedByClass.pdf;
    }
  }, [attachedByClass]);

  // The shared picker: several files at once, each filtered by its class
  // cap and the kind the sheet is for (the camera button stays
  // photo-only). Files over the cap are refused with a clear line, not
  // silently dropped.
  const pickFiles = (files: FileList | null, accept?: MimeClass) => {
    if (!files || files.length === 0) return;
    setUploadError(null);
    const next: Att[] = [...queue];
    const rejected: string[] = [];
    for (const f of Array.from(files)) {
      const cls = accept ?? mimeClassOf(f);
      if (accept && mimeClassOf(f) !== accept && accept === 'image') {
        // Camera pickers sometimes hand back a stray non-image; keep those out.
        rejected.push(f.name);
        continue;
      }
      if (roomFor(cls) <= 0 || next.some(q => q.mimeClass === cls && (cls === 'video' || cls === 'pdf'))) {
        rejected.push(f.name);
        continue;
      }
      if (cls === 'image' && next.length + attached.length >= MAX_IMAGES) {
        rejected.push(f.name);
        continue;
      }
      next.push(makeAtt(tempId.current--, f));
    }
    setQueue(next);
    if (rejected.length > 0) {
      setUploadError(t('kid.photo.tooMany', { names: rejected.join('、') }));
    }
  };

  const handleCameraPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    pickFiles(e.target.files, 'image');
    e.target.value = '';
  };
  const handleAlbumPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    pickFiles(e.target.files);
    e.target.value = '';
  };
  const handleDocPick = (e: React.ChangeEvent<HTMLInputElement>) => {
    pickFiles(e.target.files);
    e.target.value = '';
  };

  const removeQueued = (id: number) => {
    setQueue(q => q.filter(a => a.id !== id));
  };

  /**
   * Submit: finish the chore if it isn't yet (photo_source "child" is
   * satisfied through the explicit skip flag; the whole batch then follows
   * through the multi-file endpoint, whose first image is promoted to
   * photo_url server-side), then send the queued files in one request.
   */
  const handleSubmit = async () => {
    setUploading(true);
    setUploadError(null);
    try {
      let completionId = chore.completion_id;
      if (!alreadyCompleted) {
        const res = await api.chores.complete(chore.schedule_id, chore.date, undefined, { skipPhoto: true });
        completionId = res.id ?? undefined;
      }
      if (completionId && queue.length > 0) {
        await api.chores.uploadAttachments(completionId, queue.map(a => a.file));
      }
      onComplete();
    } catch (err) {
      setUploadError((err as Error)?.message || t('kid.photo.uploadFailed'));
    } finally {
      setUploading(false);
    }
  };

  // No photo to hand: finish anyway, and a grown-up checks it instead.
  const handleSkipPhoto = async () => {
    setUploading(true);
    setUploadError(null);
    try {
      await api.chores.complete(chore.schedule_id, chore.date, undefined, { skipPhoto: true });
      onComplete();
    } catch (err) {
      setUploadError((err as Error)?.message || t('kid.photo.skipFailed'));
    } finally {
      setUploading(false);
    }
  };

  const handleCopyLink = async () => {
    try {
      await navigator.clipboard.writeText(uploadUrl);
    } catch {
      // Older browsers: copy through a temporary input.
      const input = document.createElement('input');
      input.value = uploadUrl;
      document.body.appendChild(input);
      input.select();
      document.execCommand('copy');
      document.body.removeChild(input);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const queuedCount = queue.length;
  const totalAtts = attached.length + queuedCount;
  const gridAtts: (CompletionAttachment | Att)[] = [...attached, ...queue];
  const canSubmit = !uploading && queuedCount > 0;

  return (
    <Sheet title={alreadyCompleted ? t('kid.photo.titleAdd') : t('kid.photo.titleNeeded')} onClose={onClose}>
      <p className={s.sheetLead}>
        <strong>{chore.title}</strong>
        <span>{t('kid.photo.scanOrUpload')}</span>
      </p>

      {/* The parent's example photo: look at what "done" looks like before shooting. */}
      {chore.reference_photo_url && (
        <figure className={s.refPhoto} data-testid="reference-photo">
          <figcaption>
            <Icon name="book" />
            {t('kid.photo.referenceTitle')}
          </figcaption>
          <img
            src={chore.reference_photo_url}
            alt={t('kid.photo.referenceAlt', { title: chore.title })}
            loading="lazy"
            onClick={() => setRefZoom(true)}
          />
          <p className={s.helpLine}>{t('kid.photo.referenceHelp')}</p>
        </figure>
      )}

      {/* L0: the attachment grid — picked files plus what is already
          attached, one kind per completion (9 photos / 1 video / 1 PDF). */}
      <p className={s.attsCaption}>{t('kid.photo.attsCaption')}</p>
      <div className={s.atts} data-test="photo-grid" role="group" aria-label={t('kid.photo.gridLabel', { count: totalAtts })}>
        {gridAtts.slice(0, GRID_CELLS).map((a, i) => {
          const isQueued = 'file' in a;
          const name = isQueued ? (a as Att).file.name : (a as CompletionAttachment).display_name;
          const cls = isQueued ? (a as Att).mimeClass : (a as CompletionAttachment).mime_class;
          return (
            <div key={isQueued ? `q${(a as Att).id}` : `a${(a as CompletionAttachment).id}`} className={s.att} data-test={`photo-att-${i + 1}`} role="img" aria-label={t('kid.photo.attAria', { index: i + 1, name, cls: t(`kid.photo.cls_${cls}`) })}>
              <span className={s.attBadge}>{t(`kid.photo.cls_${cls}`)}</span>
              {cls === 'image' && isQueued && <img src={URL.createObjectURL((a as Att).file)} alt="" className={s.attThumb} />}
              {cls === 'image' && !isQueued && <img src={(a as CompletionAttachment).file_path} alt="" className={s.attThumb} />}
              {cls === 'video' && <span className={s.attDocIcon}><Icon name="film" /></span>}
              {cls === 'pdf' && <span className={s.attDocIcon}><Icon name="book" /></span>}
              <button type="button" className={s.attDel} title={t('kid.photo.removeAtt')} aria-label={t('kid.photo.removeAttAria', { name })} onClick={() => isQueued ? removeQueued((a as Att).id) : undefined} disabled={!isQueued || uploading}>
                <Icon name="plus" />
              </button>
              <span className={s.attName}>{name}</span>
            </div>
          );
        })}
        {totalAtts < GRID_CELLS && (
          <button type="button" className={s.attAdd} data-test="photo-add-cell" aria-label={t('kid.photo.addMore')} onClick={() => fileRef.current?.click()} disabled={uploading}>＋</button>
        )}
        {Array.from({ length: Math.max(0, GRID_CELLS - totalAtts - 1) }, (_, i) => (
          <span key={`e${i}`} className={s.attEmpty} aria-hidden="true" />
        ))}
      </div>

      <div className={s.ats} data-test="photo-count">
        <b>{t('kid.photo.picked', { count: totalAtts })}</b>
        <span>{t('kid.photo.capsHint')}</span>
      </div>

      {/* The code sits on a House "paper" in every skin: scanners need dark on light. */}
      <div className={s.qrPaper} data-theme="house">
        <React.Suspense fallback={<span className={s.spinner} role="status" aria-label={t('kid.photo.preparing')} />}>
          {uploadToken
            ? <QRCodeSVG value={uploadUrl} size={224} marginSize={2} bgColor="transparent" fgColor="currentColor" title={t('kid.photo.qrLabel')} />
            : <span className={s.spinner} role="status" aria-label={t('kid.photo.preparing')} />}
        </React.Suspense>
      </div>

      <div className={s.sheetActions}>
        <button type="button" className={s.primaryAction} onClick={() => fileRef.current?.click()} disabled={uploading}>
          <Icon name="camera" />
          {uploading ? t('kid.photo.uploading') : t('kid.photo.takePhoto')}
        </button>
        <input ref={fileRef} type="file" accept="image/*" capture="environment" onChange={handleCameraPick} hidden />
        <button type="button" className={s.secondaryAction} onClick={() => videoRef.current?.click()} disabled={uploading}>
          <Icon name="film" />
          {t('kid.photo.takeVideo')}
        </button>
        <input ref={videoRef} type="file" accept="video/*" multiple onChange={handleAlbumPick} hidden />
        <button type="button" className={s.secondaryAction} onClick={() => docRef.current?.click()} disabled={uploading}>
          <Icon name="book" />
          {t('kid.photo.uploadFile')}
        </button>
        <input ref={docRef} type="file" accept="video/*,application/pdf,image/*" multiple onChange={handleDocPick} hidden />
        <button type="button" className={s.secondaryAction} onClick={handleCopyLink}>
          {copied ? t('kid.photo.copied') : t('kid.photo.copyLink')}
        </button>
      </div>

      {uploadError && <p className={s.errorLine} role="alert">{uploadError}</p>}

      <p className={s.waitingLine} role="status">
        <span className={s.spinner} aria-hidden />
        {t('kid.photo.waiting')}
      </p>

      <div className={s.submitRow}>
        <button type="button" className={s.secondaryAction} onClick={onClose} disabled={uploading}>
          {t('kid.photo.thinkAgain')}
        </button>
        <button type="button" className={s.submitAction} data-test="photo-submit" onClick={handleSubmit} disabled={!canSubmit} aria-label={t('kid.photo.submitAria', { count: queuedCount })}>
          {uploading
            ? t('kid.photo.uploading')
            : alreadyCompleted
              ? t('kid.photo.submitAdd', { count: queuedCount })
              : t('kid.photo.submit', { count: queuedCount })}
        </button>
      </div>
      <p className={s.helpLine}>{alreadyCompleted ? t('kid.photo.helpAdd') : t('kid.photo.helpComplete')}</p>
      <p className={s.helpLine}>{t('kid.photo.reviewNote')}</p>

      {!alreadyCompleted && (
        <div className={s.skipPhoto}>
          <button type="button" className={s.skipAction} onClick={handleSkipPhoto} disabled={uploading}>
            {t('kid.photo.skipPhoto')}
          </button>
          <p className={s.helpLine}>{t('kid.photo.skipPhotoHelp')}</p>
        </div>
      )}

      {/* Tap the example to see it full-screen. */}
      {refZoom && chore.reference_photo_url && (
        <div className={s.refZoom} role="dialog" aria-modal="true" aria-label={t('kid.photo.referenceAlt', { title: chore.title })} onClick={() => setRefZoom(false)}>
          <img src={chore.reference_photo_url} alt={t('kid.photo.referenceAlt', { title: chore.title })} />
          <p>{t('kid.photo.referenceZoomHint')}</p>
        </div>
      )}
    </Sheet>
  );
}
