// Pure date helpers shared by the merged Home screen (and its tests):
// the week's Monday-first dates and the month stepper's key math. No
// React, so choreView.test.ts can import them without pulling screens.

import { localDateStr } from '../../utils';

/** Monday-first dates (YYYY-MM-DD) of the week that holds `day`. */
export function weekDates(day: string): string[] {
  const d = new Date(`${day}T00:00:00`);
  const offset = (d.getDay() + 6) % 7; // Monday = 0
  d.setDate(d.getDate() - offset);
  return Array.from({ length: 7 }, (_, i) => {
    const x = new Date(d);
    x.setDate(d.getDate() + i);
    return localDateStr(x);
  });
}

/** "2026-10" style month key of a YYYY-MM-DD date. */
export const monthKey = (date: string) => date.slice(0, 7);

/** First day (YYYY-MM-DD) of the month holding `date`. */
export const monthStart = (date: string) => `${monthKey(date)}-01`;

/** The month key before/after "YYYY-MM", stepping over year bounds. */
export function shiftMonth(key: string, by: number): string {
  const [y, m] = key.split('-').map(Number);
  const d = new Date(y, m - 1 + by, 1);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}`;
}
