import clsx from 'clsx';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Avatar, Icon, isPersonColor } from '../../design';
import type { RewardCommitment, User } from '../../types';
import { goalProgress } from './goals';
import s from './kid.module.css';

/**
 * The family's shared goal on Today's hero (P4-14): the pot's progress up
 * front, where the day happens — each saver's part in their colour, who has
 * joined, and the grown-ups who ticked their own chore off today (a parent's
 * 0-point chore is the demonstration that "the whole family" is real).
 */
export function FamilyGoalCard({
  c, me, people, grownUpsDone,
}: {
  c: RewardCommitment;
  me: User;
  people: User[];
  grownUpsDone: string[];
}) {
  const { t } = useTranslation();
  const p = goalProgress(c);
  const contributors = c.pool?.contributors ?? [];
  const colorOf = (userId: number) => {
    const col = people.find(u => u.id === userId)?.color;
    return isPersonColor(col) ? col : undefined;
  };
  const autoSave = c.auto_contribute_percent > 0;

  return (
    <Link to="/rewards" className={clsx(s.goal, s.goalNudge, s.familyGoal)} aria-label={t('kid.goal.title')}>
      <span className={s.goalHead}>
        <span className={s.well} aria-hidden><Icon name="people" /></span>
        <span className={s.goalTitles}>
          <span className={s.goalKicker}>{t('kid.goal.familyGoal')}</span>
          <span className={s.goalName}>{c.pool?.reward_name ?? c.reward_name}</span>
        </span>
        <Icon name="chev" />
      </span>

      <span className={s.goalMid}>
        <span className={s.bar} role="img" aria-label={t('kid.goal.progressLabel', { saved: p.saved, target: p.target })} aria-hidden>
          {contributors.filter(x => x.amount_saved > 0).map(x => (
            <i
              key={x.user_id}
              data-person={colorOf(x.user_id)}
              style={{ width: `${Math.min(100, (x.amount_saved / p.target) * 100)}%` }}
            />
          ))}
          {contributors.every(x => x.amount_saved === 0) && (
            <i data-person={isPersonColor(me.color) ? me.color : undefined} style={{ width: '0%' }} />
          )}
        </span>
        <span className={s.goalAmounts}>{t('kid.goal.amounts', { saved: p.saved, target: p.target })}</span>
      </span>

      {contributors.length > 0 && (
        <span className={s.who} aria-label={t('kid.goal.savers')}>
          {contributors.map(x => (
            <span key={x.user_id} className={s.whoOne}>
              <Avatar name={x.user_name} color={colorOf(x.user_id)} size="sm" />
              <span>{x.user_id === me.id ? t('kid.goal.you') : x.user_name}</span>
              <span className={s.whoAmt}>{t('kid.goal.saverAmount', { count: x.amount_saved })}</span>
            </span>
          ))}
        </span>
      )}

      <span className={s.goalFoot}>
        <span className={s.autoSaveHintLine}>
          {autoSave
            ? t('kid.goal.autoSaving', { percent: c.auto_contribute_percent })
            : t('kid.goal.autoSaveHint')}
        </span>
        {grownUpsDone.length > 0 && (
          <span className={s.grownUpsDone}>{t('kid.goal.grownUpsDone', { names: grownUpsDone.join('、') })}</span>
        )}
      </span>
    </Link>
  );
}
