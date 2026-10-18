import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { Icon } from '../../design';
import Modal from '../Modal/Modal';
import type { MediaClassStat, MediaItem, MediaSubjectStat, MimeClass, User } from '../../types';
import ui from './ui.module.css';
import styles from './MediaTab.module.css';

/**
 * 036: the parent-side media library. Photos, videos and recordings pile
 * up fast once direct recording exists; this tab is the volume answer —
 * every uploaded proof file in one grid, grouped and filtered by subject
 * (the chore's school subject), media class, date and the kid who handed
 * it in. The five-subject vocabulary is the error book's (they share the
 * admin.analysis.subject_* labels).
 */

/** The five school subjects a chore can carry; '' stays a separate chip. */
const SUBJECTS = ['math', 'chinese', 'english', 'physics', 'chemistry'] as const;

const subjectLabel = (t: (k: string) => string, subject: string) =>
  (SUBJECTS as readonly string[]).includes(subject)
    ? t(`admin.analysis.subject_${subject}`)
    : subject;

const CLASSES: { id: MimeClass; labelKey: string }[] = [
  { id: 'image', labelKey: 'admin.mediaTab.cls_image' },
  { id: 'video', labelKey: 'admin.mediaTab.cls_video' },
  { id: 'audio', labelKey: 'admin.mediaTab.cls_audio' },
  { id: 'pdf', labelKey: 'admin.mediaTab.cls_pdf' },
];

const formatSize = (bytes: number): string => {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} MB`;
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(0)} KB`;
  return `${bytes} B`;
};

const formatWhen = (iso: string, lang: string): string =>
  new Date(iso).toLocaleString(lang, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });

/**
 * The in-app preview of one proof file: a video/audio player (or the
 * image itself) inside the page instead of a bare new-tab link — "点视频
 * 直接报错" was the browser leaving the app for a raw file, and a
 * phone browser often has no handler for webm proof.
 */
const MediaPreview: React.FC<{ item: MediaItem; onClose: () => void }> = ({ item, onClose }) => {
  const { t, i18n } = useTranslation();
  const openRaw = (
    <a className={styles.previewRaw} href={item.file_path} target="_blank" rel="noreferrer">
      {t('admin.mediaTab.openRaw')}
    </a>
  );
  return (
    <Modal isOpen onClose={onClose} title={item.display_name} maxWidth="720px">
      <div className={styles.preview} data-test="media-preview">
        {item.mime_class === 'image' && <img className={styles.previewImg} src={item.file_path} alt={item.display_name} />}
        {item.mime_class === 'video' && <video className={styles.previewMedia} src={item.file_path} controls autoPlay playsInline />}
        {item.mime_class === 'audio' && <audio className={styles.previewAudio} src={item.file_path} controls autoPlay />}
        {item.mime_class === 'pdf' && (
          <object className={styles.previewPdf} data={item.file_path} type="application/pdf" aria-label={item.display_name}>
            {openRaw}
          </object>
        )}
        <p className={styles.previewMeta}>
          {item.user_name} · {item.chore_title || t('admin.mediaTab.choreDeleted')}
          {item.subject ? ` · ${subjectLabel(t, item.subject)}` : ''} · {formatWhen(item.created_at, i18n.language)} · {formatSize(item.size_bytes)}
        </p>
        <div className={styles.previewActions}>
          {item.mime_class !== 'pdf' && openRaw}
        </div>
      </div>
    </Modal>
  );
};

export const MediaTab: React.FC = () => {
  const { t, i18n } = useTranslation();
  const [items, setItems] = useState<MediaItem[]>([]);
  const [subjects, setSubjects] = useState<MediaSubjectStat[]>([]);
  const [classes, setClasses] = useState<MediaClassStat[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');

  // The active filters — every one optional, applied server-side.
  const [subject, setSubject] = useState('');
  const [cls, setCls] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [kid, setKid] = useState(0);
  const [users, setUsers] = useState<User[]>([]);
  // The item open in the in-page preview modal; null = no preview. Clicks
  // on a grid cell open this instead of navigating the browser to the raw
  // file ("点视频直接报错" was the browser leaving the app for webm).
  const [preview, setPreview] = useState<MediaItem | null>(null);

  useEffect(() => {
    api.users.list().then(u => setUsers(u.filter(x => x.role === 'child'))).catch(() => {});
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const res = await api.chores.listMedia({
        subject, cls, from, to,
        user_id: kid || undefined,
      });
      setItems(res.items);
      setSubjects(res.subjects);
      setClasses(res.classes);
      setTotal(res.total);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, [subject, cls, from, to, kid]);

  useEffect(() => { load(); }, [load]);

  const classCount = useMemo(
    () => (id: MimeClass) => classes.find(c => c.mime_class === id)?.count ?? 0,
    [classes]);
  const subjectCount = useMemo(
    () => (id: string) => subjects.find(s => s.subject === id)?.count ?? 0,
    [subjects]);
  const hasFilter = subject !== '' || cls !== '' || from !== '' || to !== '' || kid !== 0;

  const chip = (active: boolean) => `${styles.chip} ${active ? styles.chipOn : ''}`;

  return (
    <div className={ui.page}>
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>{t('admin.mediaTab.title')}</h2>
          <p className={ui.pageSub}>{t('admin.mediaTab.subtitle', { count: total })}</p>
        </div>
      </div>

      {/* Subject chips — the five school subjects plus the unclassified
          bucket, each with its live count under the other filters. All
          six always render (even at 0): a missing chip reads as "没有
          做分类", a zero chip reads as "这个科目还没有内容". */}
      <div className={styles.filterRow} data-test="media-subject-filter" role="group" aria-label={t('admin.mediaTab.filterSubject')}>
        <button type="button" className={chip(subject === '')} onClick={() => setSubject('')}>
          {t('admin.mediaTab.allSubjects')} <b>{total}</b>
        </button>
        {SUBJECTS.map(subj => (
          <button key={subj} type="button" className={chip(subject === subj)} data-test={`media-subject-${subj}`} onClick={() => setSubject(subj)}>
            {subjectLabel(t, subj)} <b>{subjectCount(subj)}</b>
          </button>
        ))}
        <button type="button" className={chip(subject === '')} onClick={() => setSubject('')}>
          {t('admin.mediaTab.noSubject')} <b>{subjectCount('')}</b>
        </button>
      </div>

      {/* Media class chips + date range + kid. */}
      <div className={styles.filterRow} role="group" aria-label={t('admin.mediaTab.filterType')}>
        <button type="button" className={chip(cls === '')} onClick={() => setCls('')}>
          {t('admin.mediaTab.allTypes')}
        </button>
        {CLASSES.map(({ id, labelKey }) => (
          <button key={id} type="button" className={chip(cls === id)} data-test={`media-class-${id}`} onClick={() => setCls(id)}>
            {t(labelKey)} <b>{classCount(id)}</b>
          </button>
        ))}
        <label className={styles.dateField}>
          <span>{t('admin.mediaTab.from')}</span>
          <input type="date" value={from} onChange={e => setFrom(e.target.value)} aria-label={t('admin.mediaTab.from')} />
        </label>
        <label className={styles.dateField}>
          <span>{t('admin.mediaTab.to')}</span>
          <input type="date" value={to} onChange={e => setTo(e.target.value)} aria-label={t('admin.mediaTab.to')} />
        </label>
        <label className={styles.dateField}>
          <span>{t('admin.mediaTab.kid')}</span>
          <select value={kid} onChange={e => setKid(Number(e.target.value))} aria-label={t('admin.mediaTab.kid')}>
            <option value={0}>{t('admin.mediaTab.allKids')}</option>
            {users.map(u => <option key={u.id} value={u.id}>{u.name}</option>)}
          </select>
        </label>
        {hasFilter && (
          <button type="button" className={styles.clearBtn} data-test="media-clear" onClick={() => { setSubject(''); setCls(''); setFrom(''); setTo(''); setKid(0); }}>
            {t('admin.mediaTab.clear')}
          </button>
        )}
      </div>

      {error && <p className={ui.msgError} role="alert">{error}</p>}
      {loading && <p className={ui.emptyInline}>{t('admin.mediaTab.loading')}</p>}

      {!loading && items.length === 0 && !error && (
        <div className={ui.empty}>
          <Icon name="camera" />
          {/* A filtered-empty is "nothing here under these filters", not
              "the library is empty" — a different message keeps the parent
              from thinking their uploads vanished. */}
          <p>{hasFilter ? t('admin.mediaTab.emptyFiltered') : t('admin.mediaTab.empty')}</p>
        </div>
      )}

      {!loading && items.length > 0 && (
        <div className={styles.grid} data-test="media-grid" role="list">
          {items.map(m => (
            <button
              key={m.attachment_id}
              type="button"
              className={styles.cell}
              data-test="media-item"
              onClick={() => setPreview(m)}
              role="listitem"
              aria-label={t('admin.mediaTab.itemAria', { name: m.display_name, chore: m.chore_title, when: formatWhen(m.created_at, i18n.language) })}
            >
              {m.mime_class === 'image' && <img src={m.file_path} alt={m.display_name} loading="lazy" />}
              {m.mime_class === 'video' && <span className={styles.docIcon}><Icon name="film" /></span>}
              {m.mime_class === 'audio' && <span className={styles.docIcon}><Icon name="mic" /></span>}
              {m.mime_class === 'pdf' && <span className={styles.docIcon}><Icon name="book" /></span>}
              {m.ai_complete != null && (
                <span
                  className={`${styles.aiBadge} ${m.ai_complete ? styles.aiBadgeGood : styles.aiBadgeWarn}`}
                  title={m.ai_feedback}
                >
                  {m.ai_complete ? '✅' : '⚠'} {Math.round((m.ai_confidence ?? 0) * 100)}%
                </span>
              )}
              <span className={styles.kindTag}>{t(`admin.mediaTab.cls_${m.mime_class}`)}</span>
              {m.subject && <span className={styles.subjectTag}>{subjectLabel(t, m.subject)}</span>}
              <span className={styles.name}>{m.display_name}</span>
              <span className={styles.meta}>
                {m.user_name} · {m.chore_title || t('admin.mediaTab.choreDeleted')} · {formatWhen(m.created_at, i18n.language)} · {formatSize(m.size_bytes)}
              </span>
            </button>
          ))}
        </div>
      )}

      {preview && <MediaPreview item={preview} onClose={() => setPreview(null)} />}
    </div>
  );
};
