(function () {
  const api = window.YXTApi;
  window.YXTQuery = {
    search: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/query`, { method: 'POST', body }),
  };
})();
