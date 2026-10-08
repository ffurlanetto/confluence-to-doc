import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { me, mockApi, prefs, renderWithProviders } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('Notifications', () => {
  it('shows the unread count and marks everything read when opened', async () => {
    let unread = 2;
    let markedRead = false;
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/exports': () => ({ body: { exports: [] } }),
      'GET /api/notifications': () => ({
        body: {
          unread,
          notifications: [
            {
              id: 'n1',
              kind: 'export.succeeded',
              title: 'Your export of “Guide” is ready',
              body: 'Ready.',
              createdAt: new Date().toISOString(),
            },
          ],
        },
      }),
      'POST /api/notifications/read': () => {
        markedRead = true;
        unread = 0;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<App />, { route: '/' });
    const link = await screen.findByRole('link', { name: 'Notifications, 2 unread' });
    await user.click(link);
    expect(await screen.findByText('Your export of “Guide” is ready')).toBeInTheDocument();
    await vi.waitFor(() => expect(markedRead).toBe(true));
  });
});

describe('Notification settings', () => {
  it('saves the Teams workflow and sends a test message', async () => {
    let saved: unknown;
    let tested = false;
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'PUT /api/preferences/teams': (init) => {
        saved = JSON.parse(String(init?.body));
        return { body: { ...prefs, hasTeamsWebhook: true } };
      },
      'POST /api/preferences/teams/test': () => {
        tested = true;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<App />, { route: '/settings' });
    await user.type(await screen.findByLabelText('Workflow URL'), 'https://prod.logic.azure.com/w');
    await user.click(screen.getByRole('button', { name: 'Save' }));
    expect(saved).toEqual({ url: 'https://prod.logic.azure.com/w' });
    await user.click(await screen.findByRole('button', { name: 'Send a test message' }));
    expect(await screen.findByText('Test message sent: check Teams.')).toBeInTheDocument();
    expect(tested).toBe(true);
  });

  it('turns email off', async () => {
    let body: unknown;
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'PUT /api/preferences/notifications': (init) => {
        body = JSON.parse(String(init?.body));
        return { body: { ...prefs, notifyEmail: false } };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<App />, { route: '/settings' });
    await user.click(await screen.findByRole('checkbox', { name: /By email to alice@example.com/ }));
    expect(body).toEqual({ email: false, exports: true });
  });
});
