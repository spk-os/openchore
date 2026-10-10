// The three-colour "today at a glance" bar over the KidsStatusTab: pure
// aggregation of the per-person chore lists the tab already loaded
// (P2-8 risk alerts, plan A — computed on open, no backend). Red wins over
// yellow, yellow over green: a missed must-do outranks a looming one.
import type { ScheduledChore, User } from '../../types';

/** Minutes before a chore's due time at which it counts as "due soon". */
export const DUE_SOON_MINUTES = 60;

export type GlanceLevel = 'green' | 'yellow' | 'red';

export interface GlanceChore {
  user: User;
  chore: ScheduledChore;
}

export interface TodayGlance {
  level: GlanceLevel;
  /** All of today's chores across everyone (bonus included). */
  total: number;
  done: number;
  /** The nearest not-yet-overdue chore due within the hour (yellow state). */
  dueSoon?: GlanceChore;
  /** How many chores are due soon (the bar names only the nearest). */
  dueSoonCount: number;
  /** Required/core chores that missed their window (red state). */
  overdue: GlanceChore[];
}

export function glanceFor(kids: { user: User; chores: ScheduledChore[] }[], now: Date): TodayGlance {
  let total = 0;
  let done = 0;
  const dueSoon: { gc: GlanceChore; dueAt: number }[] = [];
  const overdue: GlanceChore[] = [];
  for (const kid of kids) {
    // A paused day is a deliberate pause, not a warning.
    if (kid.user.paused) continue;
    for (const chore of kid.chores) {
      total += 1;
      // Excused chores count as completed: the loop is closed on purpose.
      if (chore.completed) {
        done += 1;
        continue;
      }
      // Bonus never gates anything and never warns — it is opt-in.
      if (chore.category === 'bonus') continue;
      if (chore.expired) {
        overdue.push({ user: kid.user, chore });
        continue;
      }
      // Not open yet, or no deadline: nothing the parent needs to act on.
      if (!chore.available || !chore.due_by) continue;
      const [h, m] = chore.due_by.split(':').map(Number);
      const dueAt = new Date(now);
      dueAt.setHours(h || 0, m || 0, 0, 0);
      const leftMin = (dueAt.getTime() - now.getTime()) / 60_000;
      if (leftMin > 0 && leftMin <= DUE_SOON_MINUTES) {
        dueSoon.push({ gc: { user: kid.user, chore }, dueAt: dueAt.getTime() });
      }
    }
  }
  dueSoon.sort((a, b) => a.dueAt - b.dueAt);
  return {
    level: overdue.length > 0 ? 'red' : dueSoon.length > 0 ? 'yellow' : 'green',
    total,
    done,
    dueSoon: dueSoon[0]?.gc,
    dueSoonCount: dueSoon.length,
    overdue,
  };
}
