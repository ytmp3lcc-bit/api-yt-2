const CACHE_NAME = 'ytmp3-cache-v2'; // Incremented cache version
const urlsToCache = [
  './',
  'index.html',
  'contact.html',
  'privacy-policy.html',
  'terms-of-service.html',
  'dmca.html',
  'blog.html',
  'blog-post.html'
  // 'index.css' is intentionally removed.
  // The build process changes its name, which would cause installation to fail.
  // The dynamic 'fetch' event listener will cache the correctly named CSS file anyway.
];

// Install event: open cache and add essential shell URLs to it
self.addEventListener('install', event => {
  self.skipWaiting();
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then(cache => {
        console.log('Opened cache and caching app shell');
        // Use addAll with a catch to prevent installation failure if one asset is missing.
        // This is a minimal set, dynamic caching will handle the rest.
        return cache.addAll(urlsToCache).catch(err => {
            console.warn('Could not cache all core files, but continuing install. Dynamic caching will handle the rest.', err);
        });
      })
  );
});

// Activate event: clean up old caches
self.addEventListener('activate', event => {
  const cacheWhitelist = [CACHE_NAME];
  event.waitUntil(
    caches.keys().then(cacheNames => {
      return Promise.all(
        cacheNames.map(cacheName => {
          if (cacheWhitelist.indexOf(cacheName) === -1) {
            console.log('Deleting old cache:', cacheName);
            return caches.delete(cacheName);
          }
        })
      );
    }).then(() => self.clients.claim())
  );
});

// Fetch event: implement a robust cache-then-network strategy
self.addEventListener('fetch', event => {
  const { request } = event;
  const url = new URL(request.url);

  // Ignore non-GET requests and API calls
  if (request.method !== 'GET' || url.pathname.endsWith('.php')) {
    return;
  }

  // For HTML pages, use a network-first strategy to ensure content is fresh
  if (request.headers.get('Accept').includes('text/html')) {
    event.respondWith(
      fetch(request)
        .then(response => {
          // If network is available, cache the fresh response and return it
          const responseToCache = response.clone();
          caches.open(CACHE_NAME).then(cache => {
            cache.put(request, responseToCache);
          });
          return response;
        })
        .catch(() => {
          // If network fails, try to serve from cache
          return caches.match(request);
        })
    );
    return;
  }

  // For other assets (JS, CSS, images), use a cache-first strategy for speed
  event.respondWith(
    caches.match(request)
      .then(cachedResponse => {
        // If we have a cached response, return it.
        if (cachedResponse) {
          return cachedResponse;
        }

        // Otherwise, fetch from the network, cache it, and then return it.
        return fetch(request).then(networkResponse => {
            // Check if we received a valid response
            if (networkResponse && networkResponse.status === 200) {
                const responseToCache = networkResponse.clone();
                caches.open(CACHE_NAME).then(cache => {
                    cache.put(request, responseToCache);
                });
            }
            return networkResponse;
        });
      })
  );
});