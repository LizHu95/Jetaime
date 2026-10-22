// Push only: authenticated pages and API responses are never cached offline.
self.addEventListener('install', event => event.waitUntil(self.skipWaiting()));
self.addEventListener('activate', event => event.waitUntil(self.clients.claim()));

function notificationPath(value) {
  const fallback = '/page/chat/';
  try {
    const url = new URL(value || fallback, self.location.origin);
    if (url.origin === self.location.origin && /^\/page\/chat\/(?:[0-9a-f-]{36}\/)?$/.test(url.pathname)) return url.href;
  } catch { /* A malformed target always opens the chat list. */ }
  return new URL(fallback, self.location.origin).href;
}

self.addEventListener('push', event => {
  let data = {};
  try { data = event.data?.json() || {}; } catch { /* Still show a visible notification. */ }
  event.waitUntil(self.registration.showNotification('Jetaime', {
    body: typeof data.body === 'string' ? data.body : '你收到一条新消息',
    icon: '/page/icon-192.png',
    tag: typeof data.tag === 'string' ? data.tag : undefined,
    data: { url: notificationPath(data.url) }
  }));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const target = notificationPath(event.notification.data?.url);
  event.waitUntil((async () => {
    const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
    for (const client of windows) {
      const url = new URL(client.url);
      if (url.origin === self.location.origin && url.pathname.startsWith('/page/')) {
        try { const navigated = await client.navigate(target); if (navigated) return await navigated.focus(); } catch { /* Fall back to a new window. */ }
      }
    }
    return self.clients.openWindow(target);
  })());
});
