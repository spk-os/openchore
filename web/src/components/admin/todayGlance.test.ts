import { describe, it, expect } from 'vitest';
import { glanceFor, DUE_SOON_MINUTES } from './todayGlance';
import type { ScheduledChore, User } from '../../types';

const kid: User = {
  id: 2, name: '小明', avatar_url: '', role: 'child', theme: 'house', paused: false,
  has_pin: false, auth_providers: [], created_at: '',
} as unknown as User;

const base: ScheduledChore = {
  schedule_id: 1, chore_id: 1, title: '英语朗读', description: '', category: 'required',
  points_value: 5, missed_penalty_value: 0, requires_approval: false, requires_photo: false,
  assignment_type: 'individual', expiry_penalty: 'block', expiry_penalty_value: 0,
  available: true, expired: false, completed: false, date: '2026-10-11',
} as ScheduledChore;

const chore = (over: Partial<ScheduledChore>): ScheduledChore => ({ ...base, ...over });

// 19:00 due_by, judged at 18:30 → due in 30 min (inside the window).
const NOW = new Date('2026-10-11T18:30:00');

describe('glanceFor', () => {
  it('is green when everything on today is done', () => {
    const g = glanceFor([{ user: kid, chores: [chore({ completed: true })] }], NOW);
    expect(g.level).toBe('green');
    expect(g.done).toBe(1);
    expect(g.total).toBe(1);
  });

  it('stays green with unfinished work that is not yet due', () => {
    const g = glanceFor([{ user: kid, chores: [chore({ due_by: '21:00' })] }], NOW);
    expect(g.level).toBe('green');
    expect(g.done).toBe(0);
  });

  it('is yellow with the nearest chore when one is due within the hour', () => {
    const g = glanceFor([{
      user: kid,
      chores: [
        chore({ schedule_id: 1, title: '英语朗读', due_by: '19:00' }),
        chore({ schedule_id: 2, title: '语音复盘', category: 'core', due_by: '19:10' }),
      ],
    }], NOW);
    expect(g.level).toBe('yellow');
    expect(g.dueSoon?.chore.title).toBe('英语朗读');
    expect(g.dueSoonCount).toBe(2);
  });

  it('is red when a required chore missed its window, overdue listed for excusing', () => {
    const g = glanceFor([{
      user: kid,
      chores: [chore({ expired: true, due_by: '17:00' })],
    }], NOW);
    expect(g.level).toBe('red');
    expect(g.overdue).toHaveLength(1);
    expect(g.overdue[0].chore.title).toBe('英语朗读');
    expect(g.overdue[0].user.id).toBe(kid.id);
  });

  it('red wins over yellow', () => {
    const g = glanceFor([{
      user: kid,
      chores: [
        chore({ schedule_id: 1, expired: true, due_by: '17:00' }),
        chore({ schedule_id: 2, category: 'core', due_by: '19:00' }),
      ],
    }], NOW);
    expect(g.level).toBe('red');
  });

  it('ignores bonus chores and paused kids entirely', () => {
    const g = glanceFor([
      { user: { ...kid, paused: true }, chores: [chore({ expired: true })] },
      { user: kid, chores: [chore({ category: 'bonus', expired: true, due_by: '19:00' })] },
    ], NOW);
    expect(g.level).toBe('green');
    // Only the unpaused kid's bonus chore was tallied; bonus counts toward
    // the total but never warns.
    expect(g.total).toBe(1);
    expect(g.done).toBe(0);
    expect(g.overdue).toHaveLength(0);
  });

  it('counts an excused chore as done — excusing closes the loop', () => {
    const g = glanceFor([{
      user: kid,
      chores: [chore({ completed: true, completion_status: 'excused' })],
    }], NOW);
    expect(g.level).toBe('green');
    expect(g.done).toBe(1);
  });

  it('exposes the due-soon window for the copy', () => {
    expect(DUE_SOON_MINUTES).toBe(60);
  });
});
