# ADR 0010 — Isolating the conversion from the network

- Status: accepted
- Date: 2026-10-08

## Context

Page content is written by any Confluence author, and LibreOffice, importing HTML, **fetches the resources
the document points at**: a test with a local HTTP server showed it requesting the stylesheet (`OPTIONS`,
`HEAD`, `GET`) and the images of a converted document, from the worker's network. An author could make a
worker request any URL it can reach — cloud metadata endpoints, internal services — and, for an image,
embed the answer in the export. The image inlining already turned Confluence images into `data:` URIs, but
backgrounds, `srcset`, media elements and `url()` in inline styles went through untouched. A profile-level
proxy setting in LibreOffice's configuration did **not** stop the requests.

## Decision

Three independent layers, so that one gap does not open the way:

1. **The renderer removes every reference that loads something.** Elements that only load (`video`,
   `audio`, `source`, `track`, `frame`, `applet`, `base`, `svg`, …) are dropped; loading attributes
   (`srcset`, `background`, `poster`, `data`, `xlink:href`, …) are removed; `src` survives on images only,
   as a `data:` URI; `href` survives on links only (not followed during conversion); inline style
   declarations with `url()`, `@import`, `image-set()`, `expression()` or a CSS escape are dropped.
2. **LibreOffice's HTTP traffic goes nowhere.** The conversion process runs with `http_proxy`,
   `https_proxy` and `all_proxy` set to the loopback discard port and `no_proxy` empty; the server's own
   proxy settings are not inherited. The same test shows no request leaving with this in place, and the
   conversion still succeeds.
3. **The pods can only reach what they use.** The Helm chart's NetworkPolicies (opt-in, enabled in the
   production example) deny all traffic except DNS and the listed destinations, and deny ingress to workers.

Around it, the image is checked: CI fails on any fixable high or critical vulnerability (Trivy, pinned to a
commit), the Dockerfile upgrades the base packages, and each release publishes an attested SPDX SBOM.
The pods already ran non-root, with a read-only root file system, no capabilities and the `RuntimeDefault`
seccomp profile.

## Consequences

- Images that are not Confluence attachments (hot-linked from another site) were already replaced by their
  alternative text; backgrounds, video posters and similar decorations are now dropped too.
- SVG images attached in Confluence are still inlined as `data:` URIs; an SVG referencing an external
  resource meets layer 2 and, in Kubernetes, layer 3.
- Running LibreOffice in its own network namespace would be stronger than layer 2 but needs user
  namespaces, which the default seccomp profile denies to containers; it is not pursued.
- The NetworkPolicies need the operator to list the destinations' address ranges; the policy is therefore
  off by default rather than shipped with guesses that would cut the application off.
- A Trivy finding with a fix available blocks merging until the base image or a package is updated.
