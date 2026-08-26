(function () {
  const api = window.YXTApi;
  window.YXTWorkspaces = {
    list: () => api.request('/api/frontend/workspaces'),
    get: id => api.request(`/api/frontend/workspaces/${id}`),
    agentApplications: id => api.request(`/api/frontend/workspaces/${id}/agent-applications`),
    counts: id => api.request(`/api/frontend/workspaces/${id}/counts`),
    settings: id => api.request(`/api/frontend/workspaces/${id}/settings`),
    updateSettings: (id, body, etag) => api.request(`/api/frontend/workspaces/${id}/settings`, { method: 'PATCH', body, etag }),
    preferences: () => api.request('/api/frontend/me/preferences'),
    updatePreferences: body => api.request('/api/frontend/me/preferences', { method: 'PATCH', body }),
    stats: id => api.request(`/api/frontend/workspaces/${id}/stats`),
    activity: id => api.request(`/api/frontend/workspaces/${id}/activity`),
  };
})();
