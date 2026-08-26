(function () {
  const state = { sequence: 0 };

  function makeIdempotencyKey(prefix) {
    state.sequence += 1;
    return `${prefix}-${Date.now()}-${state.sequence}`;
  }

  async function request(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (options.body !== undefined && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
    if (options.idempotency !== false && options.method && options.method !== 'GET' && options.method !== 'HEAD') {
      headers.set('Idempotency-Key', options.idempotencyKey || makeIdempotencyKey('yxt'));
    }
    if (options.etag) headers.set('If-Match', options.etag);
    const response = await fetch(path, { ...options, headers, credentials: 'include', body: options.body === undefined ? undefined : JSON.stringify(options.body) });
    const text = await response.text();
    let payload = null;
    try { payload = text ? JSON.parse(text) : null; } catch { payload = text; }
    if (!response.ok) {
      const error = new Error(payload?.code || `request_failed_${response.status}`);
      error.status = response.status;
      error.payload = payload;
      throw error;
    }
    return { data: payload, etag: response.headers.get('ETag'), response };
  }

  window.YXTApi = { request, makeIdempotencyKey, state };
})();
