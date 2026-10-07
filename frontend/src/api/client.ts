import type {
  AuditFilter,
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
  const data: unknown = await res.json().catch(() => undefined);
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string } } | undefined)?.error;
    throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `Erreur HTTP ${res.status}`);
  }
  return data as T;
}

export const api = {
  me: () => request<Me>('GET', '/api/me'),

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

  logout: () => request<{ logoutUrl?: string }>('POST', '/auth/logout'),
};

export const loginUrl = (returnTo: string) => `/auth/login?return_to=${encodeURIComponent(returnTo)}`;
