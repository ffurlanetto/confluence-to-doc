// Load test: people browsing the application while exports keep arriving.
// See docs/operations/load-testing.md for the stack to run it against.
//
//   k6 run loadtest/exports.js
//   k6 run -e EXPORTS_PER_MINUTE=60 -e DURATION=15m -e BASE_URL=https://c2d.staging.example.com loadtest/exports.js
import http from 'k6/http';
import { check, fail, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const BASE = __ENV.BASE_URL || 'http://localhost:8080';
const PAGE_ID = __ENV.PAGE_ID || '900'; // the reference documents: 14 pages, tables, images
const SEARCH = __ENV.SEARCH || 'Rendering';
const PAT = __ENV.PAT || 'dev-pat';
const DURATION = __ENV.DURATION || '5m';
const EXPORTS_PER_MINUTE = Number(__ENV.EXPORTS_PER_MINUTE || 20);
const BROWSERS = Number(__ENV.BROWSERS || 20);

// From the request to the document being ready, queue wait included.
const exportDuration = new Trend('export_duration', true);
const exportFailed = new Rate('export_failed');
const exportRefused = new Counter('export_refused');

export const options = {
  scenarios: {
    browse: { executor: 'constant-vus', exec: 'browse', vus: BROWSERS, duration: DURATION },
    exports: {
      executor: 'constant-arrival-rate',
      exec: 'exportTree',
      rate: EXPORTS_PER_MINUTE,
      timeUnit: '1m',
      duration: DURATION,
      preAllocatedVUs: 20,
      maxVUs: 200,
    },
  },
  thresholds: {
    // The service level objectives (docs/operations/slo.md), with margin.
    'http_req_failed{scenario:browse}': ['rate<0.005'],
    'http_req_duration{scenario:browse}': ['p(95)<1000'],
    export_failed: ['rate<0.01'],
    export_duration: ['p(90)<300000'], // milliseconds: 5 minutes
  },
};

const write = { 'X-CSRF-Protection': '1', Origin: BASE, 'Content-Type': 'application/json' };

// setup signs in once (the fake identity provider approves at once) and
// shares the session with every virtual user. SESSION skips the sign-in, for
// a stack behind a real identity provider: copy the session cookie's value
// (named __Host-c2d_session over https, c2d_session otherwise).
export function setup() {
  let cookie = __ENV.SESSION_COOKIE || (BASE.startsWith('https:') ? '__Host-c2d_session' : 'c2d_session');
  let session = __ENV.SESSION;
  if (!session) {
    const res = http.get(`${BASE}/auth/login?return_to=/`);
    check(res, { 'signed in': (r) => r.status === 200 });
    const cookies = http.cookieJar().cookiesForURL(BASE);
    cookie = cookies['__Host-c2d_session'] ? '__Host-c2d_session' : 'c2d_session';
    session = (cookies[cookie] || [])[0];
    if (!session) fail('no session cookie after signing in');
  }
  const data = { cookie, session };
  const pat = http.put(`${BASE}/api/preferences/pat`, JSON.stringify({ token: PAT }), params(data, { headers: write }));
  if (pat.status !== 200) fail(`saving the token: ${pat.status} ${pat.body}`);
  return data;
}

function params(data, extra = {}) {
  return { cookies: { [data.cookie]: data.session }, ...extra };
}

export function browse(data) {
  const p = params(data);
  const res = http.batch([
    ['GET', `${BASE}/api/me`, null, { ...p, tags: { name: 'me' } }],
    ['GET', `${BASE}/api/preferences`, null, { ...p, tags: { name: 'preferences' } }],
    ['GET', `${BASE}/api/exports`, null, { ...p, tags: { name: 'exports' } }],
    ['GET', `${BASE}/api/notifications`, null, { ...p, tags: { name: 'notifications' } }],
  ]);
  check(res, { 'pages load': (all) => all.every((r) => r.status === 200) });
  sleep(1 + Math.random() * 2);
  const search = http.get(`${BASE}/api/confluence/pages?q=${encodeURIComponent(SEARCH)}`, {
    ...p,
    tags: { name: 'search' },
  });
  check(search, { 'search answers': (r) => r.status === 200 });
  sleep(2 + Math.random() * 3);
}

export function exportTree(data) {
  const format = Math.random() < 0.6 ? 'pdf' : 'docx';
  const started = Date.now();
  const res = http.post(
    `${BASE}/api/exports`,
    JSON.stringify({ pageId: PAGE_ID, format, includeChildren: true }),
    params(data, { headers: write, tags: { name: 'create export' } }),
  );
  if (res.status === 429) {
    // The per-user cap (EXPORT_MAX_ACTIVE_PER_USER) or the API rate limit:
    // raise them on the test stack, they are not what is measured.
    exportRefused.add(1);
    return;
  }
  if (!check(res, { 'export accepted': (r) => r.status === 202 })) {
    exportFailed.add(true);
    return;
  }
  const id = res.json('id');
  for (let i = 0; i < 300; i++) {
    sleep(2);
    const status = http.get(`${BASE}/api/exports/${id}`, params(data, { tags: { name: 'poll export' } }));
    const state = status.json('status');
    if (state === 'succeeded' || state === 'failed') {
      exportDuration.add(Date.now() - started, { format });
      exportFailed.add(state === 'failed');
      if (state === 'succeeded') {
        const file = http.get(`${BASE}/api/exports/${id}/download`, params(data, { tags: { name: 'download' } }));
        check(file, { 'document downloaded': (r) => r.status === 200 && r.body.length > 1000 });
      }
      return;
    }
  }
  exportFailed.add(true); // not finished within ten minutes
}
