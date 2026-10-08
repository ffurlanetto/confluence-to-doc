import { useRef, useState, type FormEvent } from 'react';

import { useDeleteTemplate, useTemplate, useUploadTemplate } from '../api/hooks';
import type { TemplateInfo } from '../api/types';
import { AdminNav } from '../components/AdminNav';
import { formatBytes, formatDate } from '../lib/format';

const origins: Record<TemplateInfo['origin'], string> = {
  uploaded: 'Uploaded from this console',
  configured: 'Configured on the server (WORD_TEMPLATE_PATH)',
  none: 'None: documents use the built-in styling',
};

export function AdminTemplatePage() {
  const template = useTemplate();
  const upload = useUploadTemplate();
  const remove = useDeleteTemplate();
  const [file, setFile] = useState<File>();
  const [tooLarge, setTooLarge] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const t = template.data;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!file) return;
    upload.mutate(file, {
      onSuccess: () => {
        setFile(undefined);
        if (input.current) input.current.value = '';
      },
    });
  };

  return (
    <section>
      <AdminNav />
      <div className="section-header">
        <h1>Word template</h1>
      </div>
      <p className="muted">
        The company template gives every document its header, footer, fonts and page setup, in Word and PDF
        alike. An uploaded template applies to the next exports on every server, and takes precedence over the
        one configured on the server.
      </p>

      {template.isLoading && <p>Loading…</p>}
      {template.isError && <p className="error">Unable to load the template.</p>}
      {t && (
        <div className="card">
          <h2>In use</h2>
          <dl className="facts">
            <dt>Source</dt>
            <dd>{origins[t.origin]}</dd>
            {t.name && (
              <>
                <dt>File</dt>
                <dd>{t.name}</dd>
                <dt>Body text style</dt>
                <dd>
                  {t.defaultParagraphStyle || '—'} ({t.styles} styles)
                </dd>
              </>
            )}
            {t.uploadedAt && (
              <>
                <dt>Uploaded</dt>
                <dd>
                  {formatDate(t.uploadedAt)}
                  {t.uploadedBy && ` by ${t.uploadedBy}`}
                </dd>
              </>
            )}
          </dl>
          {t.origin === 'uploaded' && (
            <div className="actions">
              {confirming ? (
                <>
                  <button
                    type="button"
                    className="button danger"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate(undefined, { onSuccess: () => setConfirming(false) })}
                  >
                    Confirm the removal
                  </button>
                  <button type="button" className="button" onClick={() => setConfirming(false)}>
                    Keep it
                  </button>
                </>
              ) : (
                <button type="button" className="button" onClick={() => setConfirming(true)}>
                  Remove the uploaded template
                </button>
              )}
              <p className="muted small">
                {t.configured
                  ? `Documents then use the server's template, ${t.configured}.`
                  : 'Documents then use the built-in styling.'}
              </p>
            </div>
          )}
          {remove.error && (
            <p className="error" role="alert">
              {remove.error.message}
            </p>
          )}
        </div>
      )}

      {t && (
        <form className="card" onSubmit={submit} aria-label="Upload a template">
          <h2>Upload a template</h2>
          <p className="muted small">
            A .dotx or .docx file of at most {formatBytes(t.maxBytes)}. Templates with macros, ActiveX
            controls or links to external content are refused. Try it on an export before announcing it.
          </p>
          <label className="label" htmlFor="template-file">
            Template file
          </label>
          <input
            id="template-file"
            ref={input}
            type="file"
            className="input"
            accept=".dotx,.docx,application/vnd.openxmlformats-officedocument.wordprocessingml.template,application/vnd.openxmlformats-officedocument.wordprocessingml.document"
            onChange={(e) => {
              const chosen = e.target.files?.[0];
              setFile(chosen);
              setTooLarge(!!chosen && chosen.size > t.maxBytes);
              upload.reset();
            }}
          />
          {tooLarge && (
            <p className="error" role="alert">
              This file is larger than {formatBytes(t.maxBytes)}.
            </p>
          )}
          {upload.error && (
            <p className="error" role="alert">
              {upload.error.message}
            </p>
          )}
          {upload.isSuccess && <p role="status">The template is in use for the next exports.</p>}
          <div className="actions">
            <button type="submit" className="button primary" disabled={!file || tooLarge || upload.isPending}>
              {upload.isPending ? 'Uploading…' : 'Upload'}
            </button>
          </div>
        </form>
      )}
    </section>
  );
}
