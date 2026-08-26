(function () {
  const api = window.YXTApi;
  window.YXTReviews = {
    list: (workspaceId, status = 'pending') => api.request(`/api/frontend/workspaces/${workspaceId}/reviews?status=${encodeURIComponent(status)}`),
    get: reviewId => api.request(`/api/frontend/reviews/${reviewId}`),
    approve: (reviewId, body) => api.request(`/api/frontend/reviews/${reviewId}/approve`, { method: 'POST', body }),
    reject: (reviewId, body) => api.request(`/api/frontend/reviews/${reviewId}/reject`, { method: 'POST', body }),
  };
})();
