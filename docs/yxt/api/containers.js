(function () {
  const api = window.YXTApi;
  window.YXTContainers = {
    tree: workspaceId => api.request(`/api/frontend/workspaces/${workspaceId}/containers/tree`),
    create: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/containers`, { method: 'POST', body }),
    get: containerId => api.request(`/api/frontend/containers/${containerId}`),
    update: (containerId, body) => api.request(`/api/frontend/containers/${containerId}`, { method: 'PATCH', body }),
    remove: containerId => api.request(`/api/frontend/containers/${containerId}`, { method: 'DELETE' }),
    assets: containerId => api.request(`/api/frontend/containers/${containerId}/assets`),
  };
})();
