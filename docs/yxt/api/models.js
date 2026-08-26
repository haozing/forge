(function () {
  const api = window.YXTApi;
  window.YXTModels = {
    list: workspaceId => api.request(`/api/frontend/workspaces/${workspaceId}/resource-models`),
    get: modelId => api.request(`/api/frontend/resource-models/${modelId}`),
    versions: modelId => api.request(`/api/frontend/resource-models/${modelId}/versions`),
    create: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/resource-models`, { method: 'POST', body }),
    createVersion: (modelId, body) => api.request(`/api/frontend/resource-models/${modelId}/versions`, { method: 'POST', body }),
    validateVersion: versionId => api.request(`/api/frontend/resource-model-versions/${versionId}/validate`, { method: 'POST' }),
    publishVersion: versionId => api.request(`/api/frontend/resource-model-versions/${versionId}/publish`, { method: 'POST' }),
  };
})();
