import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { App } from './App';
import { me, mockApi, prefs, renderWithProviders } from './test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

const hourFromNow = (h: number) => new Date(Date.now() + h * 3600_000).toISOString();

describe('App', () => {
  it('shows the login screen when the session is missing', async () => {
    mockApi({
      'GET /api/me': () => ({ status: 401, body: { error: { code: 'unauthorized', message: 'x' } } }),
    });
    renderWithProviders(<App />, { route: '/new' });
    const link = await screen.findByRole('link', { name: 'Sign in' });
    expect(link).toHaveAttribute('href', expect.stringContaining('/auth/login?return_to='));
  });

  it('lists exports with download link and remaining time', async () => {
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/exports': () => ({
        body: {
          exports: [
            {
              id: 'e1',
              pageId: '1',
              title: 'Guide',
              format: 'pdf',
              includeChildren: true,
              status: 'succeeded',
              attempts: 1,
              pagesDone: 3,
              pagesTotal: 3,
              fileSize: 2048,
              createdAt: new Date().toISOString(),
              expiresAt: hourFromNow(47.5),
            },
            {
              id: 'e2',
              pageId: '2',
              title: 'Broken',
              format: 'docx',
              includeChildren: false,
              status: 'failed',
              error: 'The Confluence page cannot be found.',
              attempts: 1,
              pagesDone: 0,
              pagesTotal: 0,
              createdAt: new Date().toISOString(),
            },
          ],
        },
      }),
    });
    renderWithProviders(<App />);

    const download = await screen.findByRole('link', { name: /Download/ });
    expect(download).toHaveAttribute('href', '/api/exports/e1/download');
    expect(screen.getByText('47 h')).toBeInTheDocument();
    expect(screen.getByText('The Confluence page cannot be found.')).toBeInTheDocument();
    expect(screen.getByText('Failed')).toBeInTheDocument();
  });

  it('warns when no PAT is configured', async () => {
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: { ...prefs, hasPat: false } }),
      'GET /api/exports': () => ({ body: { exports: [] } }),
    });
    renderWithProviders(<App />);
    expect(await screen.findByRole('alert')).toHaveTextContent('No Confluence personal access token');
    expect(screen.getByText('You have not started any export yet.')).toBeInTheDocument();
  });

  it('sends the CSRF header when deleting an export', async () => {
    const fetchMock = mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/exports': () => ({
        body: {
          exports: [
            {
              id: 'e1',
              pageId: '1',
              title: 'Guide',
              format: 'pdf',
              includeChildren: true,
              status: 'queued',
              attempts: 0,
              pagesDone: 0,
              pagesTotal: 0,
              createdAt: new Date().toISOString(),
            },
          ],
        },
      }),
      'DELETE /api/exports/e1': () => ({ status: 204 }),
    });
    renderWithProviders(<App />);
    await userEvent.click(await screen.findByRole('button', { name: 'Delete export Guide' }));

    const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'DELETE');
    expect(call).toBeDefined();
    expect((call?.[1]?.headers as Record<string, string>)['X-CSRF-Protection']).toBe('1');
  });
});
