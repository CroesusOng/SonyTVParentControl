/*
 * Caches the app shell so the offline escape code still works when the Pi is down —
 * which is exactly the moment you need it. The API is never cached: a stale policy or a
 * stale "online" badge would be worse than an honest failure.
 */
const CACHE = 'braviaguard-v2';
const SHELL = ['./', 'index.html', 'app.js', 'styles.css', 'icon.svg', 'manifest.webmanifest'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE)
      // `cache: 'reload'` for the same reason as in the fetch handler below: a plain
      // addAll consults the HTTP cache, so a fresh install can re-import the very copy
      // the version bump was meant to drop.
      .then((cache) => cache.addAll(SHELL.map((url) => new Request(url, { cache: 'reload' }))))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== 'GET') return;
  if (url.pathname.startsWith('/api/')) return;

  // Network first, so a republished build is picked up as soon as the Pi is reachable.
  //
  // `cache: 'reload'` because a plain fetch() consults the HTTP cache first, which makes
  // this handler network-second in practice. One response stored under a long max-age --
  // a CDN applying its default to .js is enough -- then pins the shell for hours, and the
  // put() below copies that stale body into this cache, where it outlives the HTTP entry
  // and is served on every later network failure. The origin sends no-store, so there is
  // nothing worth keeping on the way out anyway.
  event.respondWith(
    fetch(event.request, { cache: 'reload' })
      .then((response) => {
        if (response.ok) {
          const copy = response.clone();
          caches.open(CACHE).then((cache) => cache.put(event.request, copy));
        }
        return response;
      })
      .catch(() => caches.match(event.request).then((hit) => hit || caches.match('index.html'))),
  );
});
