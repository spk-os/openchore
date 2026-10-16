// The error-book entry card on Today (L1): the kid's wrong-question count
// and the door into the error book. OD prototype kid-today.html's
// errorbook-entry is the contract. Self-contained on purpose — it fetches
// its own list (like ReviewEntryCard), so useKidData stays untouched; when
// the book is empty the card hides entirely.
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { api } from '../../api';
import type { ErrorItem } from '../../types';
import s from './kid.module.css';

export function ErrorBookEntryCard() {
  const { t } = useTranslation();
  const [items, setItems] = useState<ErrorItem[] | null>(null);

  useEffect(() => {
    let alive = true;
    api.errorItems.list()
      .then(list => { if (alive) setItems(list); })
      .catch(() => { if (alive) setItems(null); });
    return () => { alive = false; };
  }, []);

  // Loading or empty book: no card at all — Today stays lean.
  if (!items || items.length === 0) return null;

  // "待订正" = not yet mastered; the mastered ones still count in the book.
  const todo = items.filter(it => it.mastery_level !== 2).length;

  return (
    <Link to="/errorbook" className={s.errorbookEntry} data-test="errorbook-entry">
      <span className={s.errorbookEntryIc} aria-hidden>📕</span>
      <span className={s.errorbookEntryBd}>
        <span className={s.errorbookEntryTitle}>
          {t('kid.errorbook.entryTitle')}
          <span data-test="errorbook-count">
            {t('kid.errorbook.entryCount', { count: items.length })}
          </span>
        </span>
        <span className={s.errorbookEntryMeta} data-test="errorbook-todo">
          {todo > 0
            ? t('kid.errorbook.entryTodo', { count: todo })
            : t('kid.errorbook.entryEmptyTodo', { count: items.length })}
        </span>
      </span>
      <span className={s.errorbookEntryBtn} data-test="errorbook-open-btn">
        {t('kid.errorbook.entryOpen')}
      </span>
    </Link>
  );
}
