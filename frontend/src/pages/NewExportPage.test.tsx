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

    await user.type(screen.getByLabelText(/Search for a page/), 'guide');
    await user.click(await screen.findByRole('button', { name: /Guide utilisateur/ }));

    // Tree preview loads the children lazily.
    expect(await screen.findByText('Installation')).toBeInTheDocument();
    // The default format comes from the user's preferences.
    expect(await screen.findByRole('radio', { name: 'Word (.docx)' })).toBeChecked();

    await user.click(screen.getByRole('radio', { name: 'PDF' }));
    // No classification chosen: the configured default is announced.
    expect(screen.getByRole('option', { name: 'Default (Internal)' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Start export' }));

    await vi.waitFor(() =>
      expect(created).toEqual({ pageId: '10', format: 'pdf', includeChildren: true, classification: '' }),
    );
  });

  it('sends the chosen classification and warns about the watermark', async () => {
    let created: { classification?: string } | undefined;
    mockApi({
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/confluence/pages': () => ({ body: { results: [page] } }),
      'GET /api/confluence/pages/10/children': () => ({ body: { results: [] } }),
      'POST /api/exports': (init) => {
        created = JSON.parse(String(init?.body)) as { classification?: string };
        return { status: 202, body: { id: 'e1' } };
      },
    });
    const user = userEvent.setup();
    renderWithProviders(<NewExportPage />, { route: '/new' });
    await user.type(screen.getByLabelText(/Search for a page/), 'guide');
    await user.click(await screen.findByRole('button', { name: /Guide utilisateur/ }));

    await user.selectOptions(await screen.findByLabelText('Classification'), 'Confidential');
    expect(screen.getByText(/“CONFIDENTIAL” will be printed diagonally/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Start export' }));
    await vi.waitFor(() => expect(created?.classification).toBe('Confidential'));
  });

  it('hides the classification when none is configured', async () => {
    mockApi({
      'GET /api/preferences': () => ({ body: { ...prefs, classifications: [] } }),
      'GET /api/confluence/pages': () => ({ body: { results: [page] } }),
      'GET /api/confluence/pages/10/children': () => ({ body: { results: [] } }),
    });
    const user = userEvent.setup();
    renderWithProviders(<NewExportPage />, { route: '/new' });
    await user.type(screen.getByLabelText(/Search for a page/), 'guide');
    await user.click(await screen.findByRole('button', { name: /Guide utilisateur/ }));
    expect(await screen.findByRole('button', { name: 'Start export' })).toBeInTheDocument();
    expect(screen.queryByLabelText('Classification')).not.toBeInTheDocument();
  });

  it('displays API errors', async () => {
    mockApi({
      'GET /api/preferences': () => ({ body: prefs }),
      'GET /api/confluence/pages': () => ({
        status: 409,
        body: {
          error: {
            code: 'pat_invalid',
            message: 'The Confluence personal access token is invalid or expired.',
          },
        },
      }),
    });
    renderWithProviders(<NewExportPage />, { route: '/new' });
    await userEvent.type(screen.getByLabelText(/Search for a page/), 'guide');
    expect(
      await screen.findByText('The Confluence personal access token is invalid or expired.'),
    ).toBeInTheDocument();
  });
});
