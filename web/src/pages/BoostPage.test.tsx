// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import i18n from '../i18n';
import type { BoostSnapshot } from '../types';
import { BoostPage } from './BoostPage';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

const snap = (p: Partial<BoostSnapshot> = {}): BoostSnapshot => ({
  kid_name: '小梦',
  label: 'Grandma',
  current_streak: 4,
  week_completed: 7,
  coach_line: 'Great week so far!',
  monthly_cap: 60,
  granted_this_month: 0,
  daily_limit: 3,
  amounts: [5, 10, 20],
  ...p,
});

// The API client is mocked at the module level: the page must only talk to
// api.boost.page / api.boost.send and never render anything else from it.
const { api, MockAPIError } = vi.hoisted(() => {
  class MockAPIError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  }
  return { api: { boost: {} as Record<string, ReturnType<typeof vi.fn>> }, MockAPIError };
});
vi.mock('../api', () => ({ api, APIError: MockAPIError }));

// The token comes from the URL, exactly as App routes it.
const withRouter = (ui: React.ReactNode) => (
  <MemoryRouter initialEntries={['/boost/tok123']}>
    <Routes>
      <Route path="/boost/:token" element={ui} />
    </Routes>
  </MemoryRouter>
);

describe('BoostPage', () => {
  it('renders the snapshot: name, stats and the coach line', async () => {
    api.boost.page = vi.fn().mockResolvedValue(snap());
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('For 小梦')).toBeTruthy());
    expect(screen.getByText(/🔥 4/)).toBeTruthy();
    expect(screen.getByText(/✅ 7/)).toBeTruthy();
    expect(screen.getByText(/"Great week so far!"/)).toBeTruthy();
    // Three tiers, each labelled with its amount.
    expect(screen.getByRole('button', { name: '+5' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '+10' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '+20' })).toBeTruthy();
    expect(screen.getByText('Boost allowance left this month: 60 / 60 pts')).toBeTruthy();
  });

  it('boosts through the API and shows the sent toast with the fresh tally', async () => {
    api.boost.page = vi.fn().mockResolvedValue(snap());
    api.boost.send = vi.fn().mockResolvedValue(snap({ granted_this_month: 10 }));
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('For 小梦')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '+10' }));
    await waitFor(() => expect(screen.getByTestId('boost-sent')).toBeTruthy());
    expect(screen.getByText('🎉 10 points sent!')).toBeTruthy();
    // The quota line reflects the reloaded snapshot.
    expect(screen.getByText('Boost allowance left this month: 50 / 60 pts')).toBeTruthy();
    expect(api.boost.send).toHaveBeenCalledWith('tok123', 10);
  });

  it('turns a monthly-cap 422 into the "come back next month" line', async () => {
    // granted 55 of 60: +5 still fits client-side, so the cap trips on the
    // server's own count — exactly the race the client can't see.
    api.boost.page = vi.fn().mockResolvedValue(snap({ granted_this_month: 55 }));
    api.boost.send = vi.fn().mockRejectedValue(new MockAPIError('monthly boost cap reached', 422));
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('For 小梦')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '+5' }));
    await waitFor(() =>
      expect(screen.getByText("This month's allowance is used up — come back next month")).toBeTruthy());
  });

  it('turns a daily-limit 422 into the "see you tomorrow" line', async () => {
    api.boost.page = vi.fn().mockResolvedValue(snap());
    api.boost.send = vi.fn().mockRejectedValue(new MockAPIError('daily boost limit reached', 422));
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('For 小梦')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: '+5' }));
    await waitFor(() =>
      expect(screen.getByText("That's all the cheers for today — see you tomorrow")).toBeTruthy());
  });

  it('renders the "no longer available" card for a 404 (revoked, expired, unknown)', async () => {
    api.boost.page = vi.fn().mockRejectedValue(new MockAPIError('boost link not found', 404));
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('Link no longer available')).toBeTruthy());
    expect(screen.queryByText('Send a cheer')).toBeNull();
  });

  it('disables a tier the remaining allowance cannot cover', async () => {
    api.boost.page = vi.fn().mockResolvedValue(snap({ granted_this_month: 50 }));
    render(withRouter(<BoostPage />));
    await waitFor(() => expect(screen.getByText('For 小梦')).toBeTruthy());
    // +5 and +10 still fit (10 left); +20 does not.
    expect((screen.getByRole('button', { name: '+5' }) as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByRole('button', { name: '+20' }) as HTMLButtonElement).disabled).toBe(true);
  });
});
