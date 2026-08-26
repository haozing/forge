(function () {
  const api = window.YXTApi;
  window.YXTConversations = {
    list: workspaceId => api.request(`/api/frontend/workspaces/${workspaceId}/conversations`),
    create: (workspaceId, body) => api.request(`/api/frontend/workspaces/${workspaceId}/conversations`, { method: 'POST', body }),
    get: conversationId => api.request(`/api/frontend/conversations/${conversationId}`),
    messages: conversationId => api.request(`/api/frontend/conversations/${conversationId}/messages`),
    send: (conversationId, body) => api.request(`/api/frontend/conversations/${conversationId}/messages`, { method: 'POST', body }),
    chat: (conversationId, body) => api.request(`/api/frontend/conversations/${conversationId}/chat`, { method: 'POST', body }),
  };
})();
