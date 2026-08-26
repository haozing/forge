(function () {
  const api = window.YXTApi;
  window.YXTTransfers = {
    startImport: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/assets/imports`, { method: 'POST', body }),
    importJob: jobId => api.request(`/api/frontend/import-jobs/${jobId}`),
    startExport: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/assets/exports`, { method: 'POST', body }),
    exportJob: jobId => api.request(`/api/frontend/export-jobs/${jobId}`),
  };
})();
