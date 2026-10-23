/*
 * Pilothouse service worker: offline shell, Web Push notifications, and
 * Allow/Deny notification actions signed with this device's key.
 */
'use strict';

importScripts('/js/keystore.js');

const CACHE = 'pilothouse-shell-v1';
const SHELL = [
  '/',
  '/css/app.css',
  '/js/keystore.js',
  '/js/app.js',
  '/js/api.js',
  '/js/card.js',
  '/js/dom.js',
  '/js/push.js',
  '/js/ui.js',
  '/js/views/inbox.js',
  '/js/views/pair.js',
  '/js/views/rows.js',
  '/js/views/session.js',
  '/js/views/sessions.js',
  '/js/views/settings.js',
  '/manifest.webmanifest',
  '/icons/icon.svg',
  '/icons/icon-192.png',
  '/icons/badge-96.png',
];

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

// Network first (the daemon is on loopback or the tailnet, so it's fast and
// always the freshest), falling back to the cached shell when offline. The
// API is never cached.
self.addEventListener('fetch', (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.pathname.startsWith('/api/')) return;
  e.respondWith((async () => {
    try {
      const res = await fetch(req);
      if (res.ok && SHELL.includes(url.pathname)) {
        const copy = res.clone();
        caches.open(CACHE).then((c) => c.put(req, copy)).catch(() => {});
      }
      return res;
    } catch (err) {
      const cache = await caches.open(CACHE);
      const hit = await cache.match(req, { ignoreSearch: true });
      if (hit) return hit;
      if (req.mode === 'navigate') {
        const shell = await cache.match('/');
        if (shell) return shell;
      }
      throw err;
    }
  })());
});

self.addEventListener('push', (e) => {
  let p = {};
  try { p = e.data ? e.data.json() : {}; } catch { p = { title: 'Pilothouse', body: e.data && e.data.text() }; }
  const opts = {
    body: p.body || '',
    tag: p.tag || 'pilothouse',
    renotify: true,
    icon: '/icons/icon-192.png',
    badge: '/icons/badge-96.png',
    timestamp: Date.now(),
    requireInteraction: p.kind === 'request',
    data: { url: p.url || '/', request_id: p.request_id || '', expires_at: p.expires_at || 0 },
  };
  if (p.actions && p.request_id) {
    opts.actions = [
      { action: 'allow', title: 'Allow' },
      { action: 'deny', title: 'Deny' },
    ];
  }
  e.waitUntil(self.registration.showNotification(p.title || 'Pilothouse', opts));
});

async function focusOrOpen(url) {
  const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
  for (const w of wins) {
    if (new URL(w.url).origin !== self.location.origin) continue;
    await w.focus();
    w.postMessage({ type: 'navigate', url });
    return;
  }
  await self.clients.openWindow(url);
}

async function tell(text, ok) {
  const wins = await self.clients.matchAll({ type: 'window' });
  for (const w of wins) w.postMessage({ type: 'decided', text, ok });
}

self.addEventListener('notificationclick', (e) => {
  const n = e.notification;
  const data = n.data || {};
  n.close();
  if ((e.action === 'allow' || e.action === 'deny') && data.request_id) {
    e.waitUntil((async () => {
      const res = await self.PilothouseKeys.decide(data.request_id, e.action, 'once', '');
      if (res.ok) {
        await tell(e.action === 'allow' ? 'Allowed' : 'Denied', true);
        return;
      }
      if (res.status === 409) {
        await self.registration.showNotification('Already answered', {
          body: 'Someone answered this request on another device.',
          tag: data.request_id, icon: '/icons/icon-192.png', badge: '/icons/badge-96.png',
        });
        return;
      }
      // Anything else (signed out, clock skew, offline): open the request.
      await focusOrOpen(data.url || '/');
    })());
    return;
  }
  e.waitUntil(focusOrOpen(data.url || '/'));
});
