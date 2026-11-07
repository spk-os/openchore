// The learn entry card on Today: the door into the ten-subject knowledge
// workspace (/learn — 学科知识/挑战/再挑战/历史). Always visible (unlike the
// review/error-book entries, which hide when empty): the browse view is
// usable without any prior data.
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import s from './kid.module.css';

export function LearnEntryCard() {
  const { t } = useTranslation();
  return (
    <Link to="/learn" className={s.reviewEntry} data-test="learn-entry">
      <span className={s.reviewEntryIc} aria-hidden>🧭</span>
      <span className={s.reviewEntryBd}>
        <span className={s.reviewEntryTitle}>
          {t('kid.learn.entryTitle')}
        </span>
        <span className={s.reviewEntryMeta} data-test="learn-entry-meta">
          {t('kid.learn.entryMeta')}
        </span>
      </span>
      <span className={s.reviewEntryBtn} data-test="learn-entry-btn">
        {t('kid.learn.entryOpen')}
      </span>
    </Link>
  );
}
