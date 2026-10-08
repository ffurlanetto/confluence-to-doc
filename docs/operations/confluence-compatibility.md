# Confluence Data Center compatibility

The target is **Confluence Data Center 9.x** (decision of the hardening plan). This page lists what the
application relies on, why it holds on 9.x, and how to check a given instance before going live.

## What the application calls

All calls are `GET`, authenticated with the user's personal access token (`Authorization: Bearer`), and
only to the configured `CONFLUENCE_BASE_URL` origin.

| Endpoint | Used for | Since |
| --- | --- | --- |
| `/rest/api/user/current` | Checking a token when it is saved | 5.x |
| `/rest/pat/latest/tokens/{id}` | Reading the token's expiry (optional: failures are ignored) | 7.9 |
| `/rest/api/content/{id}?expand=space,version,body.export_view` | A page and its rendered HTML | 5.x |
| `/rest/api/content/{id}/child/page?expand=…&start=&limit=` | Children, paginated | 5.x |
| `/rest/api/content/search?cql=…` | The page picker's search | 5.5 |
| `/rest/api/content?spaceKey=&title=` | Resolving links written as `/display/SPACE/Title` | 5.x |
| Image URLs of the page on the same origin (`/download/attachments/…`) | Images, inlined into the document | — |

## Why 9.x is fine

- **REST API v1 is the Data Center API.** The v2 API (`/wiki/api/v2/…`) exists only in Confluence Cloud;
  Data Center 9 keeps `/rest/api/…` as its supported REST API, which is what this application uses.
- **Personal access tokens** exist since 7.9 and are unchanged in 9.x. Administrators can limit their
  lifetime: the application reads the expiry and warns users 14 days before.
- **`body.export_view`** is the HTML Confluence itself renders for exports (macros expanded, links
  absolute or root-relative). The renderer sanitises it and handles the markup of the common macros —
  panels, status lozenges, code blocks, expand, tasks; see the *Confluence macros* reference page.
- **Rate limiting** (available since 7.x, often enabled on 9.x) answers `429` with `Retry-After`; the
  client honours it, and the application's own shared budget (`CONFLUENCE_RATE_LIMIT`, 10 requests/s by
  default) keeps it from reaching the limit in the first place.
- **No write, no admin API, no app (plugin) to install.** The application needs no change on the
  Confluence side; its traffic is that of the users whose tokens it uses.

## What to agree with the Confluence administrators

1. The **request budget**: `CONFLUENCE_RATE_LIMIT` for all instances together, and whether Confluence's
   rate limiting should exempt or allow-list the service's address.
2. **Token policy**: maximum lifetime of personal access tokens, and whether tokens are allowed at all for
   all users (Confluence can restrict their creation).
3. **Reverse proxy and network path**: the URL the service uses (an internal one avoids a public
   round-trip), its TLS certificate chain (the container must trust it), and the egress rule to open.

## Checking an instance

Run the read-only compatibility check with a real token and a page that has children and an image:

```sh
CONFLUENCE_LIVE_URL=https://confluence.example.com \
CONFLUENCE_LIVE_PAT=<a personal access token> \
CONFLUENCE_LIVE_PAGE=<page id> \
make confluence-check
```

It calls every endpoint above with the application's own client and prints what came back: the token's
owner and expiry, the page's HTML size, its children, a search, a title lookup and an attachment download.
Any failure names the endpoint. Then, with the application deployed against that instance:

1. export the same page with its children, in Word and PDF;
2. compare with the Confluence page: headings, tables, images, macros, links between pages;
3. note anything specific to your instance (third-party macros render as whatever HTML they produce in
   `export_view`, which may be little).

The application's own tests run against a fake Confluence that reproduces these endpoints
(`internal/confluence/fake`); they cannot prove compatibility with a real instance, which is what this check
is for. Record its result (Confluence version, date, outcome) in your release notes when Confluence is
upgraded.
