import React, { useState, useEffect } from 'react';
import { useTranslation } from 'react-i18next';
import Modal from '../Modal/Modal';
import { api } from '../../api';
import { localDateStr, toggleInArray } from '../../utils';
import type { Chore, CopilotTask, User } from '../../types';
import { useAIStatus } from '../../hooks/useAIStatus';
import { PersonToggle } from '../admin/pickers';
import styles from './QuickAssign.module.css';

interface Props {
  isOpen: boolean;
  onClose: () => void;
}

type Mode = 'quick' | 'copilot' | 'flyer';

/** A drafted task plus the edits the parent makes on the card. */
interface DraftCard extends CopilotTask {
  checked: boolean;
}

const QuickAssign: React.FC<Props> = ({ isOpen, onClose }) => {
  const { t } = useTranslation();
  const aiStatus = useAIStatus();
  const [chores, setChores] = useState<Chore[]>([]);
  const [users, setUsers] = useState<User[]>([]);

  // --- shared state ---
  const [mode, setMode] = useState<Mode>('quick');

  // --- quick assign state ---
  const [selectedChoreId, setSelectedChoreId] = useState<number | 'new' | ''>('');
  const [newTitle, setNewTitle] = useState('');
  const [newPoints, setNewPoints] = useState(5);
  const [selectedUserIds, setSelectedUserIds] = useState<number[]>([]);
  const [dateMode, setDateMode] = useState<'today' | 'tomorrow' | 'custom'>('today');
  const [customDate, setCustomDate] = useState('');

  // --- copilot / flyer state ---
  const [kidId, setKidId] = useState<number | null>(null);
  const [request, setRequest] = useState('');
  const [photo, setPhoto] = useState<File | null>(null);
  const [photoUrl, setPhotoUrl] = useState('');
  const [drafting, setDrafting] = useState(false);
  const [cards, setCards] = useState<DraftCard[]>([]);
  const [draftEvents, setDraftEvents] = useState<{ title: string; date: string; template_suggestion: string }[]>([]);
  const [draftMemos, setDraftMemos] = useState<{ title: string; date: string }[]>([]);
  const [draftNotes, setDraftNotes] = useState<string[]>([]);
  const [draftError, setDraftError] = useState('');
  const [applying, setApplying] = useState(false);

  const [assigning, setAssigning] = useState(false);
  const [error, setError] = useState('');

  const aiReady = aiStatus.ai.configured;

  useEffect(() => {
    if (!isOpen) return;
    Promise.all([api.chores.list(), api.users.list()]).then(([c, u]) => {
      setChores(c);
      setUsers(u);
    });
    setMode('quick');
    setSelectedChoreId('');
    setNewTitle('');
    setNewPoints(5);
    setSelectedUserIds([]);
    setDateMode('today');
    setCustomDate('');
    setRequest('');
    setPhoto(null);
    setPhotoUrl('');
    setCards([]);
    setDraftEvents([]);
    setDraftMemos([]);
    setDraftNotes([]);
    setDraftError('');
    setError('');
  }, [isOpen]);

  const kids = users.filter(u => u.role === 'child');

  // Default the copilot target to the only kid, like the templates page.
  useEffect(() => {
    if (kidId === null && kids.length === 1) setKidId(kids[0].id);
  }, [kids, kidId]);

  const getDateString = (): string => {
    const today = new Date();
    if (dateMode === 'today') return localDateStr(today);
    if (dateMode === 'tomorrow') {
      const t = new Date(today);
      t.setDate(t.getDate() + 1);
      return localDateStr(t);
    }
    return customDate;
  };

  const toggleUser = (id: number) => {
    setSelectedUserIds(prev => toggleInArray(prev, id));
  };

  const canAssign =
    (selectedChoreId === 'new' ? newTitle.trim().length > 0 : selectedChoreId !== '') &&
    selectedUserIds.length > 0 &&
    (dateMode !== 'custom' || customDate);

  const handleAssign = async () => {
    if (!canAssign) return;
    setAssigning(true);
    setError('');
    try {
      let choreId: number;
      if (selectedChoreId === 'new') {
        const created = await api.chores.create({
          title: newTitle.trim(),
          points_value: newPoints,
          category: 'bonus',
        });
        choreId = created.id;
      } else {
        choreId = selectedChoreId as number;
      }

      const dateString = getDateString();
      await Promise.all(
        selectedUserIds.map(userId =>
          api.chores.createSchedule(choreId, {
            assigned_to: userId,
            specific_date: dateString,
          })
        )
      );
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error && err.message ? err.message : t('dashboard.quickAssign.errorFailed'));
    } finally {
      setAssigning(false);
    }
  };

  // --- copilot draft ---

  const clearDraft = () => {
    setCards([]);
    setDraftEvents([]);
    setDraftMemos([]);
    setDraftNotes([]);
    setDraftError('');
  };

  const handleDraft = async (regenerate = false) => {
    if (kidId === null) return;
    if (mode === 'text' && !request.trim()) return;
    if (mode === 'flyer' && !photo) return;
    setDrafting(true);
    setDraftError('');
    try {
      let flyerUrl = photoUrl;
      if (mode === 'flyer') {
        // Upload (or re-upload when the parent swapped the photo) so the
        // server resolves a real /uploads/ path — it never fetches URLs.
        const uploaded = await api.chores.upload(photo!);
        flyerUrl = uploaded.url;
        setPhotoUrl(flyerUrl);
      }
      const draft = mode === 'flyer'
        ? await api.admin.copilotDraft({ assigned_to: kidId, mode: 'flyer', photo_url: flyerUrl })
        : await api.admin.copilotDraft({ assigned_to: kidId, mode: 'text', request: request.trim() });
      setCards(draft.tasks.map(task => ({ ...task, checked: true })));
      setDraftEvents(draft.events);
      setDraftMemos(draft.memos);
      setDraftNotes(draft.notes);
    } catch (err: unknown) {
      clearDraft();
      setDraftError(err instanceof Error && err.message ? err.message : t('dashboard.quickAssign.copilot.errorDraft'));
    } finally {
      setDrafting(false);
      void regenerate;
    }
  };

  const updateCard = (index: number, patch: Partial<DraftCard>) => {
    setCards(prev => prev.map((c, i) => (i === index ? { ...c, ...patch } : c)));
  };

  const selectedCards = cards.filter(c => c.checked);

  const handleApply = async () => {
    if (kidId === null || selectedCards.length === 0) return;
    setApplying(true);
    setDraftError('');
    try {
      await api.admin.copilotApply({
        assigned_to: kidId,
        source: mode === 'flyer' ? 'flyer' : 'copilot',
        tasks: selectedCards.map(c => ({
          title: c.title,
          description: c.description,
          category: c.category,
          icon: c.icon,
          points_value: c.points_value,
          missed_penalty: c.missed_penalty,
          estimated_minutes: c.estimated_minutes,
          days: c.days,
          date: c.date,
          due_by: c.due_by,
          available_at: c.available_at,
          expiry: 'block' as const,
        })),
      });
      onClose();
    } catch (err: unknown) {
      setDraftError(err instanceof Error && err.message ? err.message : t('dashboard.quickAssign.copilot.errorApply'));
    } finally {
      setApplying(false);
    }
  };

  const dates: { id: 'today' | 'tomorrow' | 'custom'; label: string }[] = [
    { id: 'today', label: t('dashboard.quickAssign.dateToday') },
    { id: 'tomorrow', label: t('dashboard.quickAssign.dateTomorrow') },
    { id: 'custom', label: t('dashboard.quickAssign.datePickDate') },
  ];

  const tabs: { id: Mode; label: string; icon: string }[] = [
    { id: 'quick', label: t('dashboard.quickAssign.tabQuick'), icon: '⚡' },
    { id: 'copilot', label: t('dashboard.quickAssign.tabCopilot'), icon: '✨' },
    { id: 'flyer', label: t('dashboard.quickAssign.tabFlyer'), icon: '📷' },
  ];

  const daysLabel = (days: string[]): string => {
    if (days.length === 0) return '';
    if (days.includes('daily')) return t('dashboard.quickAssign.copilot.daysDaily');
    if (days.includes('weekdays')) return t('dashboard.quickAssign.copilot.daysWeekdays');
    if (days.includes('weekends')) return t('dashboard.quickAssign.copilot.daysWeekends');
    return days.map(d => t(`dashboard.quickAssign.copilot.day_${d}`)).join(' · ');
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title={t('dashboard.quickAssign.title')} maxWidth="560px">
      <div className={styles.form}>
        {error && <p className={styles.error} role="alert">{error}</p>}

        <div className={styles.modeTabs} role="tablist">
          {tabs.map(tab => (
            <button
              key={tab.id}
              type="button"
              role="tab"
              aria-selected={mode === tab.id}
              className={styles.modeTab}
              onClick={() => { setMode(tab.id); clearDraft(); }}
            >
              <span aria-hidden>{tab.icon}</span> {tab.label}
            </button>
          ))}
        </div>

        {mode === 'quick' && (
          <>
            <div className={styles.section}>
              <label className={styles.label} htmlFor="quick-assign-chore">{t('dashboard.quickAssign.labelChore')}</label>
              <select
                id="quick-assign-chore"
                className={styles.input}
                value={selectedChoreId}
                onChange={e => {
                  const val = e.target.value;
                  setSelectedChoreId(val === 'new' ? 'new' : val === '' ? '' : Number(val));
                }}
              >
                <option value="">{t('dashboard.quickAssign.chorePlaceholder')}</option>
                {chores.map(c => (
                  <option key={c.id} value={c.id}>{c.title}</option>
                ))}
                <option value="new">{t('dashboard.quickAssign.choreNewOption')}</option>
              </select>

              {selectedChoreId === 'new' && (
                <div className={styles.newChoreFields}>
                  <input
                    className={styles.input}
                    type="text"
                    placeholder={t('dashboard.quickAssign.choreNamePlaceholder')}
                    aria-label={t('dashboard.quickAssign.choreNamePlaceholder')}
                    value={newTitle}
                    onChange={e => setNewTitle(e.target.value)}
                    autoFocus
                  />
                  <label className={styles.pointsRow}>
                    <span className={styles.labelSmall}>{t('dashboard.quickAssign.labelPoints')}</span>
                    <input
                      className={styles.input}
                      type="number"
                      min={0}
                      value={newPoints}
                      onChange={e => setNewPoints(Number(e.target.value))}
                    />
                  </label>
                </div>
              )}
            </div>

            <div className={styles.section}>
              <span className={styles.label}>{t('dashboard.quickAssign.labelWho')}</span>
              <div className={styles.chips}>
                {users.map(u => (
                  <PersonToggle key={u.id} user={u} pressed={selectedUserIds.includes(u.id)} onClick={() => toggleUser(u.id)} />
                ))}
              </div>
            </div>

            <div className={styles.section}>
              <span className={styles.label}>{t('dashboard.quickAssign.labelWhen')}</span>
              <div className={styles.chips}>
                {dates.map(d => (
                  <button
                    key={d.id}
                    type="button"
                    className={styles.chip}
                    aria-pressed={dateMode === d.id}
                    onClick={() => setDateMode(d.id)}
                  >
                    {d.label}
                  </button>
                ))}
              </div>
              {dateMode === 'custom' && (
                <input
                  className={styles.input}
                  type="date"
                  value={customDate}
                  aria-label={t('dashboard.quickAssign.datePickDate')}
                  onChange={e => setCustomDate(e.target.value)}
                />
              )}
            </div>

            <button
              type="button"
              className={styles.assignBtn}
              disabled={!canAssign || assigning}
              onClick={handleAssign}
            >
              {assigning ? t('dashboard.quickAssign.btnAssigning') : t('dashboard.quickAssign.btnAssign')}
            </button>
          </>
        )}

        {(mode === 'copilot' || mode === 'flyer') && (
          <>
            {/* target kid */}
            <div className={styles.section}>
              <span className={styles.label}>{t('dashboard.quickAssign.labelWho')}</span>
              {kids.length === 0 ? (
                <p className={styles.hint}>{t('dashboard.quickAssign.copilot.noKids')}</p>
              ) : (
                <div className={styles.chips}>
                  {kids.map(kid => (
                    <PersonToggle
                      key={kid.id}
                      user={kid}
                      pressed={kidId === kid.id}
                      onClick={() => setKidId(kid.id)}
                    />
                  ))}
                </div>
              )}
            </div>

            {mode === 'copilot' ? (
              <div className={styles.section}>
                <label className={styles.label} htmlFor="copilot-request">
                  {t('dashboard.quickAssign.copilot.requestLabel')}
                </label>
                <textarea
                  id="copilot-request"
                  className={styles.textarea}
                  rows={3}
                  value={request}
                  onChange={e => setRequest(e.target.value)}
                  placeholder={t('dashboard.quickAssign.copilot.requestPlaceholder')}
                  disabled={!aiReady || drafting}
                />
              </div>
            ) : (
              <div className={styles.section}>
                <span className={styles.label}>{t('dashboard.quickAssign.copilot.flyerLabel')}</span>
                <label className={styles.fileDrop}>
                  <input
                    type="file"
                    accept="image/*"
                    className={styles.fileInput}
                    onChange={e => {
                      const f = e.target.files?.[0] ?? null;
                      setPhoto(f);
                      setPhotoUrl('');
                      clearDraft();
                    }}
                    disabled={!aiReady || drafting}
                  />
                  {photo
                    ? <span>{photo.name}</span>
                    : <span className={styles.fileHint}>{t('dashboard.quickAssign.copilot.flyerPlaceholder')}</span>}
                </label>
                <p className={styles.hint}>{t('dashboard.quickAssign.copilot.flyerNote')}</p>
              </div>
            )}

            {!aiReady && (
              <p className={styles.hint}>{t('dashboard.quickAssign.copilot.needsAI')}</p>
            )}

            {(mode === 'copilot' || mode === 'flyer') && (
              <button
                type="button"
                className={styles.assignBtn}
                disabled={!aiReady || drafting || kidId === null || (mode === 'copilot' ? !request.trim() : !photo)}
                onClick={() => handleDraft()}
              >
                {drafting
                  ? t('dashboard.quickAssign.copilot.drafting')
                  : t(mode === 'flyer' ? 'dashboard.quickAssign.copilot.btnDraftFlyer' : 'dashboard.quickAssign.copilot.btnDraft')}
              </button>
            )}

            {draftError && <p className={styles.error} role="alert">{draftError}</p>}

            {/* events from the flyer: dated facts, with pack suggestions */}
            {draftEvents.length > 0 && (
              <div className={styles.section}>
                <span className={styles.label}>{t('dashboard.quickAssign.copilot.eventsTitle')}</span>
                {draftEvents.map((ev, i) => (
                  <div key={i} className={styles.factRow}>
                    <span className={styles.factTitle}>📅 {ev.title}</span>
                    {ev.date && <span className={styles.factDate}>{ev.date}</span>}
                    {ev.template_suggestion && (
                      <span className={styles.packHint}>
                        {t('dashboard.quickAssign.copilot.packHint', {
                          pack: t(`dashboard.quickAssign.copilot.pack_${ev.template_suggestion}`),
                        })}
                      </span>
                    )}
                  </div>
                ))}
              </div>
            )}

            {/* parent memos: read-only, never kid tasks */}
            {draftMemos.length > 0 && (
              <div className={styles.section}>
                <span className={styles.label}>{t('dashboard.quickAssign.copilot.memosTitle')}</span>
                {draftMemos.map((m, i) => (
                  <div key={i} className={styles.factRow}>
                    <span className={styles.factTitle}>📌 {m.title}</span>
                    {m.date && <span className={styles.factDate}>{m.date}</span>}
                  </div>
                ))}
                <p className={styles.hint}>{t('dashboard.quickAssign.copilot.memoNote')}</p>
              </div>
            )}

            {/* task cards */}
            {cards.length > 0 && (
              <div className={styles.section}>
                <span className={styles.label}>
                  {t('dashboard.quickAssign.copilot.cardsTitle', { count: selectedCards.length })}
                </span>
                {cards.map((card, i) => (
                  <div key={i} className={styles.card}>
                    <label className={styles.cardHead}>
                      <input
                        type="checkbox"
                        checked={card.checked}
                        onChange={() => updateCard(i, { checked: !card.checked })}
                      />
                      <span className={styles.cardIcon} aria-hidden>{card.icon || '⭐'}</span>
                      <input
                        className={styles.cardTitleInput}
                        type="text"
                        value={card.title}
                        aria-label={t('dashboard.quickAssign.labelChore')}
                        onChange={e => updateCard(i, { title: e.target.value })}
                      />
                      <label className={styles.cardPoints}>
                        <input
                          type="number"
                          min={1}
                          max={20}
                          value={card.points_value}
                          aria-label={t('dashboard.quickAssign.labelPoints')}
                          onChange={e => updateCard(i, { points_value: Number(e.target.value) })}
                        />
                        <span aria-hidden>分</span>
                      </label>
                    </label>
                    <div className={styles.cardMeta}>
                      <select
                        className={styles.cardSelect}
                        value={card.category}
                        aria-label={t('dashboard.quickAssign.copilot.categoryLabel')}
                        onChange={e => updateCard(i, { category: e.target.value as CopilotTask['category'] })}
                      >
                        <option value="required">{t('dashboard.quickAssign.copilot.catRequired')}</option>
                        <option value="core">{t('dashboard.quickAssign.copilot.catCore')}</option>
                        <option value="bonus">{t('dashboard.quickAssign.copilot.catBonus')}</option>
                      </select>
                      {card.date ? (
                        <span className={styles.cardDays}>{card.date}</span>
                      ) : (
                        <span className={styles.cardDays}>{daysLabel(card.days)}</span>
                      )}
                      {!!card.estimated_minutes && (
                        <span className={styles.cardDays}>⏱ {card.estimated_minutes}min</span>
                      )}
                      {card.confidence > 0 && card.confidence < 1 && (
                        <span className={styles.cardDays}>{Math.round(card.confidence * 100)}%</span>
                      )}
                    </div>
                    {card.rationale && <p className={styles.cardWhy}>{card.rationale}</p>}
                  </div>
                ))}
              </div>
            )}

            {/* notes (dedup skips, model caveats) */}
            {draftNotes.length > 0 && (
              <div className={styles.notes}>
                {draftNotes.map((note, i) => (
                  <p key={i}>💡 {note}</p>
                ))}
              </div>
            )}

            {/* regenerate + apply */}
            {cards.length > 0 && (
              <div className={styles.applyRow}>
                <button
                  type="button"
                  className={styles.regenBtn}
                  disabled={drafting || applying}
                  onClick={() => handleDraft(true)}
                >
                  {t('dashboard.quickAssign.copilot.btnRegenerate')}
                </button>
                <button
                  type="button"
                  className={styles.assignBtn}
                  disabled={applying || selectedCards.length === 0}
                  onClick={handleApply}
                >
                  {applying
                    ? t('dashboard.quickAssign.copilot.applying')
                    : t('dashboard.quickAssign.copilot.btnApply', { count: selectedCards.length })}
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </Modal>
  );
};

export default QuickAssign;
