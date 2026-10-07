import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { me, mockApi, prefs, renderWithProviders } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

const admin = { ...me, isAdmin: true };

const event = (id: string, action: string, extra: Record<string, unknown> = {}) => ({
  id,
  occurredAt: '2026-10-07T09:30:00Z',
  actorEmail: 'bob@example.com',
  action,
  outcome: 'success',
  clientIp: '203.0.113.7',
  ...extra,
});

describe('Audit trail', () => {
  it('is reachable only by administrators', async () => {
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/exports': () => ({ body: { exports: [] } }),
    });
    renderWithProviders(<App />, { route: '/admin/audit' });
    expect(await screen.findByRole('heading', { name: 'My exports' })).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Audit' })).not.toBeInTheDocument();
  });

  it('lists events, filters them and loads older pages', async () => {
    const api = mockApi({
      'GET /api/me': () => ({ body: admin }),
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/admin/audit': (_init, url) => {
        if (url.searchParams.get('before') === 'e2') {
          return { body: { events: [event('e3', 'auth.login')] } };
        }
        if (url.searchParams.get('action') === 'export.download') {
          return { body: { events: [event('e9', 'export.download', { outcome: 'denied' })] } };
        }
        return {
          body: {
            events: [
              event('e1', 'export.download', {
                targetType: 'export',
                targetId: 'x-1',
                details: { title: 'Guide', size: 2048 },
              }),
              event('e2', 'export.expire', { actorEmail: undefined }),
            ],
            nextCursor: 'e2',
          },
        };
      },
    });
    renderWithProviders(<App />, { route: '/admin/audit' });

    expect(await screen.findByRole('heading', { name: 'Audit trail' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Audit' })).toBeInTheDocument();
    const [, download, expiry, ...rest] = await screen.findAllByRole('row');
    expect(rest).toHaveLength(0); // header + 2 events
    if (!download || !expiry) throw new Error('missing rows');
    expect(within(download).getByText('Document downloaded')).toBeInTheDocument();
    expect(within(download).getByText('Guide')).toBeInTheDocument();
    expect(within(download).getByText('203.0.113.7')).toBeInTheDocument();
    expect(within(expiry).getByText('System')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: 'Load older events' }));
    expect(await screen.findAllByRole('row')).toHaveLength(4);
    expect(screen.queryByRole('button', { name: 'Load older events' })).not.toBeInTheDocument();

    await userEvent.selectOptions(screen.getByLabelText('Action'), 'export.download');
    await userEvent.type(screen.getByLabelText('User'), ' bob ');
    await userEvent.click(screen.getByRole('button', { name: 'Apply' }));
    expect(await screen.findByText('denied')).toBeInTheDocument();
    const last = new URL(String(api.mock.calls.at(-1)?.[0]), 'http://localhost');
    expect(last.searchParams.get('action')).toBe('export.download');
    expect(last.searchParams.get('actor')).toBe('bob');
    expect(last.searchParams.has('from')).toBe(false);
  });
});
