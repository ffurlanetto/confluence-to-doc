import { inflateRawSync } from 'node:zlib';

import { expect, type APIRequestContext, type Page } from '@playwright/test';

export const origin = 'http://localhost:8080';

/** State-changing API calls need the CSRF header and a same-origin Origin. */
export const write = { 'X-CSRF-Protection': '1', Origin: origin };

/** Signs in through the fake identity provider, which approves at once. */
export async function signIn(page: Page) {
  await page.goto('/');
  await page.getByRole('link', { name: 'Sign in' }).click();
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
}

/** Saves the token the fake Confluence accepts. */
export async function savePAT(request: APIRequestContext) {
  const res = await request.put('/api/preferences/pat', { headers: write, data: { token: 'dev-pat' } });
  expect(res.ok()).toBeTruthy();
}

export interface ExportDTO {
  id: string;
  status: string;
  error?: string;
}

/** Requests an export through the API and waits until it is finished. */
export async function exportAndWait(
  request: APIRequestContext,
  body: { pageId: string; format: 'pdf' | 'docx'; includeChildren: boolean },
): Promise<ExportDTO> {
  const res = await request.post('/api/exports', { headers: write, data: body });
  expect(res.status()).toBe(202);
  const { id } = (await res.json()) as ExportDTO;
  let current: ExportDTO = { id, status: 'queued' };
  await expect
    .poll(
      async () => {
        current = (await (await request.get(`/api/exports/${id}`)).json()) as ExportDTO;
        return current.status;
      },
      { timeout: 180_000, intervals: [1000] },
    )
    .toMatch(/succeeded|failed/);
  return current;
}

/** The entries of a ZIP package (a .docx), enough for small test documents. */
export function zipEntries(zip: Buffer): Map<string, string> {
  const entries = new Map<string, string>();
  // The central directory lists every entry with its local header offset.
  const eocd = zip.lastIndexOf(Buffer.from([0x50, 0x4b, 0x05, 0x06]));
  let p = zip.readUInt32LE(eocd + 16);
  const count = zip.readUInt16LE(eocd + 10);
  for (let i = 0; i < count; i++) {
    const method = zip.readUInt16LE(p + 10);
    const size = zip.readUInt32LE(p + 20);
    const nameLength = zip.readUInt16LE(p + 28);
    const extra = zip.readUInt16LE(p + 30);
    const comment = zip.readUInt16LE(p + 32);
    const local = zip.readUInt32LE(p + 42);
    const name = zip.toString('utf8', p + 46, p + 46 + nameLength);
    const start = local + 30 + zip.readUInt16LE(local + 26) + zip.readUInt16LE(local + 28);
    const data = zip.subarray(start, start + size);
    entries.set(name, (method === 8 ? inflateRawSync(data) : data).toString('utf8'));
    p += 46 + nameLength + extra + comment;
  }
  return entries;
}

/** The text of the parts whose name matches, joined. */
export function zipText(zip: Buffer, name: RegExp): string {
  return [...zipEntries(zip)]
    .filter(([entry]) => name.test(entry))
    .map(([, content]) => content)
    .join('\n');
}
