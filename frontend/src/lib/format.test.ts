import { describe, expect, it } from 'vitest';

import { formatBytes, formatRemaining } from './format';

describe('formatBytes', () => {
  it.each([
    [undefined, '—'],
    [0, '—'],
    [512, '512 o'],
    [2048, '2,0 Ko'],
    [5 * 1024 * 1024, '5,0 Mo'],
  ])('formats %s as %s', (input, expected) => {
    expect(formatBytes(input)).toBe(expected);
  });
});

describe('formatRemaining', () => {
  const now = new Date('2026-01-01T00:00:00Z');
  it.each([
    [undefined, '—'],
    ['2025-12-31T23:00:00Z', 'expiré'],
    ['2026-01-01T00:00:20Z', '1 min'],
    ['2026-01-01T00:30:00Z', '30 min'],
    ['2026-01-02T23:59:00Z', '47 h'],
  ])('formats %s as %s', (input, expected) => {
    expect(formatRemaining(input, now)).toBe(expected);
  });
});
