// Types mirroring the backend JSON contract (backend/internal/httpapi).

export type ExportFormat = 'pdf' | 'docx';

export type ExportStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'expired';

export interface Me {
  id: string;
  email: string;
  name: string;
  /** Granted by the identity provider's groups (OIDC_ADMIN_GROUPS). */
  isAdmin: boolean;
}

export interface Preferences {
  confluenceBaseUrl: string;
  hasPat: boolean;
  patUpdatedAt?: string;
  /** When the token stops working, if Confluence reported it. */
  patExpiresAt?: string;
  defaultFormat: ExportFormat;
  retentionHours: number;
  maxPages: number;
  /** Company Word template applied to generated documents; absent when the built-in styling is used. */
  documentTemplate?: string;
  /** Levels offered when exporting; empty when the choice is disabled. */
  classifications: Classification[];
  /** Applied when the user picks no classification. */
  defaultClassification?: string;
}

export interface Classification {
  label: string;
  /** The label is set diagonally across every page. */
  watermark: boolean;
}

export interface PageSummary {
  id: string;
  title: string;
  spaceKey: string;
  spaceName: string;
  webUrl: string;
}

export interface Export {
  id: string;
  pageId: string;
  title: string;
  format: ExportFormat;
  includeChildren: boolean;
  classification?: string;
  status: ExportStatus;
  error?: string;
  attempts: number;
  pagesDone: number;
  pagesTotal: number;
  fileSize?: number;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  expiresAt?: string;
}

export interface CreateExportRequest {
  pageId: string;
  format: ExportFormat;
  includeChildren: boolean;
  /** One of Preferences.classifications, or empty for none. */
  classification?: string;
}

export type AuditOutcome = 'success' | 'failure' | 'denied';

export interface AuditEvent {
  id: string;
  occurredAt: string;
  actorId?: string;
  actorEmail?: string;
  action: string;
  outcome: AuditOutcome;
  targetType?: string;
  targetId?: string;
  clientIp?: string;
  userAgent?: string;
  requestId?: string;
  details?: Record<string, unknown>;
}

export interface AuditPage {
  events: AuditEvent[];
  /** Present when older events remain: pass it as `before` to get them. */
  nextCursor?: string;
}

export interface AuditFilter {
  actor?: string;
  action?: string;
  /** RFC 3339 timestamps. */
  from?: string;
  to?: string;
}
