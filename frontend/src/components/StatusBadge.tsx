import type { Export } from '../api/types';

const labels: Record<Export['status'], string> = {
  queued: 'En file d’attente',
  running: 'En cours',
  succeeded: 'Prêt',
  failed: 'Échec',
  expired: 'Expiré',
};

export function StatusBadge({ exp }: { exp: Export }) {
  let label = labels[exp.status];
  if (exp.status === 'running' && exp.pagesTotal > 0) {
    label += ` · ${exp.pagesTotal} page(s)`;
  }
  if (exp.status === 'queued' && exp.attempts > 0) {
    label = 'Nouvelle tentative prévue';
  }
  return (
    <span className={`badge badge-${exp.status}`} role="status">
      {label}
    </span>
  );
}
