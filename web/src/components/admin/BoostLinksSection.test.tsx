// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react';
import i18n from '../../i18n';
import type { BoostLink } from '../../types';
import { BoostLinksSection } from './BoostLinksSection';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

const link = (p: Partial<BoostLink> = {}): BoostLink => ({
  id: 7,
  user_id: 3,
  label: 'Grandma',
  monthly_cap: 60,
  daily_limit: 3,
  granted: 0,
  granted_month: new Date().toISOString().slice(0, 7),
  revoked: false,
  expires_at: '2027-01-10T00:00:00Z',
  created_at: '2026-10-12T00:00:00Z',
  ...p,
});

// The API client is mocked at the module level: the section must only talk
// to api.admin.listBoostLinks / createBoostLink / revokeBoostLink and the
// base_url setting.
const { api } = vi.hoisted(() => {
  return {
    api: {
      admin: {
        listBoostLinks: vi.fn(),
        createBoostLink: vi.fn(),
        revokeBoostLink: vi.fn(),
        getSetting: vi.fn().mockResolvedValue({ key: 'base_url', value: 'https://chores.example.com' }),
      },
    },
  };
});
vi.mock('../../api', () => ({ api }));

describe('BoostLinksSection', () => {
  it('lists the existing links with their month tally and expiry', async () => {
    api.admin.listBoostLinks.mockResolvedValue([link({ granted: 20 })]);
    render(<BoostLinksSection userId={3} />);
    await waitFor(() => expect(screen.getByText('Grandma')).toBeTruthy());
    expect(screen.getByText(/20\/60 pts boosted this month/)).toBeTruthy();
    expect(screen.getByText(/expires/)).toBeTruthy();
    // A live link carries the revoke button.
    expect(screen.getByRole('button', { name: 'Revoke this link' })).toBeTruthy();
  });

  it('creates a link and shows the one-time share card with its QR', async () => {
    api.admin.listBoostLinks.mockResolvedValue([]);
    api.admin.createBoostLink.mockResolvedValue(link({ token: 'tok48chars' }));
    render(<BoostLinksSection userId={3} />);
    await waitFor(() => expect(screen.getByText(/No links yet/)).toBeTruthy());
    fireEvent.change(screen.getByPlaceholderText('Grandma'), { target: { value: 'Grandma' } });
    fireEvent.click(screen.getByRole('button', { name: /Create link/ }));
    await waitFor(() => expect(screen.getByTestId('boost-share-card')).toBeTruthy());
    expect(screen.getByText('Link created: Grandma')).toBeTruthy();
    expect(api.admin.createBoostLink).toHaveBeenCalledWith(3, 'Grandma');
  });

  it('marks a revoked link without a revoke button', async () => {
    api.admin.listBoostLinks.mockResolvedValue([link({ revoked: true })]);
    render(<BoostLinksSection userId={3} />);
    await waitFor(() => expect(screen.getByText('Revoked')).toBeTruthy());
    expect(screen.queryByRole('button', { name: 'Revoke this link' })).toBeNull();
  });

  it('reads a past month\'s granted as zero', async () => {
    api.admin.listBoostLinks.mockResolvedValue([
      link({ granted: 40, granted_month: '2026-09' }),
    ]);
    render(<BoostLinksSection userId={3} />);
    await waitFor(() => expect(screen.getByText('Grandma')).toBeTruthy());
    // September's tally doesn't count against this month's allowance.
    expect(screen.getByText(/0\/60 pts boosted this month/)).toBeTruthy();
  });

  it('revokes through the API after the confirm', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    api.admin.listBoostLinks.mockResolvedValue([link()]);
    api.admin.revokeBoostLink.mockResolvedValue({ revoked: true });
    render(<BoostLinksSection userId={3} />);
    await waitFor(() => expect(screen.getByText('Grandma')).toBeTruthy());
    fireEvent.click(screen.getByRole('button', { name: 'Revoke this link' }));
    await waitFor(() => expect(api.admin.revokeBoostLink).toHaveBeenCalledWith(3, 7));
  });
});
