import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import { Icon } from '../../design';
import Modal from '../Modal/Modal';
import type { MediaClassStat, MediaItem, MediaSubjectStat, MimeClass, User } from '../../types';
// 042：学科清单不再前端硬编码——由 useSubjects（GET /api/taxonomy/subjects，
// subjects 表驱动、服务端 sort_order 顺序原样渲染）下发；本文件只保留
// subjectOptionLabel 的单值 → admin.analysis.subject_* 词条映射。
import { subjectOptionLabel } from '../../subjects';
import { useSubjects } from '../../hooks/useSubjects';
import ui from './ui.module.css';
import styles from './MediaTab.module.css';

/**
 * 036: the parent-side media library. Photos, videos and recordings pile
 * up fast once direct recording exists; this tab is the volume answer —
 * every uploaded proof file in one grid, grouped and filtered by subject
 * (the chore's school subject), media class, date and the kid who handed
 * it in. The subject vocabulary is the error book's (they share the
 * admin.analysis.subject_* labels), with the list itself served by the
 * subjects table.
 */

const subjectLabel = (t: (k: string) => string, subject: string, serverName?: string) =>
  subjectOptionLabel(t, subject, subject, serverName);

/**
 * R2 内部 API 哨兵（冻结编码）：「未分类」筛选不再与「全部」共用 subject=''。
 * '' 仍是全部；'__unclassified__' 仅选 COALESCE(chore.subject,'')='' 的行。
 * 哨兵只存在于本组件的 UI 状态并原样传给 api.chores.listMedia（api.ts 不改，
 * 服务端 media.go/store 实现该过滤），绝不留在任何持久化数据里。
 */
const UNCLASSIFIED = '__unclassified__';

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
  // 学科清单：服务端 subjects 表（sort_order 顺序）原样渲染；只要
  // subjectsError != null 就显示「数据可能过期/加载失败」+ retry（与
  // subjects.length 解耦——stale 快照在场时错误同样可见，不因旧数据隐藏）。
  const { subjects: subjectRows, error: subjectsError, retry: retrySubjects } = useSubjects();
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
        // ''（全部）与 '__unclassified__'（未分类）是两个独立值，原样上送：
        // 过滤由服务端完成，绝不在客户端 items.filter 冒充。
        subject, cls, from, to,
        user_id: kid || undefined,
      });
      // A filter with no matches used to come back with null stats (Go
      // nil slices encode as null); the chips then crashed on .find and
      // took the whole dashboard down ("点录音白屏"). Treat null as empty.
      setItems(res.items ?? []);
      setSubjects(res.subjects ?? []);
      setClasses(res.classes ?? []);
      setTotal(res.total ?? 0);
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
    <div className={ui.page} data-test="admin-media-page">
      <div className={ui.pageHead}>
        <div>
          <h2 className={ui.pageTitle}>{t('admin.mediaTab.title')}</h2>
          <p className={ui.pageSub}>{t('admin.mediaTab.subtitle', { count: total })}</p>
        </div>
      </div>

      {/* Subject chips — the subjects the server's subjects table lists
          plus the unclassified bucket, each with its live count under the
          other filters. 服务端顺序原样渲染（subjects.sort_order）；不迭代
          任何前端静态词表。R2：全部/未分类是两个独立 UI 哨兵（''=全部，
          '__unclassified__'=仅 COALESCE(subject,'')=''，服务端过滤），
          不再同值；subjectError 只要存在就可见 + retry（与快照长度解耦，
          stale 快照在场时错误不隐藏）。词条复用 errorbookTab.loadFailed /
          kidsStatusTab.refreshLabel，不加新 i18n。 */}
      {subjectsError != null && (
        <p className={ui.msgError} role="alert" data-test="media-subjects-error">
          {t('admin.errorbookTab.loadFailed')}{' '}
          <button type="button" className={ui.btnGhost} onClick={retrySubjects} data-test="media-subjects-retry">
            {t('admin.kidsStatusTab.refreshLabel')}
          </button>
        </p>
      )}
      <div className={styles.filterRow} data-test="media-subject-filter" role="group" aria-label={t('admin.mediaTab.filterSubject')}>
        {/* 「全部」与「未分类」不带 media-subject- 前缀 data-test：测试的
            subjectChipSelector 只把服务端学科 chip 计入顺序断言。 */}
        <button type="button" className={chip(subject === '')} onClick={() => setSubject('')}>
          {t('admin.mediaTab.allSubjects')} <b>{total}</b>
        </button>
        {subjectRows.map(row => (
          <button key={row.id} type="button" className={chip(subject === row.id)} data-test={`media-subject-${row.id}`} onClick={() => setSubject(row.id)}>
            {subjectLabel(t, row.id, row.name)} <b>{subjectCount(row.id)}</b>
          </button>
        ))}
        <button type="button" className={chip(subject === UNCLASSIFIED)} onClick={() => setSubject(UNCLASSIFIED)}>
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
        )}      </div>

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
