import { useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router-dom';

import { ApiError } from '../api/client';
import { useCreateExport, usePreferences, useSearchPages } from '../api/hooks';
import type { Classification, ExportFormat, PageSummary } from '../api/types';
import { PageTree } from '../components/PageTree';
import { useDebounce } from '../lib/useDebounce';

export function NewExportPage() {
  const [query, setQuery] = useState('');
  const debounced = useDebounce(query.trim(), 300);
  const search = useSearchPages(debounced);
  const [selected, setSelected] = useState<PageSummary | null>(null);

  return (
    <section>
      <h1>New export</h1>
      <label htmlFor="search" className="label">
        Search for a page (title, ID or Confluence URL)
      </label>
      <input
        id="search"
        type="search"
        className="input"
        placeholder="e.g. User guide, 123456 or https://confluence…/pages/viewpage.action?pageId=123456"
        value={query}
        autoComplete="off"
        onChange={(e) => setQuery(e.target.value)}
      />

      {search.isFetching && <p className="muted">Searching…</p>}
      {search.isError && <p className="error">{errorMessage(search.error)}</p>}
      {search.data && search.data.length === 0 && <p className="muted">No page found.</p>}
      {search.data && search.data.length > 0 && (
        <ul className="results" aria-label="Search results">
          {search.data.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                className={selected?.id === p.id ? 'result selected' : 'result'}
                aria-pressed={selected?.id === p.id}
                onClick={() => setSelected(p)}
              >
                <strong>{p.title}</strong>
                <span className="muted small">
                  {p.spaceName || p.spaceKey} · #{p.id}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}

      {selected && <ExportForm key={selected.id} page={selected} />}
    </section>
  );
}

function ExportForm({ page }: { page: PageSummary }) {
  const prefs = usePreferences();
  const [format, setFormat] = useState<ExportFormat | null>(null);
  const [includeChildren, setIncludeChildren] = useState(true);
  const [classification, setClassification] = useState('');
  const create = useCreateExport();
  const navigate = useNavigate();
  const effectiveFormat = format ?? prefs.data?.defaultFormat ?? 'pdf';

  const onSubmit = (e: FormEvent) => {
    e.preventDefault();
    create.mutate(
      { pageId: page.id, format: effectiveFormat, includeChildren, classification },
      { onSuccess: () => navigate('/') },
    );
  };

  return (
    <form className="card" onSubmit={onSubmit} aria-label="Export options">
      <h2>{page.title}</h2>
      {page.webUrl && (
        <p className="small">
          <a href={page.webUrl} target="_blank" rel="noreferrer noopener">
            Open in Confluence ↗
          </a>
        </p>
      )}

      <fieldset>
        <legend>Format</legend>
        {(['pdf', 'docx'] as const).map((f) => (
          <label key={f} className="radio">
            <input
              type="radio"
              name="format"
              value={f}
              checked={effectiveFormat === f}
              onChange={() => setFormat(f)}
            />
            {f === 'pdf' ? 'PDF' : 'Word (.docx)'}
          </label>
        ))}
      </fieldset>

      <label className="checkbox">
        <input
          type="checkbox"
          checked={includeChildren}
          onChange={(e) => setIncludeChildren(e.target.checked)}
        />
        Include all child pages (the hierarchy is preserved)
      </label>

      {includeChildren && (
        <details open>
          <summary>Tree preview</summary>
          <PageTree root={page} />
          {prefs.data && <p className="muted small">Limit: {prefs.data.maxPages} pages per export.</p>}
        </details>
      )}

      {prefs.data && prefs.data.classifications.length > 0 && (
        <ClassificationField
          levels={prefs.data.classifications}
          fallback={prefs.data.defaultClassification}
          value={classification}
          onChange={setClassification}
        />
      )}
      <p className="muted small">
        Every page carries your name, the export date and its reference, so the document can be traced back to
        this export.
      </p>

      {create.isError && <p className="error">{errorMessage(create.error)}</p>}
      <button type="submit" className="button primary" disabled={create.isPending}>
        {create.isPending ? 'Submitting…' : 'Start export'}
      </button>
    </form>
  );
}

function ClassificationField({
  levels,
  fallback,
  value,
  onChange,
}: {
  levels: Classification[];
  fallback?: string;
  value: string;
  onChange: (v: string) => void;
}) {
  const chosen = levels.find((l) => l.label === (value || fallback));
  return (
    <div>
      <label className="label" htmlFor="classification">
        Classification
      </label>
      <select id="classification" className="input" value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="">{fallback ? `Default (${fallback})` : 'None'}</option>
        {levels.map((l) => (
          <option key={l.label} value={l.label}>
            {l.label}
            {l.watermark ? ' — watermarked' : ''}
          </option>
        ))}
      </select>
      {chosen?.watermark && (
        <p className="muted small">
          “{chosen.label.toUpperCase()}” will be printed diagonally across every page.
        </p>
      )}
    </div>
  );
}

function errorMessage(e: unknown): string {
  return e instanceof ApiError ? e.message : 'An unexpected error occurred.';
}
