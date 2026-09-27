const dateFormatter = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'short', timeStyle: 'short' });

export const formatDate = (iso: string | undefined): string =>
  iso ? dateFormatter.format(new Date(iso)) : '—';

export const formatBytes = (bytes: number | undefined): string => {
  if (!bytes) return '—';
  const units = ['o', 'Ko', 'Mo', 'Go'];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit++;
  }
  return `${value.toFixed(unit === 0 ? 0 : 1).replace('.', ',')} ${units[unit]}`;
};

/** Human-readable remaining time until `iso`, e.g. "47 h", "12 min", or "expiré". */
export const formatRemaining = (iso: string | undefined, now: Date = new Date()): string => {
  if (!iso) return '—';
  const ms = new Date(iso).getTime() - now.getTime();
  if (ms <= 0) return 'expiré';
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 60) return `${Math.max(minutes, 1)} min`;
  const hours = Math.floor(minutes / 60);
  return `${hours} h`;
};
