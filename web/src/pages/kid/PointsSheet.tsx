import clsx from 'clsx';
import { useTranslation } from 'react-i18next';
import { Icon, type IconName } from '../../design';
import type { PointTransaction } from '../../types';
import type { KidData } from './useKidData';
import { Sheet } from './Sheet';
import s from './kid.module.css';

// Same icon vocabulary as the admin activity feed, so a kid and their
// parent see the same shape for the same event.
const REASON_ICON: Partial<Record<PointTransaction['reason'], IconName>> = {
  chore_complete: 'check',
  chore_uncomplete: 'back',
  reward_redeem: 'gift',
  streak_bonus: 'flame',
  admin_adjust: 'star',
  expiry_penalty: 'clock',
  missed_chore: 'clock',
  points_decay: 'moon',
  commit_to_goal: 'rocket',
  goal_break: 'rocket',
  pet_feed: 'paw',
  bonus_drop: 'spark',
  grandparent_boost: 'star',
};

/**
 * 任务④「积分明细都没有，用户如何看？」：孩子自己的积分账本。每次变动
 * （完成任务、兑换奖励、存入目标、彩蛋……）都在这里解释了为什么变、变
 * 了多少。入口是顶栏的积分胶囊（parts.tsx TopBar），数据来自 useKidData
 * 已经拉好的 points.transactions。
 */
export function PointsSheet({ data, onClose }: { data: KidData; onClose: () => void }) {
  const { t, i18n } = useTranslation();
  const balance = data.points?.balance ?? 0;
  const committed = data.points?.committed ?? 0;
  const txns = data.points?.transactions ?? [];

  const label = (reason: string) => {
    const key = `admin.activityTab.reason.${reason}`;
    return i18n.exists(key) ? t(key) : reason;
  };

  return (
    <Sheet title={t('kid.points.title')} onClose={onClose} className={s.pointsSheet}>
      <div className={s.txBalance}>
        <span className={s.txBalanceNum}>
          <Icon name="star" fill className={s.txBalanceStar} />
          {balance}
        </span>
        <span className={s.txBalanceSub}>
          {committed > 0 ? t('kid.points.savedSub', { count: committed }) : t('kid.points.balanceSub')}
        </span>
      </div>
      <p className={s.txHelp}>{t('kid.points.help')}</p>

      {txns.length === 0 ? (
        <p className={s.txEmpty}>{t('kid.points.empty')}</p>
      ) : (
        <ul className={s.txList}>
          {txns.map(tx => (
            <li key={tx.id} className={s.txItem}>
              <span className={s.txIcon} aria-hidden>
                <Icon name={REASON_ICON[tx.reason] ?? 'star'} />
              </span>
              <span className={s.txMain}>
                <span className={s.txReason}>{label(tx.reason)}</span>
                {tx.note && <span className={s.txNote}>{tx.note}</span>}
                <time className={s.txDate} dateTime={tx.created_at}>
                  {new Date(tx.created_at).toLocaleDateString(i18n.language, { month: 'short', day: 'numeric' })}
                  {' '}
                  {new Date(tx.created_at).toLocaleTimeString(i18n.language, { hour: 'numeric', minute: '2-digit' })}
                </time>
              </span>
              <span className={clsx(s.txAmount, tx.amount < 0 && s.txAmountNeg)}>
                {tx.amount > 0 ? '+' : tx.amount < 0 ? '−' : ''}{Math.abs(tx.amount)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </Sheet>
  );
}
