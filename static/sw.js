/* On-Call service worker — browser notifications */
self.addEventListener('install', e => self.skipWaiting());
self.addEventListener('activate', e => e.waitUntil(self.clients.claim()));

self.addEventListener('notificationclick', event => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(clients => {
      for (const c of clients) {
        if ('focus' in c) return c.focus();
      }
      if (self.clients.openWindow) return self.clients.openWindow('/');
    })
  );
});

self.addEventListener('message', event => {
  const data = event.data || {};
  if (data.type === 'notify' && data.title) {
    self.registration.showNotification(data.title, {
      body: data.body || '',
      icon: data.icon || '/favicon.ico',
      tag: data.tag || 'oncall',
      renotify: !!data.renotify,
      data: data.url ? { url: data.url } : {}
    });
  }
});
