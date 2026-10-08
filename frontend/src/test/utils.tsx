import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import type { ReactElement } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { vi } from 'vitest';

type Handler = (init: RequestInit | undefined, url: URL) => { status?: number; body?: unknown };

/**
 * Installs a fetch mock routing "METHOD /path" to handlers. Unhandled calls
 * fail the test loudly. Returns the mock to assert on calls.
 */
export function mockApi(routes: Record<string, Handler>) {
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(typeof input === 'string' ? input : input.toString(), 'http://localhost');
    const key = `${init?.method ?? 'GET'} ${url.pathname}`;
    const handler = routes[key];
    if (!handler) {
      throw new Error(`Unexpected request: ${key}`);
    }
    const { status = 200, body } = handler(init, url);
    return new Response(body === undefined ? null : JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json' },
    });
  });
  vi.stubGlobal('fetch', fn);
  return fn;
}

export function renderWithProviders(ui: ReactElement, { route = '/' }: { route?: string } = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[route]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

export const prefs = {
  confluenceBaseUrl: 'https://confluence.example.com',
  hasPat: true,
  defaultFormat: 'docx',
  retentionHours: 48,
  maxPages: 500,
  classifications: [
    { label: 'Internal', watermark: false },
    { label: 'Confidential', watermark: true },
  ],
  defaultClassification: 'Internal',
};

export const me = { id: 'u1', email: 'alice@example.com', name: 'Alice', isAdmin: false };
