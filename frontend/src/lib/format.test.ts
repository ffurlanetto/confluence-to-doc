import { describe, expect, it } from 'vitest';

import { formatBytes, formatRemaining } from './format';

describe('formatBytes', () => {
  it.each([
    [undefined, '—'],
    [0, '—'],
    [512, '512 B'],
    [2048, '2.0 KB'],
    [5 * 1024 * 1024, '5.0 MB'],
  ])('formats %s as %s', (input, expected) => {
    expect(formatBytes(input)).toBe(expected);
  });
});

describe('formatRemaining', () => {
  const now = new Date('2026-01-01T00:00:00Z');
  it.each([
    [undefined, '—'],
    ['2025-12-31T23:00:00Z', 'expired'],
    ['2026-01-01T00:00:20Z', '1 min'],
    ['2026-01-01T00:30:00Z', '30 min'],
    ['2026-01-02T23:59:00Z', '47 h'],
  ])('formats %s as %s', (input, expected) => {
    expect(formatRemaining(input, now)).toBe(expected);
  });
});
