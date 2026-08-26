(function () {
  const api = window.YXTApi;
  window.YXTAssets = {
    list: (workspaceId, params = {}) => {
      const query = new URLSearchParams(Object.entries(params).filter(([, value]) => value !== undefined && value !== ''));
      return api.request(`/api/frontend/workspaces/${workspaceId}/assets${query.toString() ? `?${query}` : ''}`);
    },
    get: assetId => api.request(`/api/frontend/assets/${assetId}`),
    create: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/assets`, { method: 'POST', body }),
    update: (assetId, body, etag) => api.request(`/api/frontend/assets/${assetId}`, { method: 'PATCH', body, etag }),
    submitReview: (assetId, body) => api.request(`/api/frontend/assets/${assetId}/submit-review`, { method: 'POST', body }),
    publish: (assetId, body) => api.request(`/api/frontend/assets/${assetId}/publish`, { method: 'POST', body }),
    archive: assetId => api.request(`/api/frontend/assets/${assetId}/archive`, { method: 'POST' }),
  };
})();
