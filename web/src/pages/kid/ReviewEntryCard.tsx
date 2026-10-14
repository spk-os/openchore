// The review entry card on Today (L4): the kid's due-card count and the
// door into the review screen. OD prototype kid-today.html's
// review-entry is the contract. Self-contained on purpose — it fetches
// its own queue (like PetCard), so useKidData stays untouched; when
// nothing is due the card hides entirely.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ReviewToday } from '../../types';
import s from './kid.module.css';

export function ReviewEntryCard() {
  const { t } = useTranslation();
  const [today, setToday] = useState<ReviewToday | null>(null);

  useEffect(() => {
    let alive = true;
    api.review.today()
      .then(out => { if (alive) setToday(out); })
      .catch(() => { if (alive) setToday(null); });
    return () => { alive = false; };
  }, []);

  // Loading or nothing due: no card at all — Today stays lean.
  if (!today || today.due.length === 0) return null;

  return (
    <Link to="/review" className={s.reviewEntry} data-test="review-entry">
      <span className={s.reviewEntryIc} aria-hidden>🔁</span>
      <span className={s.reviewEntryBd}>
        <span className={s.reviewEntryTitle}>
          {t('kid.review.entryTitle')}
          <span data-test="review-due-count">
            {t('kid.review.entryCount', { count: today.due.length })}
          </span>
        </span>
        <span className={s.reviewEntryMeta} data-test="review-meta">
          {t('kid.review.entryMeta')}
        </span>
      </span>
      <span className={s.reviewEntryBtn} data-test="review-start-btn">
        {t('kid.review.entryStart')}
      </span>
    </Link>
  );
}
