import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { me, mockApi, prefs, renderWithProviders } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

const admin = { ...me, isAdmin: true };

const base = {
  'GET /api/me': () => ({ body: admin }),
  'GET /api/preferences': () => ({ body: prefs }),
};

const exportOf = (id: string, status: string, extra: Record<string, unknown> = {}) => ({
  id,
  pageId: '42',
  title: `Guide ${id}`,
  format: 'pdf',
  includeChildren: true,
  status,
  attempts: 1,
  pagesDone: 0,
  pagesTotal: 0,
  createdAt: '2026-10-08T09:00:00Z',
  ownerId: 'u9',
  ownerEmail: 'bob@example.com',
  ...extra,
});

describe('Administration', () => {
  it('is offered to administrators only', async () => {
    mockApi({
      ...base,
      'GET /api/me': () => ({ body: me }),
      'GET /api/exports': () => ({ body: { exports: [] } }),
    });
    renderWithProviders(<App />, { route: '/admin/queue' });
    expect(await screen.findByRole('heading', { name: 'My exports' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Administration' })).not.toBeInTheDocument();
  });

  it('cancels a running export and retries a failed one', async () => {
    const api = mockApi({
      ...base,
      'GET /api/admin/exports': (_init, url) =>
        url.searchParams.get('status') === 'failed'
          ? {
              body: {
                exports: [exportOf('e2', 'failed', { error: 'Cancelled by an administrator.' })],
                limit: 200,
              },
            }
          : { body: { exports: [exportOf('e1', 'running')], limit: 200 } },
      'POST /api/admin/exports/e1/cancel': () => ({ body: exportOf('e1', 'failed') }),
      'POST /api/admin/exports/e2/retry': () => ({ body: exportOf('e2', 'queued') }),
    });
    renderWithProviders(<App />, { route: '/admin' });

    expect(await screen.findByRole('heading', { name: 'Export queue' })).toBeInTheDocument();
    const first = new URL(
      String(api.mock.calls.find((c) => String(c[0]).includes('/admin/exports'))?.[0]),
      'http://x',
    );
    expect(first.searchParams.get('status')).toBe('queued,running');
    expect(await screen.findByText('bob@example.com')).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole('button', { name: 'Cancel the export of Guide e1 for bob@example.com' }),
    );
    expect(api).toHaveBeenCalledWith(
      '/api/admin/exports/e1/cancel',
      expect.objectContaining({ method: 'POST' }),
    );

    await userEvent.selectOptions(screen.getByLabelText('Show'), 'failed');
    expect(await screen.findByText('Cancelled by an administrator.')).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole('button', { name: 'Retry the export of Guide e2 for bob@example.com' }),
    );
    expect(api).toHaveBeenCalledWith(
      '/api/admin/exports/e2/retry',
      expect.objectContaining({ method: 'POST' }),
    );
  });

  it('blocks a user with a reason, and never offers to block oneself', async () => {
    let blocked = false;
    const api = mockApi({
      ...base,
      'GET /api/admin/users': () => ({
        body: {
          users: [
            {
              id: 'u1',
              email: 'alice@example.com',
              name: 'Alice',
              isAdmin: true,
              createdAt: '2026-01-01T00:00:00Z',
              lastActiveAt: '2026-10-08T08:00:00Z',
              activeExports: 0,
              totalExports: 3,
            },
            {
              id: 'u9',
              email: 'bob@example.com',
              name: 'Bob',
              isAdmin: false,
              createdAt: '2026-01-01T00:00:00Z',
              lastActiveAt: '2026-10-07T08:00:00Z',
              activeExports: 1,
              totalExports: 12,
              ...(blocked ? { blockedAt: '2026-10-08T10:00:00Z', blockedReason: 'Left the company' } : {}),
            },
          ],
          limit: 200,
        },
      }),
      'POST /api/admin/users/u9/block': () => {
        blocked = true;
        return { body: { cancelledExports: 1 } };
      },
      'DELETE /api/admin/users/u9/block': () => ({ status: 204 }),
    });
    renderWithProviders(<App />, { route: '/admin/users' });

    expect(await screen.findByText('bob@example.com')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Block alice@example.com' })).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Block bob@example.com' }));
    const form = screen.getByRole('form', { name: 'Block bob@example.com' });
    const confirm = within(form).getByRole('button', { name: 'Confirm the block' });
    expect(confirm).toBeDisabled();
    await userEvent.type(
      within(form).getByLabelText(/Reason for blocking bob@example.com/),
      'Left the company',
    );
    await userEvent.click(confirm);

    const call = api.mock.calls.find((c) => String(c[0]) === '/api/admin/users/u9/block');
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ reason: 'Left the company' });
    expect(await screen.findByText(/Left the company/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Unblock bob@example.com' }));
    expect(api).toHaveBeenCalledWith(
      '/api/admin/users/u9/block',
      expect.objectContaining({ method: 'DELETE' }),
    );
  });

  it('shows usage over the chosen period', async () => {
    const api = mockApi({
      ...base,
      'GET /api/admin/usage': (_init, url) => ({
        body: {
          from: '2026-09-09T00:00:00Z',
          succeeded: url.searchParams.get('days') === '7' ? 4 : 120,
          failed: 3,
          pages: 2400,
          bytes: 5 * 1024 * 1024,
          users: 17,
          byFormat: { pdf: 80, docx: 40 },
          daily: [{ day: '2026-10-08', succeeded: 5, failed: 1 }],
          topUsers: [{ userId: 'u9', email: 'bob@example.com', exports: 30, pages: 600 }],
        },
      }),
    });
    renderWithProviders(<App />, { route: '/admin/usage' });

    const summary = await screen.findByRole('table', { name: 'Summary' });
    expect(within(summary).getByRole('row', { name: 'Documents generated 120' })).toBeInTheDocument();
    expect(within(summary).getByText('5.0 MB')).toBeInTheDocument();
    expect(within(summary).getByText('PDF 80 · Word 40')).toBeInTheDocument();
    expect(
      within(screen.getByRole('table', { name: 'Most active users' })).getByText('bob@example.com'),
    ).toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText('Period'), '7');
    expect(await within(summary).findByRole('row', { name: 'Documents generated 4' })).toBeInTheDocument();
    expect(String(api.mock.calls.at(-1)?.[0])).toBe('/api/admin/usage?days=7');
  });

  it('uploads a Word template and reports a refused one', async () => {
    let uploaded = false;
    const api = mockApi({
      ...base,
      'GET /api/admin/template': () => ({
        body: uploaded
          ? {
              origin: 'uploaded',
              name: 'ACME.dotx',
              defaultParagraphStyle: 'Normal',
              styles: 40,
              uploadedAt: '2026-10-08T10:00:00Z',
              uploadedBy: 'alice@example.com',
              configured: 'server.dotx',
              maxBytes: 10485760,
            }
          : {
              origin: 'configured',
              name: 'server.dotx',
              styles: 12,
              configured: 'server.dotx',
              maxBytes: 10485760,
            },
      }),
      'PUT /api/admin/template': (_init, url) => {
        if (url.searchParams.get('name') === 'evil.dotx') {
          return {
            status: 400,
            body: { error: { code: 'unsafe_template', message: 'The template refers to external content.' } },
          };
        }
        uploaded = true;
        return { body: { origin: 'uploaded', name: 'ACME.dotx', styles: 40, maxBytes: 10485760 } };
      },
      'DELETE /api/admin/template': () => {
        uploaded = false;
        return { status: 204 };
      },
    });
    renderWithProviders(<App />, { route: '/admin/template' });

    expect(await screen.findByText('Configured on the server (WORD_TEMPLATE_PATH)')).toBeInTheDocument();
    const fileInput = screen.getByLabelText('Template file');

    await userEvent.upload(fileInput, new File(['x'], 'evil.dotx'));
    await userEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('external content');

    const file = new File(['PK'], 'ACME.dotx');
    await userEvent.upload(fileInput, file);
    await userEvent.click(screen.getByRole('button', { name: 'Upload' }));
    expect(await screen.findByText('The template is in use for the next exports.')).toBeInTheDocument();
    const put = api.mock.calls.filter((c) => c[1]?.method === 'PUT').at(-1);
    expect(String(put?.[0])).toBe('/api/admin/template?name=ACME.dotx');
    expect(put?.[1]?.body).toBe(file);
    expect(new Headers(put?.[1]?.headers).get('X-CSRF-Protection')).toBe('1');
    expect(await screen.findByText(/by alice@example.com/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Remove the uploaded template' }));
    expect(screen.getByText("Documents then use the server's template, server.dotx.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Confirm the removal' }));
    expect(await screen.findByText('Configured on the server (WORD_TEMPLATE_PATH)')).toBeInTheDocument();
  });

  it('refuses a file larger than the limit before sending it', async () => {
    const api = mockApi({
      ...base,
      'GET /api/admin/template': () => ({ body: { origin: 'none', styles: 0, maxBytes: 4 } }),
    });
    renderWithProviders(<App />, { route: '/admin/template' });
    await userEvent.upload(await screen.findByLabelText('Template file'), new File(['12345'], 'big.dotx'));
    expect(screen.getByRole('alert')).toHaveTextContent('larger than');
    expect(screen.getByRole('button', { name: 'Upload' })).toBeDisabled();
    expect(api.mock.calls.some((c) => c[1]?.method === 'PUT')).toBe(false);
  });
});
