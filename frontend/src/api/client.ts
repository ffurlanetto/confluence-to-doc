import type {
  AdminExport,
  AdminUser,
  AuditFilter,
  ExportStatus,
  TemplateInfo,
  Usage,
  Inbox,
  AuditPage,
  CreateExportRequest,
  Export,
  ExportFormat,
  Me,
  PageSummary,
  Preferences,
} from './types';

/** Error returned by the API, carrying the stable machine-readable code. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

export const isUnauthenticated = (e: unknown): boolean => e instanceof ApiError && e.status === 401;

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  if (method !== 'GET') {
    // Required by the backend on every state-changing request (CSRF defence).
    headers['X-CSRF-Protection'] = '1';
  }
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  });
  if (res.status === 204) {
    return undefined as T;
  }
  if (!res.ok) {
    throw await failure(res);
  }
  return (await res.json().catch(() => undefined)) as T;
}

async function failure(res: Response): Promise<ApiError> {
  const data: unknown = await res.json().catch(() => undefined);
  const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
  return new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `HTTP error ${res.status}`);
}

/** Sends a file as the raw request body (the template upload). */
async function upload<T>(path: string, file: File): Promise<T> {
  const res = await fetch(path, {
    method: 'PUT',
    headers: {
      Accept: 'application/json',
      'Content-Type': 'application/octet-stream',
      'X-CSRF-Protection': '1',
    },
    body: file,
    credentials: 'same-origin',
  });
  if (!res.ok) {
    throw await failure(res);
  }
  return (await res.json()) as T;
}

const id = encodeURIComponent;

export const api = {
  me: () => request<Me>('GET', '/api/me'),
  deleteAccount: () => request<undefined>('DELETE', '/api/me'),

  notifications: () => request<Inbox>('GET', '/api/notifications'),
  markNotificationsRead: () => request<undefined>('POST', '/api/notifications/read'),
  setNotificationPreferences: (email: boolean, exports: boolean) =>
    request<Preferences>('PUT', '/api/preferences/notifications', { email, exports }),
  setTeamsWebhook: (url: string) => request<Preferences>('PUT', '/api/preferences/teams', { url }),
  deleteTeamsWebhook: () => request<undefined>('DELETE', '/api/preferences/teams'),
  testTeams: () => request<undefined>('POST', '/api/preferences/teams/test'),

  preferences: () => request<Preferences>('GET', '/api/preferences'),
  setDefaultFormat: (defaultFormat: ExportFormat) =>
    request<Preferences>('PUT', '/api/preferences', { defaultFormat }),
  setPat: (token: string) => request<{ confluenceUser: string }>('PUT', '/api/preferences/pat', { token }),
  deletePat: () => request<undefined>('DELETE', '/api/preferences/pat'),

  searchPages: (q: string) =>
    request<{ results: PageSummary[] }>('GET', `/api/confluence/pages?q=${encodeURIComponent(q)}`).then(
      (r) => r.results,
    ),
  children: (pageId: string) =>
    request<{ results: PageSummary[] }>(
      'GET',
      `/api/confluence/pages/${encodeURIComponent(pageId)}/children`,
    ).then((r) => r.results),

  exports: () => request<{ exports: Export[] }>('GET', '/api/exports').then((r) => r.exports),
  createExport: (req: CreateExportRequest) => request<Export>('POST', '/api/exports', req),
  deleteExport: (id: string) => request<undefined>('DELETE', `/api/exports/${encodeURIComponent(id)}`),
  downloadUrl: (id: string) => `/api/exports/${encodeURIComponent(id)}/download`,

  auditEvents: (filter: AuditFilter, before?: string) => {
    const params = new URLSearchParams();
    for (const [key, value] of Object.entries({ ...filter, before })) {
      if (value) params.set(key, value);
    }
    const query = params.toString();
    return request<AuditPage>('GET', `/api/admin/audit${query ? `?${query}` : ''}`);
  },

  adminQueue: (statuses: ExportStatus[]) =>
    request<{ exports: AdminExport[]; limit: number }>(
      'GET',
      `/api/admin/exports${statuses.length ? `?status=${statuses.join(',')}` : ''}`,
    ),
  cancelExport: (exportId: string) => request<Export>('POST', `/api/admin/exports/${id(exportId)}/cancel`),
  retryExport: (exportId: string) => request<Export>('POST', `/api/admin/exports/${id(exportId)}/retry`),
  adminUsers: (q: string) =>
    request<{ users: AdminUser[]; limit: number }>(
      'GET',
      `/api/admin/users${q ? `?q=${encodeURIComponent(q)}` : ''}`,
    ),
  blockUser: (userId: string, reason: string) =>
    request<{ cancelledExports: number }>('POST', `/api/admin/users/${id(userId)}/block`, { reason }),
  unblockUser: (userId: string) => request<undefined>('DELETE', `/api/admin/users/${id(userId)}/block`),
  usage: (days: number) => request<Usage>('GET', `/api/admin/usage?days=${days}`),
  template: () => request<TemplateInfo>('GET', '/api/admin/template'),
  uploadTemplate: (file: File) =>
    upload<TemplateInfo>(`/api/admin/template?name=${encodeURIComponent(file.name)}`, file),
  deleteTemplate: () => request<undefined>('DELETE', '/api/admin/template'),

  logout: () => request<{ logoutUrl?: string }>('POST', '/auth/logout'),
};

export const loginUrl = (returnTo: string) => `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
