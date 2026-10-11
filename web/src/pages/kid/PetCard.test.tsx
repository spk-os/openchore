// @vitest-environment jsdom
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react';
import i18n from '../../i18n';
import type { Pet, PetUnhatched } from '../../types';
import { PetCard } from './PetCard';

beforeAll(async () => {
  await i18n.changeLanguage('en');
});
afterEach(cleanup);

const unhatched: PetUnhatched = { hatched: false };

const pet = (p: Partial<Pet> = {}): Pet => ({
  id: 1, user_id: 3, name: '豆豆', stage: 1, fed_value: 0,
  hatched_at: '2026-10-11T00:00:00Z', mood: 'happy',
  next_stage_at: 100, stage_progress: 0, ...p,
});

// The API client is mocked at the module level: the card must only talk to
// api.pet.hatch / api.pet.feed and never render anything else from it.
const { api, MockAPIError } = vi.hoisted(() => {
  class MockAPIError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.status = status;
    }
  }
  return { api: { pet: {} as Record<string, ReturnType<typeof vi.fn>> }, MockAPIError };
});
vi.mock('../../api', () => ({ api, APIError: MockAPIError }));

describe('PetCard', () => {
  it('adopts: typing a name and hatching shows the buddy', async () => {
    api.pet.hatch = vi.fn().mockResolvedValue(pet({ name: 'Rex' }));
    render(<PetCard initial={unhatched} onToast={vi.fn()} />);
    fireEvent.change(screen.getByPlaceholderText("Your buddy's name"), { target: { value: 'Rex' } });
    fireEvent.click(screen.getByRole('button', { name: 'Adopt' }));
    await waitFor(() => expect(screen.getByText('Rex')).toBeTruthy());
    expect(api.pet.hatch).toHaveBeenCalledWith('Rex');
  });

  it('shows the mood line and the feed button once hatched', () => {
    render(<PetCard initial={pet({ mood: 'calm', fed_value: 40, stage_progress: 40 })} onToast={vi.fn()} />);
    expect(screen.getByText(/Some chores are still open/)).toBeTruthy();
    expect(screen.getByRole('button', { name: /Feed -10 pts/ })).toBeTruthy();
    expect(screen.getByText('60 more to grow up')).toBeTruthy();
    expect(screen.getByText('Stage 1/3')).toBeTruthy();
  });

  it('feeds: debits through the API, toasts, and reports the new state', async () => {
    const onToast = vi.fn();
    api.pet.feed = vi.fn().mockResolvedValue(pet({ fed_value: 10, stage_progress: 10 }));
    render(<PetCard initial={pet()} onToast={onToast} />);
    fireEvent.click(screen.getByRole('button', { name: /Feed -10 pts/ }));
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('豆豆 is full and happy!'));
    expect(screen.getByText('90 more to grow up')).toBeTruthy();
  });

  it('turns a 422 into the friendly "not enough points" nudge', async () => {
    const onToast = vi.fn();
    api.pet.feed = vi.fn().mockRejectedValue(new MockAPIError('not enough points', 422));
    render(<PetCard initial={pet()} onToast={onToast} />);
    fireEvent.click(screen.getByRole('button', { name: /Feed -10 pts/ }));
    await waitFor(() => expect(onToast).toHaveBeenCalledWith('Not enough points — go earn some first.'));
  });

  it('shows the grown-up state once fully grown', () => {
    render(<PetCard initial={pet({ stage: 3, fed_value: 300, next_stage_at: 0, stage_progress: 100 })} onToast={vi.fn()} />);
    expect(screen.getByText('All grown up!')).toBeTruthy();
  });
});
