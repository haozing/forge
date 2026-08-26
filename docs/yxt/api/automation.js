(function () {
  const api = window.YXTApi;
  window.YXTAutomation = {
    list: workspaceId => api.request(`/api/frontend/workspaces/${workspaceId}/automation-jobs`),
    create: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/automation-jobs`, { method: 'POST', body }),
    update: (jobId, body) => api.request(`/api/frontend/automation-jobs/${jobId}`, { method: 'PATCH', body }),
    runs: jobId => api.request(`/api/frontend/automation-jobs/${jobId}/runs`),
    run: runId => api.request(`/api/frontend/task-runs/${runId}`),
    cancel: runId => api.request(`/api/frontend/task-runs/${runId}/cancel`, { method: 'POST' }),
    retry: runId => api.request(`/api/frontend/task-runs/${runId}/retry`, { method: 'POST' }),
  };
})();
