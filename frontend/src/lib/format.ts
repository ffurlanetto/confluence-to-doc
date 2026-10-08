const dateFormatter = new Intl.DateTimeFormat('en-GB', { dateStyle: 'short', timeStyle: 'short' });

export const formatDate = (iso: string | undefined): string =>
  iso ? dateFormatter.format(new Date(iso)) : '—';

export const formatBytes = (bytes: number | undefined): string => {
  if (!bytes) return '—';
  const units = ['B', 'KB', 'MB', 'GB'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
};

/** Human-readable remaining time until `iso`, e.g. "47 h", "12 min", or "expired". */
export const formatRemaining = (iso: string | undefined, now: Date = new Date()): string => {
  if (!iso) return '—';
  const ms = new Date(iso).getTime() - now.getTime();
  if (ms <= 0) return 'expired';
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 60) return `${Math.max(minutes, 1)} min`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h`;
};

/** Days left until `iso` (negative once past), rounded down. */
export const daysUntil = (iso: string, now: Date = new Date()): number =>
  Math.floor((new Date(iso).getTime() - now.getTime()) / 86_400_000);

/** How soon before its expiry the user is warned about the Confluence token. */
export const PAT_WARNING_DAYS = 14;
