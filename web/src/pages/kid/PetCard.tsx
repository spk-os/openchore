import React, { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { api, APIError } from '../../api';
import type { Pet, PetMood, PetResponse } from '../../types';
import s from './kid.module.css';

// The kid's pet (P4-13): the "emotional balance" layer on Today. A named
// companion whose mood is computed server-side from the chore record
// (happy / calm / hungry), fed with points and grown through cumulative
// feeding. Never dies — the worst mood is hungry. The whole layer renders
// only when the pet_enabled setting is on (the API 404s otherwise).

/** The buddy's face: a stage-scaled emoji the mood animates. */
function petFace(pet: Pet): string {
  // One buddy, three stages: the face grows with the kid's care.
  if (pet.stage >= 3) return '🐕';
  if (pet.stage === 2) return '🐶';
  return '🐣';
}

const cap = (m: PetMood) => m[0].toUpperCase() + m.slice(1);

interface PetCardProps {
  initial: PetResponse;
  onToast: (message: string) => void;
  /** Points changed (feeding debits the ledger): refresh the balance. */
  onFed?: () => void;
}

export function PetCard({ initial, onToast, onFed }: PetCardProps) {
  const { t } = useTranslation();
  // Unhatched pets come back as {hatched: false} — detect by that marker
  // (a real Pet has no `hatched` field, only `hatched_at`).
  const [pet, setPet] = useState<Pet | null>('hatched' in initial ? null : initial);
  const [busy, setBusy] = useState(false);
  const [nameDraft, setNameDraft] = useState('');

  const hatch = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!nameDraft.trim() || busy) return;
    setBusy(true);
    try {
      setPet(await api.pet.hatch(nameDraft.trim()));
    } catch (err) {
      console.error(err);
    } finally {
      setBusy(false);
    }
  };

  const feed = async () => {
    if (!pet || busy) return;
    setBusy(true);
    try {
      const next = await api.pet.feed();
      setPet(next);
      onToast(t('kid.pet.feedToast', { name: next.name }));
      onFed?.();
    } catch (err) {
      // 422: not enough points — the friendly nudge, not an error toast.
      if (err instanceof APIError && err.status === 422) {
        onToast(t('kid.pet.notEnough'));
      } else {
        console.error(err);
      }
    } finally {
      setBusy(false);
    }
  };

  if (!pet) {
    return (
      <section className={s.petCard} aria-label={t('kid.pet.hatchTitle')}>
        <div className={s.petHead}>
          <span className={s.petFace} aria-hidden>🥚</span>
          <div className={s.petTitles}>
            <span className={s.petKicker}>{t('kid.pet.hatchTitle')}</span>
            <span className={s.petName}>{t('kid.pet.hatchText')}</span>
          </div>
        </div>
        <form className={s.petHatchForm} onSubmit={hatch}>
          <input
            className={s.petNameInput}
            value={nameDraft}
            onChange={e => setNameDraft(e.target.value)}
            placeholder={t('kid.pet.hatchPlaceholder')}
            maxLength={24}
            aria-label={t('kid.pet.hatchPlaceholder')}
          />
          <button type="submit" className={s.petFeedBtn} disabled={!nameDraft.trim() || busy}>
            {t('kid.pet.hatchBtn')}
          </button>
        </form>
      </section>
    );
  }

  return (
    <section className={s.petCard} aria-label={t('kid.pet.title')}>
      <div className={s.petHead}>
        <span className={s.petFace} data-mood={pet.mood} aria-hidden>{petFace(pet)}</span>
        <div className={s.petTitles}>
          <span className={s.petKicker}>{t('kid.pet.title')}</span>
          <span className={s.petName}>{pet.name}</span>
          <span className={s.petMood} data-mood={pet.mood}>
            {t(`kid.pet.mood${cap(pet.mood)}`)} · {t(`kid.pet.moodHint${cap(pet.mood)}`)}
          </span>
        </div>
      </div>
      <div className={s.petGrowth}>
        <span className={s.petStage}>{t('kid.pet.stage', { stage: pet.stage })}</span>
        <span className={s.petGrowthText}>
          {pet.next_stage_at > 0
            ? t('kid.pet.nextStage', { count: pet.next_stage_at - pet.fed_value })
            : t('kid.pet.grown')}
        </span>
      </div>
      <span className={s.bar} aria-hidden>
        <i style={{ width: `${Math.min(100, pet.stage_progress)}%` }} />
      </span>
      <button type="button" className={s.petFeedBtn} onClick={feed} disabled={busy}>
        {t('kid.pet.feedBtn', { cost: 10 })}
      </button>
    </section>
  );
}
