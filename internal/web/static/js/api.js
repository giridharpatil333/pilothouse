// Device API client and the live state store (snapshot + SSE).

export class ApiError extends Error {
  constructor(status, code, message) {
    super(message || code || 'Request failed');
    this.status = status;
    this.code = code;
  }
}

export async function api(method, path, body) {
  const opts = { method, credentials: 'same-origin', headers: {} };
  if (method !== 'GET' && method !== 'HEAD') opts.headers['X-Pilothouse'] = '1';
  if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new ApiError(0, 'network', 'Can’t reach your computer.');
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new ApiError(res.status, data.code, data.error);
  return data;
}

// ---------- Store ----------

const listeners = new Set();

export const store = {
  me: null,
  seq: 0,
  machine: '',
  sessions: new Map(),
  requests: new Map(),
  presence: null,
  conn: 'connecting', // live | connecting | offline
  loaded: false,
};

export function subscribe(fn) {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

let queued = false;
/** Coalesces change notifications into one per frame. */
export function changed(kind = 'data') {
  if (queued) return;
  queued = true;
  requestAnimationFrame(() => {
    queued = false;
    for (const fn of listeners) fn(kind);
  });
}

export function loadSnapshot(snap) {
  store.seq = snap.seq;
  store.machine = snap.machine;
  store.presence = snap.presence;
  store.sessions = new Map(snap.sessions.map((s) => [s.id, s]));
  store.requests = new Map(snap.requests.map((r) => [r.id, r]));
  store.loaded = true;
  changed();
}

function applyEvent(ev) {
  if (ev.seq <= store.seq) return;
  store.seq = ev.seq;
  const d = ev.data;
  switch (ev.type) {
    case 'session.started':
    case 'session.updated':
    case 'session.exited':
      if (d && d.id) store.sessions.set(d.id, d);
      break;
    case 'request.opened':
    case 'request.closed':
      if (d && d.id) store.requests.set(d.id, d);
      break;
    case 'presence.changed':
      if (d) store.presence = d;
      break;
    case 'device.changed':
      if (d && store.me && d.id === store.me.device.id) {
        if (d.revoked_at) { location.assign('/pair'); return; }
        store.me.device = d;
      }
      break;
  }
  for (const fn of eventHooks) fn(ev);
  changed();
}

const eventHooks = new Set();
/** Raw event listener (session detail refreshes its timeline). */
export function onEvent(fn) {
  eventHooks.add(fn);
  return () => eventHooks.delete(fn);
}

// ---------- Live connection ----------

let es = null;
let retryTimer = 0;
let backoff = 0;
let hiddenAt = 0;

function setConn(c) {
  if (store.conn !== c) {
    store.conn = c;
    changed('conn');
  }
}

export async function resync() {
  const snap = await api('GET', '/api/snapshot');
  loadSnapshot(snap);
}

export function connect() {
  clearTimeout(retryTimer);
  if (es) { es.close(); es = null; }
  setConn(store.conn === 'live' ? 'live' : 'connecting');
  const src = new EventSource('/api/events?after=' + store.seq);
  es = src;
  src.addEventListener('open', () => {
    backoff = 0;
    setConn('live');
  });
  src.addEventListener('event', (m) => {
    try { applyEvent(JSON.parse(m.data)); } catch (e) { console.warn('bad event', e); }
  });
  src.addEventListener('control', async (m) => {
    let c = {};
    try { c = JSON.parse(m.data); } catch { /* ignore */ }
    if (!c.resync_required) return;
    src.close();
    if (es === src) es = null;
    try {
      await resync();
      connect();
    } catch (e) {
      fail(e);
    }
  });
  src.addEventListener('error', () => {
    if (es !== src) return;
    src.close();
    es = null;
    fail();
  });
}

async function fail(err) {
  setConn(backoff >= 2 ? 'offline' : 'connecting');
  // A 401 means this device was revoked or signed out elsewhere.
  if (!err || err.status !== 401) {
    try { await api('GET', '/api/me'); } catch (e) { err = e; }
  }
  if (err && err.status === 401) { location.assign('/pair'); return; }
  const delay = Math.min(30000, 1000 * 2 ** backoff) * (0.75 + Math.random() * 0.5);
  backoff = Math.min(backoff + 1, 6);
  retryTimer = setTimeout(connect, delay);
}

// Phones freeze background pages; reconnect at once when shown again.
document.addEventListener('visibilitychange', () => {
  if (document.hidden) { hiddenAt = Date.now(); return; }
  if (!store.loaded) return;
  if (!es || store.conn !== 'live' || Date.now() - hiddenAt > 20000) {
    backoff = 0;
    connect();
  }
});
addEventListener('online', () => { if (store.loaded) { backoff = 0; connect(); } });
