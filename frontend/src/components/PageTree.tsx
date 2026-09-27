import { useState } from 'react';

import { useChildren } from '../api/hooks';
import type { PageSummary } from '../api/types';

/**
 * Read-only preview of the Confluence page hierarchy that will be exported.
 * Children are loaded lazily when a node is expanded, so browsing a huge
 * space stays cheap.
 */
export function PageTree({ root }: { root: PageSummary }) {
  return (
    <ul className="tree" role="tree" aria-label="Arborescence exportée">
      <TreeNode page={root} depth={0} initiallyOpen />
    </ul>
  );
}

function TreeNode({
  page,
  depth,
  initiallyOpen = false,
}: {
  page: PageSummary;
  depth: number;
  initiallyOpen?: boolean;
}) {
  const [open, setOpen] = useState(initiallyOpen);
  const children = useChildren(page.id, open);
  const leaf = children.isSuccess && children.data.length === 0;

  return (
    <li role="treeitem" aria-expanded={leaf ? undefined : open} aria-level={depth + 1}>
      <div className="tree-row">
        <button
          type="button"
          className="tree-toggle"
          onClick={() => setOpen((o) => !o)}
          disabled={leaf}
          aria-label={open ? `Replier ${page.title}` : `Déplier ${page.title}`}
        >
          {leaf ? '•' : open ? '▾' : '▸'}
        </button>
        <span>{page.title}</span>
      </div>
      {open && children.isLoading && <p className="tree-hint">Chargement…</p>}
      {open && children.isError && <p className="tree-hint error">Impossible de charger les sous-pages.</p>}
      {open && children.data && children.data.length > 0 && (
        <ul role="group">
          {children.data.map((c) => (
            <TreeNode key={c.id} page={c} depth={depth + 1} />
          ))}
        </ul>
      )}
    </li>
  );
}
