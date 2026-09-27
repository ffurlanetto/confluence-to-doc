import { screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { mockApi, prefs, renderWithProviders } from '../test/utils';
import { NewExportPage } from './NewExportPage';

afterEach(() => {
  vi.unstubAllGlobals();
});

const page = {
  id: '10',
  title: 'Guide utilisateur',
  spaceKey: 'DOC',
  spaceName: 'Documentation',
  webUrl: '',
};

describe('NewExportPage', () => {
  it('searches a page, previews its tree and enqueues an export', async () => {
    let created: unknown;
    mockApi({
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/confluence/pages': (_init, url) => ({
        body: { results: url.searchParams.get('q') === 'guide' ? [page] : [] },
      }),
      'GET /api/confluence/pages/10/children': () => ({
        body: { results: [{ ...page, id: '11', title: 'Installation' }] },
      }),
      'GET /api/confluence/pages/11/children': () => ({ body: { results: [] } }),
      'POST /api/exports': (init) => {
        created = JSON.parse(String(init?.body));
        return { status: 202, body: { id: 'e1' } };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<NewExportPage />, { route: '/new' });

    await user.type(screen.getByLabelText(/Rechercher une page/), 'guide');
    await user.click(await screen.findByRole('button', { name: /Guide utilisateur/ }));

    // Tree preview loads the children lazily.
    expect(await screen.findByText('Installation')).toBeInTheDocument();
    // The default format comes from the user's preferences.
    expect(await screen.findByRole('radio', { name: 'Word (.docx)' })).toBeChecked();

    await user.click(screen.getByRole('radio', { name: 'PDF' }));
    await user.click(screen.getByRole('button', { name: 'Lancer l’export' }));

    await vi.waitFor(() => expect(created).toEqual({ pageId: '10', format: 'pdf', includeChildren: true }));
  });

  it('displays API errors', async () => {
    mockApi({
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/confluence/pages': () => ({
        status: 409,
        body: {
          error: { code: 'pat_invalid', message: 'Le jeton d’accès Confluence est invalide ou expiré.' },
        },
      }),
    });
    renderWithProviders(<NewExportPage />, { route: '/new' });
    await userEvent.type(screen.getByLabelText(/Rechercher une page/), 'guide');
    expect(
      await screen.findByText('Le jeton d’accès Confluence est invalide ou expiré.'),
    ).toBeInTheDocument();
  });
});
