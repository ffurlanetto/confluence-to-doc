import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { App } from '../App';
import { me, mockApi, prefs, renderWithProviders } from '../test/utils';

afterEach(() => {
  vi.unstubAllGlobals();
});

const inDays = (d: number) => new Date(Date.now() + d * 86_400_000).toISOString();

describe('Preferences', () => {
  it('warns when the Confluence token is about to expire', async () => {
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: { ...prefs, patExpiresAt: inDays(5) } }),
    });
    renderWithProviders(<App />, { route: '/settings' });
    expect(await screen.findByText(/Your Confluence token expires on/)).toBeInTheDocument();
    expect(screen.getByText(/create a new token and save it here before then/)).toBeInTheDocument();
  });

  it('says nothing about a token far from its expiry', async () => {
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: { ...prefs, patExpiresAt: inDays(90) } }),
    });
    renderWithProviders(<App />, { route: '/settings' });
    expect(await screen.findByText(/^Expires on/)).toBeInTheDocument();
    expect(screen.queryByText(/Your Confluence token expires/)).not.toBeInTheDocument();
  });

  it('deletes the account after confirmation', async () => {
    const assign = vi.fn();
    vi.stubGlobal('location', { ...window.location, assign });
    let deleted = false;
    mockApi({
      'GET /api/me': () => ({ body: me }),
      'GET /api/preferences': () => ({ body: prefs }),
      'DELETE /api/me': () => {
        deleted = true;
        return { status: 204 };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<App />, { route: '/settings' });
    await user.click(await screen.findByRole('button', { name: 'Delete my account…' }));
    expect(deleted).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Yes, delete everything' }));
    await vi.waitFor(() => expect(assign).toHaveBeenCalledWith('/'));
    expect(deleted).toBe(true);
  });
});
