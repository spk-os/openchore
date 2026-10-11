// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import i18n from '../../i18n';
import type { RewardCommitment, User } from '../../types';
import { FamilyGoalCard } from './FamilyGoalCard';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

const base = { avatar_url: '', theme: 'light' as const, has_pin: false, auth_providers: [] as string[], created_at: '2026-10-01' };
const me: User = { ...base, id: 3, name: '小梦', role: 'child', paused: false };

const people: User[] = [
  me,
  { ...base, id: 1, name: 'Dad', role: 'admin', paused: false },
  { ...base, id: 3, name: '小梦', role: 'child', paused: false, color: 'teal' },
];

const family = (p: Partial<RewardCommitment> = {}): RewardCommitment => ({
  id: 9,
  user_id: 3,
  reward_id: 4,
  reward_name: 'Trip to the zoo',
  target_cost: 5000,
  amount_saved: 120,
  auto_contribute_percent: 0,
  status: 'active',
  shared_pool_id: 7,
  pool: {
    id: 7,
    reward_id: 4,
    reward_name: 'Trip to the zoo',
    target_cost: 5000,
    amount_saved: 2120,
    status: 'active',
    contributors: [
      { user_id: 3, user_name: '小梦', amount_saved: 120 },
      { user_id: 1, user_name: 'Dad', amount_saved: 2000 },
    ],
    created_at: '2026-10-01',
  },
  created_at: '2026-10-01',
  ...p,
});

// react-router's Link needs a router around it.
const withRouter = (ui: React.ReactNode) => <MemoryRouter>{ui}</MemoryRouter>;

describe('FamilyGoalCard', () => {
  it('shows the pot: family kicker, both savers, and their amounts', () => {
    render(withRouter(
      <FamilyGoalCard c={family()} me={me} people={people} grownUpsDone={['Dad']} />,
    ));
    expect(screen.getByText('Family goal')).toBeTruthy();
    expect(screen.getByText('Trip to the zoo')).toBeTruthy();
    expect(screen.getByText('Dad')).toBeTruthy();
    expect(screen.getByText(/2,?000 pts/)).toBeTruthy();
    // The grown-up's check-in: "the whole family" is real.
    expect(screen.getByText('👨 Dad checked in today too')).toBeTruthy();
    // The pot's totals come from the pool, not the kid's own share.
    expect(screen.getByText(/2,?120 \/ 5,?000 pts/)).toBeTruthy();
  });

  it('labels the kid as "You" among the savers', () => {
    render(withRouter(<FamilyGoalCard c={family()} me={me} people={people} grownUpsDone={[]} />));
    expect(screen.getByText('You')).toBeTruthy();
    // No grown-up check-in line when the list is empty.
    expect(screen.queryByText(/checked in today/)).toBeNull();
  });

  it('shows the auto-save line with the percent when it is on', () => {
    render(withRouter(
      <FamilyGoalCard c={family({ auto_contribute_percent: 30 })} me={me} people={people} grownUpsDone={[]} />,
    ));
    expect(screen.getByText('✨ Auto-saving · 30% of every point')).toBeTruthy();
  });
});
