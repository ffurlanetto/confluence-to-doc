import { fileURLToPath } from 'node:url';

import { expect, test } from '@playwright/test';

import { exportAndWait, savePAT, signIn, zipText } from './helpers';

const template = fileURLToPath(new URL('../fixtures/company-template.dotx', import.meta.url));

test('the company Word template is uploaded, applied to the next export, then removed', async ({ page }) => {
  await signIn(page);
  await savePAT(page.request);

  await page.getByRole('link', { name: 'Administration' }).click();
  await page
    .getByRole('navigation', { name: 'Administration' })
    .getByRole('link', { name: 'Word template' })
    .click();
  await expect(page.getByText('None: documents use the built-in styling')).toBeVisible();

  await page.getByLabel('Template file').setInputFiles(template);
  await page.getByRole('button', { name: 'Upload' }).click();
  await expect(page.getByText('The template is in use for the next exports.')).toBeVisible();
  await expect(page.getByText('Uploaded from this console')).toBeVisible();
  await expect(page.getByText('company-template.dotx')).toBeVisible();

  const done = await exportAndWait(page.request, { pageId: '901', format: 'docx', includeChildren: false });
  expect(done.status, done.error).toBe('succeeded');
  const docx = await (await page.request.get(`/api/exports/${done.id}/download`)).body();
  const headers = zipText(docx, /^word\/.*header.*\.xml$/);
  expect(headers).toContain('E2E COMPANY TEMPLATE');

  await page.getByRole('button', { name: 'Remove the uploaded template' }).click();
  await page.getByRole('button', { name: 'Confirm the removal' }).click();
  await expect(page.getByText('None: documents use the built-in styling')).toBeVisible();

  // A file that is not a template is refused, with the reason.
  await page.getByLabel('Template file').setInputFiles({
    name: 'notes.pdf',
    mimeType: 'application/pdf',
    buffer: Buffer.from('%PDF-1.7 not a template'),
  });
  await page.getByRole('button', { name: 'Upload' }).click();
  await expect(page.getByRole('alert')).toContainText('not a usable Word template');
});

test('the administration console shows the queue, the users, usage and the audit trail', async ({ page }) => {
  await signIn(page);
  await savePAT(page.request);
  const done = await exportAndWait(page.request, { pageId: '902', format: 'pdf', includeChildren: false });
  expect(done.status, done.error).toBe('succeeded');

  await page.goto('/admin');
  await expect(page.getByRole('heading', { name: 'Export queue' })).toBeVisible();
  await page.getByLabel('Show').selectOption('all');
  await expect(page.getByRole('row').filter({ hasText: 'Tables' }).first()).toContainText('dev@example.com');

  await page.getByRole('navigation', { name: 'Administration' }).getByRole('link', { name: 'Users' }).click();
  const me = page.getByRole('row').filter({ hasText: 'dev@example.com' });
  await expect(me).toContainText('Administrator');
  // Nobody can lock themselves out.
  await expect(page.getByRole('button', { name: 'Block dev@example.com' })).toHaveCount(0);

  await page.getByRole('navigation', { name: 'Administration' }).getByRole('link', { name: 'Usage' }).click();
  const generated = page
    .getByRole('table', { name: 'Summary' })
    .getByRole('row', { name: /Documents generated/ });
  await expect(generated).not.toHaveText(/Documents generated\s*0$/);

  await page.getByRole('navigation', { name: 'Administration' }).getByRole('link', { name: 'Audit' }).click();
  await page.getByLabel('Action').selectOption('admin.template.upload');
  await page.getByRole('button', { name: 'Apply' }).click();
  await expect(page.getByRole('row').filter({ hasText: 'Word template uploaded' }).first()).toBeVisible();
});
