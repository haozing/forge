const icons = {
  search: 'M11 11a7 7 0 1 1-1.4-9.8A7 7 0 0 1 11 11Zm0 0 4 4',
  database: 'M4 5c0-1.1 3.6-2 8-2s8 .9 8 2-3.6 2-8 2-8-.9-8-2Zm0 0v7c0 1.1 3.6 2 8 2s8-.9 8-2V5m-16 7v7c0 1.1 3.6 2 8 2s8-.9 8-2v-7',
  'chevrons-up-down': 'm7 7 5-5 5 5M7 17l5 5 5-5',
  'panel-left': 'M9 4v16M4 4h16v16H4z',
  sparkles: 'm12 3-1.2 4.1L7 8.3l3.8 1.2L12 14l1.2-4.5L17 8.3l-3.8-1.2L12 3ZM5 14l-.7 2.3L2 17l2.3.7L5 20l.7-2.3L8 17l-2.3-.7L5 14Z',
  clapperboard: 'M4 5h16v14H4zM4 9h16M8 5l2 4M13 5l2 4M18 5l2 4',
  'circle-help': 'M12 21a9 9 0 1 1 9-9 9 9 0 0 1-9 9Zm0-5v.01M9.5 9a2.6 2.6 0 1 1 4.3 2c-1.1.8-1.8 1.1-1.8 2.4',
  'file-text': 'M6 3h8l4 4v14H6zM14 3v5h5M9 13h6M9 17h6M9 9h1',
  'notebook-pen': 'M4 3h12a2 2 0 0 1 2 2v16H6a2 2 0 0 1-2-2V3Zm4 0v18M9 8h6M9 12h5m-2 5 4-4 2 2-4 4-3 1 1-3Z',
  inbox: 'M4 4h16v16H4zM4 14h4l1.5 2h5L16 14h4',
  bot: 'M8 9h8v8H8zM12 5v4M9 13h.01M15 13h.01M5 12H3m18 0h-2M10 17v2h4v-2',
  timer: 'M12 8v4l2.5 2.5M9 3h6M12 3v2M5.6 7.3l1.4 1.4M18.4 7.3 17 8.7M20 13a8 8 0 1 1-16 0 8 8 0 0 1 16 0Z',
  'settings-2': 'M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7ZM19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1-1.8 1.8-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5V20h-2.6v-.1a1.7 1.7 0 0 0-1-1.5 1.7 1.7 0 0 0-1.9.3l-.1.1-1.8-1.8.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H6v-2.6h.1a1.7 1.7 0 0 0 1.5-1 1.7 1.7 0 0 0-.3-1.9l-.1-.1L9 6.6l.1.1a1.7 1.7 0 0 0 1.9.3 1.7 1.7 0 0 0 1-1.5V5h2.6v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1 1.8 1.8-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.5 1h.1v2.6h-.1a1.7 1.7 0 0 0-1.5 1Z',
  'more-horizontal': 'M5 12h.01M12 12h.01M19 12h.01',
  'chevron-right': 'm9 18 6-6-6-6',
  'chevron-left': 'm15 18-6-6 6-6',
  x: 'M6 6l12 12M18 6 6 18',
  bell: 'M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9M10 21h4',
  plus: 'M12 5v14M5 12h14',
  filter: 'm4 6h16M7 12h10m-7 6h4',
  'layout-list': 'M8 6h12M8 12h12M8 18h12M4 6h.01M4 12h.01M4 18h.01',
  grid: 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 14h6v6h-6z',
  upload: 'M12 16V4m0 0L7 9m5-5 5 5M5 20h14',
  download: 'M12 4v12m0 0 5-5m-5 5-5-5M5 20h14',
  'more-vertical': 'M12 5h.01M12 12h.01M12 19h.01',
  folder: 'M3 6h7l2 2h9v10H3z',
  'folder-open': 'M3 7h7l2 2h9l-2 10H3z',
  'file-plus-2': 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6M12 11v6M9 14h6',
  'arrow-up-right': 'M7 17 17 7M7 7h10v10',
  'arrow-up': 'm5 12 7-7 7 7M12 5v14',
  paperclip: 'm21.4 11.6-8.8 8.8a6 6 0 0 1-8.5-8.5l8.8-8.8a4 4 0 0 1 5.7 5.7l-8.8 8.8a2 2 0 1 1-2.8-2.8l8.1-8.1',
  mic: 'M12 2a3 3 0 0 0-3 3v7a3 3 0 0 0 6 0V5a3 3 0 0 0-3-3ZM5 11a7 7 0 0 0 14 0M12 18v4M8 22h8',
  quote: 'M10 11H5a4 4 0 1 1 4-4v4a4 4 0 0 1-4 4M22 11h-5a4 4 0 1 1 4-4v4a4 4 0 0 1-4 4',
  send: 'm22 2-7 20-4-9-9-4 20-7ZM22 2 11 13',
  'file-edit': 'M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8zM14 2v6h6m-9 6 5.5-5.5 2 2L13 16l-3 .9.9-2.9Z',
  check: 'm5 12 4 4L19 6',
  'square-pen': 'M12 20h9M16.5 3.5a2.1 2.1 0 0 1 3 3L8 18l-4 1 1-4Z',
  'panel-top': 'M4 4h16v16H4zM4 9h16',
  'clock-3': 'M12 6v6l4 2',
  'archive': 'M3 6h18M5 6v14h14V6M8 10h8M3 6l1-3h16l1 3',
  'wand-sparkles': 'm15 4 1 3 3 1-3 1-1 3-1-3-3-1 3-1 1-3M5 12l1 3 3 1-3 1-1 3-1-3-3-1 3-1-3M12 12l-6 6',
  'git-branch': 'M6 3v12a3 3 0 1 0 3 3v-3a3 3 0 0 1 3-3h3M18 6a3 3 0 1 0-3-3v3a3 3 0 0 0 3 3Z',
  'message-circle': 'M20 11.5a8 8 0 0 1-8 8 8.5 8.5 0 0 1-3.2-.6L4 20l1.1-3.8A8 8 0 1 1 20 11.5Z',
};

function icon(name) { const p = icons[name] || icons.sparkles; return `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="${p}"></path></svg>`; }
document.querySelectorAll('[data-icon]').forEach(el => { el.innerHTML = icon(el.dataset.icon); });

const content = document.getElementById('contentView');
const breadcrumbTitle = document.getElementById('breadcrumbTitle');
const toast = document.getElementById('toast');
let activeView = 'thoughts';
let currentDoc = null;
let chatMessages = [];
let activeThoughtTitle = '';
let activeThoughtSource = null;

const views = {
  thoughts: renderThoughts,
  shots: renderShots,
  faq: renderFaq,
  documents: renderDocuments,
  'my-documents': renderMyDocuments,
  'my-notes': renderMyNotes,
  review: renderReview,
  assistant: renderAssistant,
  tasks: renderTasks,
  settings: renderSettings,
};

function bindViewButtons() {
  document.querySelectorAll('[data-view]').forEach(btn => btn.addEventListener('click', () => switchView(btn.dataset.view)));
  document.querySelectorAll('[data-action="toast"]').forEach(btn => btn.addEventListener('click', () => showToast(btn.dataset.message || '操作已完成')));
}

// Live API mode: once the backend contract is available, the workbench reads
// workspace/model/asset/review data from the server and renders fields from
// the published model schema. The legacy visual functions above remain only as
// a loading shell while the first request is in flight.
(function enableLiveWorkbench() {
  const live = { workspace: null, model: null, agentApplicationId: null };
  window.YXTState = live;

  function statusLabel(status) {
    return { published: '已发布', internal: '内部', archived: '已归档', pending: '待审核', approved: '已通过', rejected: '已驳回', draft: '草稿', none: '未提交' }[status] || status || '';
  }

  function renderLiveError(error) {
    const code = error?.payload?.code || error?.message || 'request_failed';
    content.innerHTML = `<div class="page-wrap"><section class="panel empty-state"><span class="activity-icon blue">${icon('circle-help')}</span><h2>无法加载工作区数据</h2><p>${escapeHtml(code)}</p><button class="btn-primary" data-live-retry>重试</button></section></div>`;
    document.querySelector('[data-live-retry]')?.addEventListener('click', () => bootLiveWorkbench());
  }

  async function renderLiveAssets() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载资源...</p></section></div>';
    try {
      const result = await YXTAssets.list(live.workspace.id, { resource_model_id: live.model?.id });
      const items = result.data?.items || [];
      const columns = live.model?.current_version?.list_schema?.columns || ['title', 'summary', 'tags', 'publication_status', 'updated_at'];
      const headerMarkup = columns.map(column => `<th>${escapeHtml(typeof column === 'string' ? column : column.label || column.field || column.key || '')}</th>`).join('');
      const rowsMarkup = items.map(item => {
        const cells = columns.map(column => `<td>${YXTSchema.renderListValue(item, column)}</td>`).join('');
        return `<tr data-live-asset="${item.id}">${cells}</tr>`;
      }).join('');
      const heading = pageHeading('资源表', live.model?.name || '资源库', live.model?.description || '由 resource model schema 驱动的动态资产列表。', '<button class="btn-primary" data-live-new>+ 新建资产</button>');
      content.innerHTML = `<div class="page-wrap">${heading}<div class="toolbar"><label class="search-input">${icon('search')}<input data-live-search placeholder="搜索资源..." /></label><span class="toolbar-spacer"></span><button class="btn-secondary" data-live-refresh>刷新</button></div><section class="panel"><table class="data-table"><thead><tr>${headerMarkup}</tr></thead><tbody>${rowsMarkup}</tbody></table><div class="table-foot"><span>共 ${items.length} 条记录</span></div></section></div>`;
      document.querySelector('[data-live-refresh]')?.addEventListener('click', () => renderLiveAssets());
      document.querySelector('[data-live-new]')?.addEventListener('click', () => renderLiveAssetEditor());
      document.querySelectorAll('[data-live-asset]').forEach(row => row.addEventListener('click', () => renderLiveAssetEditor(row.dataset.liveAsset)));
    } catch (error) { renderLiveError(error); }
  }

  async function renderLiveAssetEditor(assetId) {
    let asset = null;
    try { if (assetId) asset = (await YXTAssets.get(assetId)).data; } catch (error) { renderLiveError(error); return; }
    const fields = YXTSchema.fieldDefinitions(live.model?.current_version?.field_schema || {});
    content.innerHTML = `<div class="page-wrap"><div class="page-heading"><div><div class="eyebrow">动态表单</div><h1>${asset ? '编辑资产' : '新建资产'}</h1><p>${escapeHtml(live.model?.name || '')}</p></div><div class="head-actions"><button class="btn-secondary" data-live-back>取消</button><button class="btn-primary" data-live-save>${icon('check')} 保存</button></div></div><section class="panel extract-modal-body"><div class="extract-fields"><label class="extract-field"><span>标题</span><input data-live-title value="${escapeHtml(asset?.title || '')}" /></label>${fields.map(field => YXTSchema.renderFormField(field, asset?.fields?.[field.key])).join('')}<label class="extract-field"><span>Markdown</span><textarea data-live-markdown>${escapeHtml(asset?.markdown || '')}</textarea></label></div></section></div>`;
    document.querySelector('[data-live-back]')?.addEventListener('click', () => renderLiveAssets());
    document.querySelector('[data-live-save]')?.addEventListener('click', async () => {
      const body = { title: document.querySelector('[data-live-title]').value, markdown: document.querySelector('[data-live-markdown]').value, fields: {}, visibility: asset?.visibility || 'workspace' };
      document.querySelectorAll('[data-schema-field]').forEach(input => { body.fields[input.dataset.schemaField] = input.type === 'checkbox' ? input.checked : input.type === 'number' ? Number(input.value) : input.value; });
      try { if (asset) await YXTAssets.update(asset.id, body, asset.etag || asset.current_working_version_id); else await YXTAssets.create(live.workspace.id, { resource_model_id: live.model.id, ...body }); showToast('已保存'); await renderLiveAssets(); } catch (error) { renderLiveError(error); }
    });
  }

  async function renderLiveReviews() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载审核队列...</p></section></div>';
    try {
      const result = await YXTReviews.list(live.workspace.id);
      const items = result.data?.items || [];
      const reviewRows = items.map(item => `<tr><td>${escapeHtml(item.title || item.asset_id)}</td><td>${escapeHtml(item.resource_model_name)}</td><td>${escapeHtml(item.quality)}</td><td>${escapeHtml(item.submitted_by?.display_name || '')}</td><td><span class="chip amber">${statusLabel(item.status)}</span></td><td><button class="btn-secondary" data-review-approve="${item.review_id}">通过</button><button class="btn-secondary" data-review-reject="${item.review_id}">驳回</button></td></tr>`).join('');
      content.innerHTML = `<div class="page-wrap">${pageHeading('审核流', '待我审核', '审核资产版本后才能进入发布指针。')}<section class="panel"><table class="data-table"><thead><tr><th>资产</th><th>模型</th><th>质量</th><th>提交人</th><th>状态</th><th></th></tr></thead><tbody>${reviewRows}</tbody></table></section></div>`;
      document.querySelectorAll('[data-review-approve]').forEach(button => button.addEventListener('click', async () => { try { await YXTReviews.approve(button.dataset.reviewApprove, { expected_version_id: items.find(item => item.review_id === button.dataset.reviewApprove)?.asset_version_id }); await renderLiveReviews(); } catch (error) { renderLiveError(error); } }));
      document.querySelectorAll('[data-review-reject]').forEach(button => button.addEventListener('click', async () => { try { await YXTReviews.reject(button.dataset.reviewReject, { comment: '需要补充来源', expected_version_id: items.find(item => item.review_id === button.dataset.reviewReject)?.asset_version_id }); await renderLiveReviews(); } catch (error) { renderLiveError(error); } }));
    } catch (error) { renderLiveError(error); }
  }

  function flattenContainers(items, depth = 0, result = []) {
    (items || []).forEach(item => {
      result.push({ item, depth });
      flattenContainers(item.children, depth + 1, result);
    });
    return result;
  }

  async function renderLiveDocuments() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载文档目录...</p></section></div>';
    try {
      const result = await YXTContainers.tree(live.workspace.id);
      const items = flattenContainers(result.data?.items || []);
      const rows = items.map(({ item, depth }) => `<button class="doc-item" style="padding-left:${16 + depth * 24}px" data-live-container="${item.id}"><span class="doc-icon">${icon(item.kind === 'document' ? 'file-text' : 'folder')}</span><span class="doc-item-copy"><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.kind)} · ${escapeHtml(item.visibility)}</small></span><span class="chip ${item.status === 'active' ? 'green' : 'gray'}">${statusLabel(item.status)}</span></button>`).join('');
      content.innerHTML = `<div class="page-wrap">${pageHeading('文档库', '团队文档', '目录和文档容器由 workspace 数据驱动。', '<button class="btn-secondary" data-live-refresh>刷新</button>')}<section class="panel doc-list-panel"><div class="panel-head"><div><h2>目录树</h2><small>${items.length} 个容器</small></div></div><div class="doc-list">${rows || '<div class="doc-empty">当前 workspace 还没有文档容器</div>'}</div></section></div>`;
      document.querySelector('[data-live-refresh]')?.addEventListener('click', renderLiveDocuments);
      document.querySelectorAll('[data-live-container]').forEach(button => button.addEventListener('click', async () => {
        try {
          const assets = await YXTContainers.assets(button.dataset.liveContainer);
          showToast(`容器内 ${assets.data?.items?.length || 0} 条资产`);
        } catch (error) { renderLiveError(error); }
      }));
    } catch (error) { renderLiveError(error); }
  }

  async function renderLiveThoughts() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载对话...</p></section></div>';
    try {
      const result = await YXTConversations.list(live.workspace.id);
      const items = result.data?.items || [];
      const rows = items.map(item => `<button class="activity-row" data-live-conversation="${item.conversation_id || item.id}"><span class="activity-icon blue">${icon('message-circle')}</span><span><strong>${escapeHtml(item.title || '未命名对话')}</strong><small>${escapeHtml(item.last_message_preview || item.source || '暂无消息')} · ${escapeHtml(item.status || '')}</small></span><span class="doc-time">${item.message_count || 0} 条消息</span></button>`).join('');
      content.innerHTML = `<div class="page-wrap">${pageHeading('AI 思考空间', '新思考', '对话、消息和上下文来自当前 workspace。', '<button class="btn-secondary" data-live-refresh>刷新</button><button class="btn-primary" data-live-new-conversation>新建对话</button>')}<section class="panel activity-panel"><div class="panel-head"><div><h2>最近对话</h2><small>${items.length} 个会话</small></div></div>${rows || '<div class="empty-state"><p>还没有对话记录</p></div>'}</section></div>`;
      document.querySelector('[data-live-refresh]')?.addEventListener('click', renderLiveThoughts);
      document.querySelector('[data-live-new-conversation]')?.addEventListener('click', async () => {
        if (!live.agentApplicationId) { showToast('当前 workspace 没有可用 Agent 应用'); return; }
        try {
          const created = await YXTConversations.create(live.workspace.id, { agent_application_id: live.agentApplicationId, title: '新思考', source: 'chat_interface', visibility: 'workspace' });
          await renderLiveConversation(created.data?.conversation_id);
        } catch (error) { renderLiveError(error); }
      });
      document.querySelectorAll('[data-live-conversation]').forEach(button => button.addEventListener('click', () => renderLiveConversation(button.dataset.liveConversation)));
    } catch (error) { renderLiveError(error); }
  }

  async function renderLiveConversation(conversationId) {
    if (!conversationId) return renderLiveThoughts();
    try {
      const [conversation, messages] = await Promise.all([YXTConversations.get(conversationId), YXTConversations.messages(conversationId)]);
      const item = conversation.data || {};
      const messageItems = messages.data?.items || [];
      content.innerHTML = `<div class="page-wrap">${pageHeading('AI 思考空间', escapeHtml(item.title || '新思考'), '消息先落库，再进入 Agent 处理。', '<button class="btn-secondary" data-live-back-thoughts>返回列表</button>')}<section class="panel"><div class="chat-stream" data-live-message-stream>${messageItems.map(message => `<div class="message ${message.role === 'human' || message.role === 'user' ? 'user' : ''}"><div class="message-avatar">${message.role === 'human' || message.role === 'user' ? '我' : icon('sparkles')}</div><div class="message-bubble">${escapeHtml(message.content || '')}</div></div>`).join('') || '<div class="doc-empty">还没有消息</div>'}</div><form class="chat-composer" data-live-chat-form><textarea data-live-chat-input rows="2" placeholder="继续输入..." required></textarea><button class="btn-primary" type="submit">发送</button></form></section></div>`;
      document.querySelector('[data-live-back-thoughts]')?.addEventListener('click', renderLiveThoughts);
      document.querySelector('[data-live-chat-form]')?.addEventListener('submit', async event => {
        event.preventDefault();
        const input = document.querySelector('[data-live-chat-input]');
        const query = input.value.trim();
        if (!query) return;
        input.disabled = true;
        try { await YXTConversations.chat(conversationId, { query }); await renderLiveConversation(conversationId); } catch (error) { renderLiveError(error); } finally { input.disabled = false; }
      });
    } catch (error) { renderLiveError(error); }
  }

  async function renderLiveAssistant() {
    content.innerHTML = `<div class="page-wrap">${pageHeading('成员查询', '镜头问答助手', '在当前 workspace 内检索有权限访问的资产。')}<section class="panel"><form data-live-query-form class="toolbar"><label class="search-input">${icon('search')}<input data-live-query placeholder="输入镜头、情绪或字段关键词" required /></label><button class="btn-primary" type="submit">搜索</button></form><div data-live-query-results class="doc-list"><div class="doc-empty">输入关键词开始搜索</div></div></section></div>`;
    document.querySelector('[data-live-query-form]')?.addEventListener('submit', async event => {
      event.preventDefault();
      const query = document.querySelector('[data-live-query]').value.trim();
      if (!query) return;
      const resultBox = document.querySelector('[data-live-query-results]');
      resultBox.innerHTML = '<div class="doc-empty">正在搜索...</div>';
      try {
        const result = await YXTQuery.search(live.workspace.id, { mode: 'hybrid', query, top_k: 20 });
        const items = result.data?.items || [];
        resultBox.innerHTML = items.length ? items.map(item => `<div class="doc-item"><span class="doc-icon">${icon('clapperboard')}</span><span class="doc-item-copy"><strong>${escapeHtml(item.title || item.asset_id)}</strong><small>${escapeHtml(item.snippet || item.summary || '')}</small></span><span class="chip blue">${item.score?.toFixed ? item.score.toFixed(2) : item.score || ''}</span></div>`).join('') : '<div class="doc-empty">没有找到匹配资产</div>';
      } catch (error) { renderLiveError(error); }
    });
  }

  async function renderLiveTasks() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载整理任务...</p></section></div>';
    try {
      const result = await YXTAutomation.list(live.workspace.id);
      const items = result.data?.items || [];
      const rows = items.map(item => `<div class="activity-row"><span class="activity-icon ${item.enabled ? 'green' : 'gray'}">${icon('timer')}</span><span><strong>${escapeHtml(item.name)}</strong><small>${escapeHtml(item.operation)} · ${escapeHtml(item.concurrency_policy)} · ${item.enabled ? '运行中' : '已暂停'}</small></span><button class="btn-secondary" data-live-runs="${item.id}">运行记录</button></div>`).join('');
      content.innerHTML = `<div class="page-wrap">${pageHeading('自动化', '整理任务', '任务配置和运行状态来自 automation job。', '<button class="btn-secondary" data-live-refresh>刷新</button>')}<section class="panel activity-panel"><div class="panel-head"><div><h2>任务配置</h2><small>${items.length} 个任务</small></div></div>${rows || '<div class="empty-state"><p>当前 workspace 还没有自动化任务</p></div>'}<div data-live-runs-panel></div></section></div>`;
      document.querySelector('[data-live-refresh]')?.addEventListener('click', renderLiveTasks);
      document.querySelectorAll('[data-live-runs]').forEach(button => button.addEventListener('click', async () => {
        try {
          const runs = await YXTAutomation.runs(button.dataset.liveRuns);
          const runItems = runs.data?.items || [];
          document.querySelector('[data-live-runs-panel]').innerHTML = `<div class="section-label">运行记录</div>${runItems.map(run => `<div class="activity-row"><span class="activity-icon blue">${icon('clock-3')}</span><span><strong>${escapeHtml(run.operation)}</strong><small>${escapeHtml(run.source)} · ${escapeHtml(run.status)}</small></span><span class="chip gray">${Math.round((run.progress || 0) * 100)}%</span></div>`).join('') || '<div class="doc-empty">暂无运行记录</div>'}`;
        } catch (error) { renderLiveError(error); }
      }));
    } catch (error) { renderLiveError(error); }
  }

  async function renderLiveSettings() {
    content.innerHTML = '<div class="page-wrap"><section class="panel empty-state"><p>正在加载设置...</p></section></div>';
    try {
      const [settings, preferences, stats] = await Promise.all([
        YXTWorkspaces.settings(live.workspace.id),
        YXTWorkspaces.preferences(),
        YXTWorkspaces.stats(live.workspace.id),
      ]);
      const workspace = settings.data || {};
      const member = preferences.data || {};
      const summary = stats.data || {};
      content.innerHTML = `<div class="page-wrap">${pageHeading('工作区', '设置', '设置、偏好和统计均绑定当前 workspace。', '<button class="btn-primary" data-live-save-settings>保存</button>')}<div class="settings-grid"><section class="panel settings-section"><div class="panel-head"><h2>工作区默认值</h2></div><label class="extract-field"><span>工作区名称</span><input data-live-setting="name" value="${escapeHtml(workspace.name || live.workspace.name || '')}" /></label><label class="extract-field"><span>默认可见性</span><select data-live-setting="default_visibility"><option value="private" ${workspace.default_visibility === 'private' ? 'selected' : ''}>private</option><option value="workspace" ${workspace.default_visibility === 'workspace' ? 'selected' : ''}>workspace</option><option value="internal" ${workspace.default_visibility === 'internal' ? 'selected' : ''}>internal</option></select></label></section><section class="panel settings-section"><div class="panel-head"><h2>我的偏好</h2></div><label class="extract-field"><span>自动保存草稿</span><input type="checkbox" data-live-preference="auto_save_draft" ${member.auto_save_draft !== false ? 'checked' : ''} /></label><label class="extract-field"><span>回答显示引用</span><input type="checkbox" data-live-preference="show_answer_references" ${member.show_answer_references !== false ? 'checked' : ''} /></label></section></div><section class="panel"><div class="panel-head"><h2>当前统计</h2></div><div class="stats-grid">${statCard('资产总数', summary.assets_total ?? 0, 'workspace 内', '', 'database')} ${statCard('已发布', summary.assets_published ?? 0, '发布指针', '', 'check')} ${statCard('待审核', summary.assets_pending_review ?? 0, '审核队列', '', 'clock-3')}</div></section></div>`;
      document.querySelector('[data-live-save-settings]')?.addEventListener('click', async () => {
        const body = { name: document.querySelector('[data-live-setting="name"]').value, default_visibility: document.querySelector('[data-live-setting="default_visibility"]').value };
        const preferencesBody = {};
        document.querySelectorAll('[data-live-preference]').forEach(input => { preferencesBody[input.dataset.livePreference] = input.checked; });
        try {
          await YXTWorkspaces.updateSettings(live.workspace.id, body, settings.etag || '*');
          await YXTWorkspaces.updatePreferences(preferencesBody);
          showToast('设置已保存');
        } catch (error) { renderLiveError(error); }
      });
    } catch (error) { renderLiveError(error); }
  }

  async function bootLiveWorkbench() {
    try {
      const workspaces = await YXTWorkspaces.list();
      live.workspace = workspaces.data?.items?.[0];
      if (!live.workspace) throw new Error('workspace_not_found');
      document.querySelector('.workspace-copy strong').textContent = live.workspace.name;
      document.querySelector('.workspace-copy small').textContent = live.workspace.description || '';
      const models = await YXTModels.list(live.workspace.id);
      live.model = models.data?.items?.find(item => item.status !== 'archived') || null;
      try {
        const applications = await YXTWorkspaces.agentApplications(live.workspace.id);
        live.agentApplicationId = applications.data?.items?.find(item => item.status === 'active' || item.enabled !== false)?.id || applications.data?.items?.[0]?.id || null;
      } catch (_) {
        live.agentApplicationId = null;
      }
      views.thoughts = renderLiveThoughts;
      views.shots = renderLiveAssets;
      views.documents = renderLiveDocuments;
      views.faq = renderLiveDocuments;
      views.review = renderLiveReviews;
      views.assistant = renderLiveAssistant;
      views.tasks = renderLiveTasks;
      views.settings = renderLiveSettings;
      if (activeView === 'shots' || activeView === 'review') views[activeView]();
      else switchView('shots');
    } catch (error) {
      if (error.status === 401) {
        content.innerHTML = `<div class="page-wrap"><section class="panel empty-state"><h2>请先登录</h2><p>工作区数据需要成员 session。</p><button class="btn-primary" data-live-login>登录</button></section></div>`;
        document.querySelector('[data-live-login]')?.addEventListener('click', () => showToast('请使用登录接口创建 agent_session'));
        return;
      }
      renderLiveError(error);
    }
  }

  window.addEventListener('load', bootLiveWorkbench);
})();
function bindNavigationGroups() {
  document.querySelectorAll('.nav-group-head').forEach(head => head.addEventListener('click', () => {
    const group = head.closest('.nav-group');
    const opening = !group.classList.contains('is-open');
    document.querySelectorAll('.nav-group.has-children').forEach(other => {
      other.classList.remove('is-open');
      other.querySelector('.nav-group-head')?.setAttribute('aria-expanded', 'false');
    });
    if (opening) {
      group.classList.add('is-open');
      head.setAttribute('aria-expanded', 'true');
    }
  }));
  document.querySelectorAll('[data-folder-toggle]').forEach(toggle => toggle.addEventListener('click', event => {
    if (!event.target.closest('.folder-chevron')) return;
    const key = toggle.dataset.folderToggle;
    const children = document.querySelector(`[data-folder-children="${key}"]`);
    const opening = !children.classList.contains('is-open');
    children.classList.toggle('is-open', opening);
    toggle.classList.toggle('is-expanded', opening);
  }));
  document.querySelectorAll('.folder-toggle').forEach(toggle => toggle.addEventListener('click', event => {
    if (event.target.closest('.folder-chevron')) return;
    openDocumentNode(toggle.dataset.docNode);
  }));
  document.querySelectorAll('[data-doc-node]:not(.folder-toggle)').forEach(item => item.addEventListener('click', () => openDocumentNode(item.dataset.docNode)));
  document.querySelectorAll('.pending-chat').forEach(item => item.addEventListener('click', () => {
    activeThoughtTitle = item.dataset.thoughtTitle;
    activeThoughtSource = null;
    document.querySelectorAll('.pending-chat').forEach(other => other.classList.toggle('active', other === item));
    switchView('thoughts', { preserveThought: true });
  }));
}
function openDocumentNode(title) {
  const children = { '团队规范': ['录入与审核', '智能应用'] }[title] || [];
  openEditor(title, { children });
}
function syncNavigationSelection() {
  const mainThoughtActive = activeView === 'thoughts' && !activeThoughtTitle;
  document.querySelectorAll('.tree-item[data-view]').forEach(btn => btn.classList.toggle('active', btn.dataset.view === activeView && (activeView !== 'thoughts' || mainThoughtActive) && !(activeView === 'documents' && currentDoc)));
  document.querySelectorAll('[data-doc-node]').forEach(btn => btn.classList.toggle('active', activeView === 'documents' && Boolean(currentDoc) && btn.dataset.docNode === currentDoc));
  document.querySelectorAll('.pending-chat').forEach(btn => btn.classList.toggle('active', activeView === 'thoughts' && Boolean(activeThoughtTitle) && btn.dataset.thoughtTitle === activeThoughtTitle));
}
function switchView(view, options = {}) {
  if (view !== 'thoughts' || !options.preserveThought) { activeThoughtTitle = ''; activeThoughtSource = null; }
  activeView = view; currentDoc = null;
  syncNavigationSelection();
  const names = { thoughts: '新思考', shots: '经典镜头库', faq: 'FAQ 知识库', documents: '文档库', 'my-documents': '我的', 'my-notes': '我的笔记', review: '待我审核', assistant: '镜头问答助手', tasks: '整理任务', settings: '设置' };
  breadcrumbTitle.textContent = view === 'thoughts' && activeThoughtTitle ? activeThoughtTitle : (names[view] || '工作台');
  views[view]?.(); bindViewButtons();
}
function showToast(message) { toast.textContent = message; toast.classList.add('show'); clearTimeout(showToast.timer); showToast.timer = setTimeout(() => toast.classList.remove('show'), 2200); }
function pageHeading(eyebrow, title, desc, actions='') { return `<div class="page-heading"><div><div class="eyebrow">${eyebrow}</div><h1>${title}</h1><p>${desc}</p></div>${actions ? `<div class="head-actions">${actions}</div>` : ''}</div>`; }
function statCard(label, value, hint, trend, iconName, down=false) { return `<div class="stat-card"><div class="stat-top"><span>${label}</span><span class="stat-icon">${icon(iconName)}</span></div><strong>${value}</strong><small>${hint} <span class="stat-trend ${down ? 'down':''}">${trend}</span></small></div>`; }

function renderThoughtsLegacy() {
  const thoughtTitle = activeThoughtTitle || '如何把“经典镜头”沉淀成团队可复用的资产？';
  const thoughtIntro = activeThoughtTitle ? '这是从主干对话中派生出的新思考，可继续在这里深聊。' : '我在整理最近收集的镜头资料，发现很多内容只有零散描述。想请你帮我梳理一套既方便录入、又能被团队搜索和复用的结构。';
  content.innerHTML = `<div class="page-wrap">
    ${pageHeading('AI 思考空间','新思考','把零散灵感沉淀为可检索、可协作的数据资产。','<button class="btn-primary" data-action="new-chat">'+icon('plus')+' 开始新对话</button>')}
    <div class="thought-layout">
      <section class="panel thought-card"><div class="thought-meta"><span class="tag">灵感整理</span><span>今天 09:42</span><span>·</span><span>林默</span><button class="icon-btn" style="margin-left:auto" title="更多" aria-label="更多">${icon('more-horizontal')}</button></div>
        <h2>${thoughtTitle}</h2>
        <p>${thoughtIntro}</p>
        <blockquote><strong>建议先从最小可用模型开始：</strong><br/>镜头名称、景别、机位、运动方式、情绪氛围、参考片段，以及一段 Markdown 形式的使用说明。</blockquote>
        <p>${activeThoughtTitle ? '这个派生思考保留了主干对话的引用摘要。你可以继续补充观点，聊到成熟后再派生为文档或正式资产。' : '后续再通过 Agent 自动抽取标签、补全描述，并将原始笔记与结构化镜头记录关联起来。这样既保留创作语境，也能让数据真正进入工作流。'}</p>
        <div class="thought-footer"><span><span>已生成 3 条建议</span><button data-action="toast" data-message="已复制思考内容">${icon('square-pen')} 复制</button></span><button data-action="toast" data-message="已收藏">${icon('archive')} 收藏</button></div>
      </section>
      <section class="panel quick-panel"><div class="panel-head"><h2>继续探索</h2><small>最近使用</small></div>
        <button class="quick-link" data-view="shots"><span class="quick-icon">${icon('clapperboard')}</span><div><strong>经典镜头库</strong><small>128 条已发布资产</small></div>${icon('arrow-up-right')}</button>
        <button class="quick-link" data-view="documents"><span class="quick-icon">${icon('file-text')}</span><div><strong>文档库</strong><small>24 篇团队文档</small></div>${icon('arrow-up-right')}</button>
        <button class="quick-link" data-view="assistant"><span class="quick-icon">${icon('bot')}</span><div><strong>镜头问答助手</strong><small>随时获取灵感</small></div>${icon('arrow-up-right')}</button>
      </section>
    </div>
    <div class="section-label">最近活动</div><div class="panel activity-panel"><div class="activity-row"><span class="activity-icon blue">${icon('file-edit')}</span><div><strong>《夜景情绪光线笔记》</strong><small>已保存到文档库 · 2 分钟前</small></div><span class="chip blue">草稿</span></div><div class="activity-row"><span class="activity-icon green">${icon('check')}</span><div><strong>《低机位运动镜头》</strong><small>Agent 整理完成 · 1 小时前</small></div><span class="chip green">待审核</span></div></div>
  </div>`;
  document.querySelector('[data-action="new-chat"]').addEventListener('click', () => switchView('assistant'));
}

function tableToolbar(extra='') { return `<div class="toolbar"><label class="search-input">${icon('search')}<input placeholder="搜索资源名称、标签..." /></label><select class="filter-select"><option>全部状态</option><option>已发布</option><option>待审核</option><option>草稿</option></select><button class="btn-secondary" data-action="toast" data-message="筛选条件已应用">${icon('filter')} 筛选</button><span class="toolbar-spacer"></span>${extra}<div class="view-toggle"><button class="active" title="列表视图">${icon('layout-list')}</button><button title="网格视图">${icon('grid')}</button></div></div>`; }
function renderShots() {
  const rows = [['雨夜街头的回望','远景 · 低机位 · 缓慢推进','夜景, 情绪','已发布','2026-08-22'],['电梯门打开后的停顿','中景 · 平视 · 固定','悬疑, 留白','已发布','2026-08-21'],['手持跟拍穿过人群','全景 · 肩扛 · 跟随','纪实, 动态','待审核','2026-08-20'],['窗边人物的侧脸光','近景 · 侧面 · 固定','自然光, 人物','已发布','2026-08-18'],['天台风中的双人对话','中远景 · 平视 · 横移','关系, 氛围','草稿','2026-08-16']];
  content.innerHTML = `<div class="page-wrap">${pageHeading('资源表','经典镜头库','按结构化字段管理镜头资产，支持检索、关联与 Agent 问答。','<button class="btn-secondary" data-action="toast" data-message="导出任务已创建">'+icon('download')+' 导出</button><button class="btn-primary" data-action="toast" data-message="已打开新建镜头表单">'+icon('plus')+' 新建镜头</button>')}${statsRow()}${tableToolbar('<button class="btn-secondary" data-action="toast" data-message="导入模板已下载">'+icon('upload')+' 导入</button>')}<section class="panel"><table class="data-table"><thead><tr><th style="width:34%">镜头名称</th><th>结构描述</th><th>标签</th><th>状态</th><th>更新时间</th><th></th></tr></thead><tbody>${rows.map((r,i)=>`<tr><td><span class="record-name" data-doc="${i}">${r[0]}</span><span class="record-sub">SHOT-${String(128-i).padStart(3,'0')} · 经典镜头库</span></td><td>${r[1]}</td><td>${r[2].split(', ').map(x=>`<span class="chip gray" style="margin-right:4px">${x}</span>`).join('')}</td><td><span class="chip ${r[3]==='已发布'?'green':r[3]==='待审核'?'amber':'gray'}">${r[3]}</span></td><td>${r[4]}</td><td><button class="icon-btn" data-action="toast" data-message="更多操作">${icon('more-vertical')}</button></td></tr>`).join('')}</tbody></table><div class="table-foot"><span>共 128 条记录</span><div class="pagination"><button class="page-btn active">1</button><button class="page-btn">2</button><button class="page-btn">3</button><button class="page-btn">…</button><button class="page-btn">16</button></div></div></section></div>`;
  document.querySelectorAll('[data-doc]').forEach(el => el.addEventListener('click', () => openShotDetail(rows[Number(el.dataset.doc)])));
}
function statsRow() { return `<div class="stats-row">${statCard('总资产','128','较上月','+12%','clapperboard')}${statCard('待审核','6','需要你的处理','+2','inbox',true)}${statCard('已发布','97','发布率 75.8%','+8.4%','check')}${statCard('本月新增','24','较上月','+18%','sparkles')}</div>`; }
function renderFaq() { content.innerHTML = `<div class="page-wrap">${pageHeading('文档库','FAQ 知识库','沉淀团队高频问题，统一维护经过审核的标准答案。','<button class="btn-primary" data-action="toast" data-message="已打开新建 FAQ 表单">'+icon('plus')+' 新建 FAQ</button>')}${tableToolbar()}<section class="panel"><table class="data-table"><thead><tr><th style="width:48%">问题</th><th>分类</th><th>来源</th><th>状态</th><th>更新时间</th><th></th></tr></thead><tbody>${[['如何提交一个新的镜头资产？','内容录入','团队手册','已发布','今天'],['镜头的质量等级如何判断？','质量规范','审核规范','已发布','昨天'],['如何申请使用镜头问答助手？','智能应用','权限说明','已发布','8月20日'],['导入 CSV 时有哪些字段要求？','数据管理','操作指南','待审核','8月18日']].map(r=>`<tr><td><span class="record-name">${r[0]}</span></td><td>${r[1]}</td><td>${r[2]}</td><td><span class="chip ${r[3]==='已发布'?'green':'amber'}">${r[3]}</span></td><td>${r[4]}</td><td><button class="icon-btn" data-action="toast" data-message="更多操作">${icon('more-vertical')}</button></td></tr>`).join('')}</tbody></table><div class="table-foot"><span>共 32 条记录</span><div class="pagination"><button class="page-btn active">1</button><button class="page-btn">2</button><button class="page-btn">3</button></div></div></section></div>`; }

function renderDocuments() {
  const docs = [['镜头库录入规范','团队知识 · 2026-08-22','file-text'],['内容审核与发布流程','流程文档 · 2026-08-21','file-edit'],['Agent 整理提示词模板','智能应用 · 2026-08-19','wand-sparkles'],['本周创作例会纪要','会议记录 · 2026-08-18','notebook-pen'],['品牌视觉参考清单','参考资料 · 2026-08-15','file-text']];
  content.innerHTML = `<div class="page-wrap">${pageHeading('文档内容','文档库','以 Markdown 记录团队知识，按文件夹管理并支持全文检索。','<button class="btn-primary" data-action="new-doc">'+icon('file-plus-2')+' 新建文档</button>')}<div class="toolbar"><label class="search-input">${icon('search')}<input placeholder="搜索文档..." /></label><span class="toolbar-spacer"></span><div class="segmented"><button class="active" data-doc-mode="list">文档</button><button data-doc-mode="chat">对话</button></div></div><div id="docContent"></div></div>`;
  renderDocMode('list', docs);
  document.querySelectorAll('[data-doc-mode]').forEach(btn => btn.addEventListener('click', () => { document.querySelectorAll('[data-doc-mode]').forEach(b=>b.classList.toggle('active', b===btn)); renderDocMode(btn.dataset.docMode, docs); }));
  document.querySelector('[data-action="new-doc"]').addEventListener('click', () => openEditor('未命名文档'));
}
function renderDocMode(mode, docs) { const target = document.getElementById('docContent'); if (!target) return; if (mode === 'chat') { target.innerHTML = `<div class="panel chat-mini"><div class="panel-head"><div><h2>灵感对话</h2><small>基于当前知识库内容</small></div><span class="chip green">在线</span></div><div class="mini-stream"><div class="message"><div class="message-avatar">${icon('bot')}</div><div class="message-bubble">你好，林默。你可以问我镜头语言、录入规范，或者让我基于现有资产给出创作建议。</div></div><div class="message user"><div class="message-avatar">林</div><div class="message-bubble">给我 3 个适合表现“疏离感”的镜头参考。</div></div><div class="message"><div class="message-avatar">${icon('bot')}</div><div class="message-bubble">可以从这三个方向开始：<ul><li>长焦压缩下的隔窗侧脸</li><li>固定机位中的人物离场</li><li>空镜与人物背影的错位</li></ul></div></div></div><div class="mini-composer"><input placeholder="继续提问..." /><button class="send-btn" data-action="toast" data-message="请先输入问题">${icon('send')}</button></div></div>`; return; } target.innerHTML = `<div class="doc-layout"><aside class="panel folder-panel"><div class="folder-title"><span>文件夹</span><button class="tiny-btn" data-action="toast" data-message="已新建文件夹">${icon('plus')}</button></div><div class="folder-row active">${icon('folder-open')}<span>全部文档</span><span class="folder-count">24</span></div><div class="folder-row">${icon('folder')}<span>团队规范</span><span class="folder-count">8</span></div><div class="folder-row folder-indent">${icon('folder')}<span>录入与审核</span><span class="folder-count">4</span></div><div class="folder-row folder-indent">${icon('folder')}<span>智能应用</span><span class="folder-count">3</span></div><div class="folder-row">${icon('folder')}<span>创作资料</span><span class="folder-count">12</span></div><div class="folder-row">${icon('folder')}<span>未分类</span><span class="folder-count">4</span></div></aside><section class="panel doc-panel"><div class="panel-head"><div><h2>全部文档</h2><small>24 篇文档</small></div><button class="icon-btn" data-action="toast" data-message="排序方式已切换">${icon('filter')}</button></div><div class="doc-list">${docs.map(d=>`<div class="doc-item" data-doc-title="${d[0]}"><span class="doc-icon">${icon(d[2])}</span><div class="doc-item-copy"><strong>${d[0]}</strong><small>${d[1]}</small></div><span class="doc-time">${d[1].split(' · ')[1]}</span>${icon('more-horizontal')}</div>`).join('')}</div><div class="table-foot"><span>最近编辑优先</span><div class="pagination"><button class="page-btn active">1</button><button class="page-btn">2</button><button class="page-btn">3</button></div></div></section></div>`; document.querySelectorAll('[data-doc-title]').forEach(el => el.addEventListener('click', () => openEditor(el.dataset.docTitle))); document.querySelectorAll('.folder-panel .folder-row:not(.active)').forEach(row => row.addEventListener('click', () => openDocumentNode(row.querySelector('span')?.textContent.trim()))); }
function renderMyNotes() { content.innerHTML = `<div class="page-wrap">${pageHeading('我的内容','我的笔记','你创建的文档、草稿和灵感记录。','<button class="btn-primary" data-action="new-doc">'+icon('file-plus-2')+' 写一篇笔记</button>')}<section class="panel"><div class="panel-head"><div><h2>最近笔记</h2><small>8 篇内容</small></div><div class="segmented"><button class="active">全部</button><button>草稿</button><button>已发表</button></div></div><div class="doc-list">${['夜景情绪光线笔记','一个关于“留白”的想法','北方公路的风与尘','参考片单：城市边缘'].map((x,i)=>`<div class="doc-item" data-doc-title="${x}"><span class="doc-icon">${icon(i===1?'sparkles':'notebook-pen')}</span><div class="doc-item-copy"><strong>${x}</strong><small>${i===0?'刚刚编辑':'林默 · '+(i+1)+' 天前'} · ${i===1?'草稿':'已保存'}</small></div><span class="chip ${i===1?'gray':'blue'}">${i===1?'草稿':'笔记'}</span>${icon('more-horizontal')}</div>`).join('')}</div></section></div>`; document.querySelector('[data-action="new-doc"]').addEventListener('click', () => openEditor('未命名笔记')); document.querySelectorAll('[data-doc-title]').forEach(el=>el.addEventListener('click',()=>openEditor(el.dataset.docTitle))); }
function renderReview() { content.innerHTML = `<div class="page-wrap">${pageHeading('内容治理','待我审核','确认 Agent 整理结果，让高质量资产进入发布流程。','<button class="btn-secondary" data-action="toast" data-message="已切换批量审核">批量审核</button>')}<div class="stats-row">${statCard('待处理','6','平均等待 1.4 天','-2','inbox')}${statCard('本周已处理','18','通过率 83%','+6','check')}${statCard('退回修改','3','需要补充信息','+1','square-pen',true)}${statCard('自动发布','41','模型规则触发','+12','wand-sparkles')}</div><section class="panel"><div class="panel-head"><h2>审核队列</h2><div class="head-actions"><select class="filter-select"><option>全部模型</option><option>经典镜头库</option><option>通用文档</option></select></div></div><table class="data-table"><thead><tr><th>内容</th><th>提交人</th><th>整理结果</th><th>提交时间</th><th>操作</th></tr></thead><tbody>${[['手持跟拍穿过人群','周宁','字段完整 · 置信度 94%','今天 10:12'],['雨天公交站的等待','陈一','新增 5 个标签 · 置信度 87%','昨天 16:40'],['关于景别选择的 FAQ','林默','引用 2 篇文档 · 置信度 92%','昨天 14:06']].map(r=>`<tr><td><span class="record-name">${r[0]}</span><span class="record-sub">经典镜头库</span></td><td>${r[1]}</td><td><span class="chip green">${r[2]}</span></td><td>${r[3]}</td><td><button class="btn-secondary" data-action="toast" data-message="已打开审核详情">查看并处理</button></td></tr>`).join('')}</tbody></table></section></div>`; }
function renderAssistant() { content.innerHTML = `<div class="chat-layout"><div class="chat-heading"><div class="chat-orb">${icon('bot')}</div><h1>镜头问答助手</h1><p>基于经典镜头库，为你的创作提供有依据的灵感</p></div><div class="chat-stream" id="chatStream"><div class="message"><div class="message-avatar">${icon('bot')}</div><div><div class="message-bubble"><p>你好，林默。我可以帮你检索镜头资产、拆解镜头语言，也可以根据场景给出创作参考。</p><div class="message-source">${icon('database')} 已连接经典镜头库 · 128 条资产</div></div></div></div><div class="suggestions"><button class="suggestion" data-prompt="找 3 个适合表现疏离感的镜头">找 3 个适合表现疏离感的镜头</button><button class="suggestion" data-prompt="解释一下低机位的情绪效果">解释一下低机位的情绪效果</button><button class="suggestion" data-prompt="帮我整理一段镜头描述">帮我整理一段镜头描述</button></div></div><div class="chat-composer"><div class="composer-box"><textarea id="chatInput" rows="1" placeholder="向镜头问答助手提问..."></textarea><div class="composer-actions"><button class="mode-pill" data-action="toast" data-message="深度检索已开启">${icon('sparkles')} 深度检索</button><button class="icon-btn" data-action="toast" data-message="附件上传功能即将开放" title="添加附件">${icon('paperclip')}</button><span class="composer-spacer"></span><button class="send-btn" id="sendChat" disabled title="发送">${icon('send')}</button></div></div><div class="composer-note">回答由 AI 生成，请结合引用内容判断</div></div></div>`; bindChat(); }
function bindChat() { const input = document.getElementById('chatInput'); const send = document.getElementById('sendChat'); const sendMessage = () => { const textValue = input.value.trim(); if (!textValue) return; appendUserMessage(textValue); input.value = ''; send.disabled = true; showToast('消息已发送，等待服务端响应'); }; input.addEventListener('input', () => { send.disabled = !input.value.trim(); }); input.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMessage(); } }); send.addEventListener('click', sendMessage); document.querySelectorAll('.suggestion').forEach(btn => btn.addEventListener('click', () => { input.value = btn.dataset.prompt; send.disabled = false; input.focus(); })); }
function appendUserMessage(textValue) { const stream = document.getElementById('chatStream'); stream.insertAdjacentHTML('beforeend', `<div class="message user"><div class="message-avatar">林</div><div class="message-bubble">${escapeHtml(textValue)}</div></div>`); stream.scrollTop = stream.scrollHeight; }
function appendBotMessage(question) { const stream = document.getElementById('chatStream'); stream.insertAdjacentHTML('beforeend', `<div class="message"><div class="message-avatar">${icon('bot')}</div><div class="message-bubble"><p>根据经典镜头库的已发布资产，我整理了几个方向，与你的问题“${escapeHtml(question)}”相关：</p><ul><li>先用固定机位建立观察感，再让人物离开画面。</li><li>通过长焦压缩前后景，保留人物与环境的距离。</li><li>把环境声作为情绪线索，减少对白解释。</li></ul><div class="message-source">${icon('quote')} 引用 3 条镜头资产 · 权限范围内结果</div></div></div>`); }
function escapeHtml(str) { return str.replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#039;','"':'&quot;'}[c])); }
function openEditor(title) { currentDoc = title; breadcrumbTitle.textContent = title; document.querySelectorAll('.tree-item[data-view]').forEach(btn => btn.classList.remove('active')); content.innerHTML = `<div class="editor-layout">${pageHeading('Markdown 文档','编辑文档','','<button class="btn-secondary" data-action="back-doc">取消</button><button class="btn-primary" data-action="save-doc">'+icon('check')+' 保存文档</button>')}<div class="editor-toolbar"><button class="icon-btn" data-action="toast" data-message="已加粗">B</button><button class="icon-btn" data-action="toast" data-message="已添加斜体"><em>I</em></button><button class="icon-btn" data-action="toast" data-message="已插入引用">${icon('quote')}</button><button class="icon-btn" data-action="toast" data-message="已添加附件">${icon('paperclip')}</button><span class="toolbar-spacer"></span><span class="chip gray">Markdown</span><div class="segmented"><button class="active">编辑</button><button data-action="toast" data-message="预览模式已开启">预览</button></div></div><article class="editor-surface"><input class="editor-title" value="${escapeHtml(title)}" aria-label="文档标题" /><div class="editor-meta"><span class="tag">文档库</span><span>林默创建</span><span>·</span><span>自动保存已开启</span></div><textarea class="editor-body" aria-label="Markdown 正文"># ${title}\n\n记录你的想法、资料和上下文。\n\n## 为什么值得沉淀\n\n好的内容不只是被保存下来，还应该能够被团队搜索、引用，并在新的创作里继续发挥作用。\n\n- 保留原始来源与上下文\n- 使用清晰的 Markdown 结构\n- 添加可检索的标签和关联镜头\n\n> 这是一段可以继续编辑的文档内容。</textarea><div class="editor-bottom"><span class="save-state"><span class="sync-dot"></span>已自动保存 · 刚刚</span><span>约 186 字</span></div></article></div>`; document.querySelector('[data-action="back-doc"]').addEventListener('click', () => switchView(activeView === 'thoughts' ? 'documents' : activeView)); document.querySelector('[data-action="save-doc"]').addEventListener('click', () => showToast('文档已保存')); bindViewButtons(); }
function renderTasks() { content.innerHTML = `<div class="page-wrap">${pageHeading('智能应用','整理任务','查看 Agent 处理链路、运行状态与失败重试。','<button class="btn-primary" data-action="toast" data-message="已打开新建任务配置">'+icon('plus')+' 新建任务</button>')}<section class="panel"><div class="panel-head"><div><h2>任务列表</h2><small>3 个自动化任务</small></div><select class="filter-select"><option>全部状态</option><option>运行中</option><option>已暂停</option></select></div><table class="data-table"><thead><tr><th>任务名称</th><th>触发方式</th><th>最近运行</th><th>状态</th><th>成功率</th><th></th></tr></thead><tbody>${[['文档整理入库','每天 09:00','今天 09:00','运行中','98.2%'],['镜头标签补全','每周一 10:00','08-19 10:00','已暂停','94.6%'],['审核提醒','事件触发','刚刚','运行中','100%']].map(r=>`<tr><td><span class="record-name">${r[0]}</span></td><td>${r[1]}</td><td>${r[2]}</td><td><span class="chip ${r[3]==='运行中'?'green':'gray'}">${r[3]}</span></td><td>${r[4]}</td><td><button class="icon-btn" data-action="toast" data-message="更多操作">${icon('more-vertical')}</button></td></tr>`).join('')}</tbody></table></section></div>`; }
function renderSettings() { content.innerHTML = `<div class="page-wrap">${pageHeading('系统设置','设置','管理知识库信息、默认规则和个人偏好。')}<section class="panel settings-panel"><div class="settings-item"><div><strong>知识库名称</strong><small>显示在左上角的工作区选择器中</small></div><input class="setting-input" value="经典镜头库" /></div><div class="settings-item"><div><strong>默认可见性</strong><small>新建资产的默认访问范围</small></div><select class="setting-input"><option>登录可见</option><option>公开</option><option>用户组可见</option></select></div><div class="settings-item"><div><strong>自动保存草稿</strong><small>编辑中的文档自动保存最近版本</small></div><label class="switch"><input type="checkbox" checked /><span></span></label></div><div class="settings-item"><div><strong>回答引用来源</strong><small>在 Agent 回答底部展示关联资产</small></div><label class="switch"><input type="checkbox" checked /><span></span></label></div></section></div>`; }

// Lightweight workspace switcher keeps the prototype usable without a backend.
document.getElementById('workspaceSelect').addEventListener('click', () => showToast('知识库切换器已打开（当前为经典镜头库）'));
bindNavigationGroups();
switchView('thoughts');

function renderThoughts() {
  const title = activeThoughtTitle || '新思考';
  const derived = Boolean(activeThoughtTitle);
  const source = activeThoughtSource || { kind: '主干对话', title: '关于团队性格周期的探讨', summary: '已保留相关讨论片段，默认不带入全文。' };
  const sourceCard = derived ? `<div class="thought-source-card">${icon('quote')}<div><strong>来源：${escapeHtml(source.kind)} · 已保留引用摘要</strong><p><span class="source-summary">来自「${escapeHtml(source.title)}」：${escapeHtml(source.summary)}</span><span class="source-full">系统保留完整内容、关键转折和原始引用，当前只带入轻量摘要，避免新对话被旧内容淹没。</span></p><div class="source-actions"><button data-source-action="expand">展开全文</button><button data-source-action="open-trunk">查看来源</button></div></div></div>` : '';
  content.innerHTML = `<div class="thought-workbench"><section class="thought-main"><div class="chat-heading"><div class="chat-orb thought-orb">${icon('message-circle')}</div><div><h1>${escapeHtml(title)}</h1><p>${derived ? '待整理对话 · 可继续深聊并保留来源引用' : '新的对话会自动保存为思考主干'}</p></div></div>${sourceCard}<div class="chat-stream" id="thoughtStream"><div class="message"><div class="message-avatar thought-avatar">${icon('message-circle')}</div><div><div class="message-bubble"><p>${derived ? '这是从主干对话派生出的待整理对话。你可以继续补充观点，聊到成熟后再归档为文档或正式资产。' : '这是一个新的思考对话。直接说出你正在想的事，我会帮你追问、整理，并保留完整的碰撞过程。'}</p><div class="message-actions"><button data-message-action="save-note" title="存入当前笔记">${icon('notebook-pen')}</button><button data-message-action="follow-up" title="让 AI 继续追问">${icon('sparkles')}</button><button data-message-action="extract" title="转为正式资产">${icon('file-plus-2')}</button><button data-message-action="relate" title="关联到已有资产">${icon('paperclip')}</button></div></div></div></div></div><div class="thought-suggestions"><button class="suggestion" data-thought-prompt="我刚刚想到一个关于镜头的观点">记录一个刚刚想到的观点</button><button class="suggestion" data-thought-prompt="帮我把这个想法继续往下推">继续追问这个想法</button><button class="suggestion" data-thought-prompt="从这段对话里提炼一个派生思考">从对话中派生思考</button></div><div class="chat-composer"><div class="composer-box"><textarea id="thoughtInput" rows="1" placeholder="说出你正在想的事..."></textarea><div class="composer-actions"><button class="mode-pill" data-action="toast" data-message="内容会自动记录到思考主干">${icon('sparkles')} 自动记录</button><button class="icon-btn" data-action="toast" data-message="语音输入功能即将开放" title="语音输入">${icon('mic')}</button><span class="composer-spacer"></span><button class="send-btn" id="thoughtSend" disabled title="发送">${icon('send')}</button></div></div><div class="composer-note">对话会自动保存，成熟观点可以随时派生</div></div></section><aside class="thought-preview"><div class="preview-head"><div><strong>实时整理预览</strong><small>AI 会根据对话持续更新</small></div><button class="preview-toggle" data-preview-toggle title="收起预览">${icon('chevron-right')}</button></div><div class="preview-body"><span class="preview-label">Markdown 笔记</span><h2>${escapeHtml(derived ? title : '关于镜头语言的随想')}</h2><p>低机位与缓慢推近会改变观众的观看权力关系，适合表现压迫、紧张和不确定感。</p><div class="preview-section"><h3>AI 提取的标签</h3><div class="preview-tags"><span class="chip blue">低机位</span><span class="chip blue">压迫感</span><span class="chip gray">镜头语言</span><span class="chip gray">待确认</span></div></div><div class="preview-section"><h3>可能生成的资产</h3><div class="preview-asset">${icon('clapperboard')}<span>镜头条目 · 低机位压迫感镜头</span><span class="confidence">置信度 82%</span></div><div class="preview-asset">${icon('file-text')}<span>文档 · 镜头情绪拆解</span></div></div><div class="preview-section"><h3>已关联内容</h3><div class="preview-asset">${icon('quote')}<span>主干对话 · ${derived ? '已保留引用摘要' : '当前对话'}</span></div></div><div class="preview-actions"><button class="btn-secondary" data-action="toast" data-message="整理建议已刷新">${icon('sparkles')} 刷新整理</button><button class="btn-primary" data-action="derive-thought">${icon('git-branch')} 派生</button></div></div></aside></div>`;
  bindThoughtChat();
  bindViewButtons();
}
function bindThoughtChat() {
  const input = document.getElementById('thoughtInput');
  const send = document.getElementById('thoughtSend');
  const stream = document.getElementById('thoughtStream');
  const appendMessage = (markup) => { stream.insertAdjacentHTML('beforeend', markup); stream.scrollTop = stream.scrollHeight; };
  const sendMessage = () => { const value = input.value.trim(); if (!value) return; appendMessage(`<div class="message user"><div class="message-avatar">林</div><div class="message-bubble">${escapeHtml(value)}<div class="message-actions"><button data-message-action="save-note" title="存入当前笔记">${icon('notebook-pen')}</button><button data-message-action="follow-up" title="让 AI 继续追问">${icon('sparkles')}</button><button data-message-action="extract" title="转为正式资产">${icon('file-plus-2')}</button><button data-message-action="relate" title="关联到已有资产">${icon('paperclip')}</button></div></div></div>`); input.value = ''; send.disabled = true; setTimeout(() => appendMessage(`<div class="message"><div class="message-avatar thought-avatar">${icon('sparkles')}</div><div class="message-bubble"><p>我先把这个想法记下来。它可以继续往下追问，也可以在你觉得成型时派生为一条独立思考。</p><div class="message-source">${icon('check')} 已保存到思考主干</div><div class="message-actions"><button data-message-action="save-note" title="存入当前笔记">${icon('notebook-pen')}</button><button data-message-action="follow-up" title="让 AI 继续追问">${icon('sparkles')}</button><button data-message-action="extract" title="转为正式资产">${icon('file-plus-2')}</button><button data-message-action="relate" title="关联到已有资产">${icon('paperclip')}</button></div></div></div>`), 360); };
  input.addEventListener('input', () => { send.disabled = !input.value.trim(); });
  input.addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendMessage(); } });
  send.addEventListener('click', sendMessage);
  document.querySelectorAll('[data-thought-prompt]').forEach(btn => btn.addEventListener('click', () => { input.value = btn.dataset.thoughtPrompt; send.disabled = false; input.focus(); }));
  stream.addEventListener('click', event => { const button = event.target.closest('[data-message-action]'); if (!button) return; const message = button.closest('.message'); const action = button.dataset.messageAction; if (action === 'save-note') { message.classList.add('message-saved'); showToast('已存入当前笔记'); } if (action === 'follow-up') { input.value = '请围绕这条内容继续追问，并帮我确认还缺哪些信息。'; send.disabled = false; input.focus(); } if (action === 'extract') { openExtractModal(message.querySelector('.message-bubble')?.innerText || '当前对话片段', message); } if (action === 'relate') showToast('关联资产选择器已打开'); });
  document.querySelectorAll('[data-source-action]').forEach(button => button.addEventListener('click', () => { const card = button.closest('.thought-source-card'); if (button.dataset.sourceAction === 'expand') { const expanded = card.classList.toggle('is-expanded'); button.textContent = expanded ? '收起摘要' : '展开全文'; } else if (activeThoughtSource?.kind === '文档段落') { openEditor(activeThoughtSource.title, { returnView: 'thoughts' }); } else { activeThoughtTitle = ''; switchView('thoughts'); } }));
  document.querySelector('[data-preview-toggle]')?.addEventListener('click', event => { const preview = event.currentTarget.closest('.thought-preview'); preview.classList.toggle('is-collapsed'); event.currentTarget.innerHTML = icon(preview.classList.contains('is-collapsed') ? 'chevron-left' : 'chevron-right'); });
  document.querySelector('[data-action="derive-thought"]')?.addEventListener('click', () => { const title = '关于低机位压迫感的派生思考'; const pendingList = document.querySelector('.pending-chats'); if (pendingList && !Array.from(pendingList.querySelectorAll('.pending-chat')).some(item => item.dataset.thoughtTitle === title)) { const item = document.createElement('button'); item.className = 'tree-item pending-chat'; item.dataset.thoughtTitle = title; item.innerHTML = `${icon('message-circle')}<span>${escapeHtml(title)}</span><span class="pending-dot"></span>`; item.addEventListener('click', () => { activeThoughtTitle = title; switchView('thoughts', { preserveThought: true }); }); pendingList.prepend(item); const count = document.querySelector('[data-group="pending"] .nav-group-head .item-count'); if (count) count.textContent = pendingList.querySelectorAll('.pending-chat').length; } activeThoughtTitle = title; switchView('thoughts', { preserveThought: true }); showToast('已创建新思考，来源摘要已保留'); });
}

function openExtractModal(sourceText, sourceMessage, sourceParagraph) {
  document.querySelector('.extract-modal-backdrop')?.remove();
  document.body.insertAdjacentHTML('beforeend', `<div class="extract-modal-backdrop"><section class="extract-modal" role="dialog" aria-modal="true" aria-label="转为正式资产"><div class="extract-modal-head"><div><h2>转为正式资产</h2><p>AI 已根据当前对话提取字段，请确认后入库。</p></div><button class="extract-modal-close" data-modal-action="close" aria-label="关闭">${icon('x')}</button></div><div class="extract-modal-body"><div class="extract-type-row"><button class="extract-type active">镜头条目</button><button class="extract-type">文档</button><button class="extract-type">FAQ</button></div><div class="extract-fields"><div class="extract-field"><label>标题</label><input value="低机位压迫感镜头" /></div><div class="extract-field"><label>景别 <span class="confidence">高</span></label><input value="中近景" /></div><div class="extract-field"><label>机位 <span class="confidence">高</span></label><input value="低机位" /></div><div class="extract-field"><label>焦段 <span class="confidence">中，需确认</span></label><input value="35mm（估算）" /></div><div class="extract-field"><label>情绪</label><input value="压迫、紧张" /></div><div class="extract-field"><label>中文提示词</label><textarea>低机位、中近景、缓慢推近，人物被强烈轮廓光切出压迫感。</textarea></div></div><div class="extract-field" style="margin-top:12px"><label>来源摘要</label><textarea>${escapeHtml(sourceText.slice(0, 180))}</textarea></div><div class="extract-modal-foot"><button class="btn-secondary" data-modal-action="draft">存为草稿</button><button class="btn-primary" data-modal-action="confirm">确认入库</button></div></div></section></div>`);
  document.querySelectorAll('.extract-type').forEach(type => type.addEventListener('click', () => { document.querySelectorAll('.extract-type').forEach(item => item.classList.toggle('active', item === type)); }));
  const backdrop = document.querySelector('.extract-modal-backdrop');
  const closeModal = () => { backdrop?.remove(); document.removeEventListener('keydown', closeOnEscape); };
  const closeOnEscape = event => { if (event.key === 'Escape') closeModal(); };
  document.addEventListener('keydown', closeOnEscape);
  backdrop.addEventListener('click', event => { if (event.target === backdrop) closeModal(); });
  document.querySelectorAll('[data-modal-action]').forEach(button => button.addEventListener('click', () => { const action = button.dataset.modalAction; if (action === 'confirm' || action === 'draft') { const label = action === 'confirm' ? '已转为资产 · 待审核' : '已保存为资产草稿'; const target = sourceParagraph || sourceMessage; if (target) { target.classList.add(sourceParagraph ? 'paragraph-extracted' : 'message-extracted'); if (!target.querySelector('.message-extracted-label')) { const host = sourceParagraph || sourceMessage.querySelector('.message-bubble'); host?.insertAdjacentHTML('beforeend', `<div class="message-extracted-label">${icon('check')} ${label}</div>`); } } showToast(action === 'confirm' ? '资产已创建，等待审核' : '已保存为资产草稿'); } closeModal(); }));
}

// A document may also be a parent node: its content remains editable while child documents are listed below it.
function openEditor(title, options = {}) {
  const sourceView = activeView;
  const returnView = options.returnView || (sourceView === 'thoughts' ? 'documents' : sourceView);
  currentDoc = title;
  breadcrumbTitle.textContent = title;
  syncNavigationSelection();
  const children = options.children || [];
  const childSection = children.length ? `<section class="doc-children"><div class="doc-children-head"><div><strong>子文档</strong><small>${children.length} 篇</small></div><button class="btn-secondary" data-action="new-child">${icon('file-plus-2')} 新建子文档</button></div><div class="doc-child-list">${children.map(child => `<button class="doc-child-row" data-child-doc="${escapeHtml(child)}">${icon('file-text')}<span>${escapeHtml(child)}</span>${icon('chevron-right')}</button>`).join('')}</div></section>` : '';
  content.innerHTML = `<div class="editor-layout">${pageHeading('Markdown 文档', '编辑文档', '', '<button class="btn-secondary" data-action="back-doc">取消</button><button class="btn-primary" data-action="save-doc">'+icon('check')+' 保存文档</button>')}<div class="editor-toolbar"><button class="icon-btn" data-action="toast" data-message="已加粗">B</button><button class="icon-btn" data-action="toast" data-message="已添加斜体"><em>I</em></button><button class="icon-btn" data-action="toast" data-message="已插入引用">${icon('quote')}</button><button class="icon-btn" data-action="toast" data-message="已添加附件">${icon('paperclip')}</button><span class="toolbar-spacer"></span><span class="chip gray">Markdown</span><div class="segmented"><button class="active">编辑</button><button data-action="toast" data-message="预览模式已开启">预览</button></div></div><article class="editor-surface"><input class="editor-title" value="${escapeHtml(title)}" aria-label="文档标题" /><div class="editor-meta"><span class="tag">文档库</span><span>林默创建</span><span>·</span><span>自动保存已开启</span></div><textarea class="editor-body" aria-label="Markdown 正文"># ${escapeHtml(title)}\n\n记录你的想法、资料和上下文。\n\n## 为什么值得沉淀\n\n好的内容不只是被保存下来，还应该能够被团队搜索、引用，并在新的创作里继续发挥作用。\n\n- 保留原始来源与上下文\n- 使用清晰的 Markdown 结构\n- 添加可检索的标签和关联镜头\n\n> 这是一段可以继续编辑的文档内容。</textarea>${childSection}<div class="editor-bottom"><span class="save-state"><span class="sync-dot"></span>已自动保存 · 刚刚</span><span>约 186 字</span></div></article></div>`;
  document.querySelector('[data-action="back-doc"]').addEventListener('click', () => switchView(returnView));
  document.querySelector('[data-action="save-doc"]').addEventListener('click', () => showToast('文档已保存'));
  document.querySelector('[data-action="new-child"]')?.addEventListener('click', () => openEditor('未命名子文档', { parent: title }));
  document.querySelectorAll('[data-child-doc]').forEach(child => child.addEventListener('click', () => openDocumentNode(child.dataset.childDoc)));
  bindViewButtons();
}

function openShotDetail(row) {
  const [title, structure, tagText, status, updated] = row;
  const [shotSize = '中景', camera = '平视', movement = '固定'] = structure.split(' · ');
  const tags = tagText.split(', ');
  currentDoc = null;
  activeView = 'shots';
  breadcrumbTitle.textContent = title;
  syncNavigationSelection();
  const shotCode = `SHOT-${String(128 - ['雨夜街头的回望','电梯门打开后的停顿','手持跟拍穿过人群','窗边人物的侧脸光','天台风中的双人对话'].indexOf(title)).padStart(3,'0')}`;
  const actions = `<button class="btn-secondary" data-action="back-shots">${icon('chevron-left')} 返回列表</button><button class="btn-primary" data-action="save-shot">${icon('check')} 保存修改</button>`;
  content.innerHTML = `<div class="page-wrap">${pageHeading('经典镜头库', '镜头详情', `${shotCode} · 结构化镜头资产`, actions)}<div class="shot-detail-layout"><section class="panel shot-detail-main"><h2>${escapeHtml(title)}</h2><div class="shot-detail-sub">最后更新 ${updated} · 林默维护 · 经典镜头库</div><div class="shot-field-section"><h3>基础字段</h3><div class="shot-field-grid"><div class="shot-field"><label>景别</label><input value="${escapeHtml(shotSize)}" /></div><div class="shot-field"><label>机位</label><input value="${escapeHtml(camera)}" /></div><div class="shot-field"><label>运动方式</label><input value="${escapeHtml(movement)}" /></div></div></div><div class="shot-field-section"><h3>镜头描述</h3><div class="shot-field"><label>结构化描述</label><textarea>${escapeHtml(structure)}。用于记录画面构图、镜头运动和观看感受。</textarea></div></div><div class="shot-field-section"><h3>标签</h3><div class="shot-tags">${tags.map(tag => `<span class="chip gray">${escapeHtml(tag)}</span>`).join('')}<button class="btn-secondary" data-action="toast" data-message="标签编辑已打开">${icon('plus')} 添加标签</button></div></div></section><aside class="shot-side"><section class="panel shot-side-panel"><h3>资产状态</h3><div class="shot-status-row"><span>当前状态</span><span class="chip ${status === '已发布' ? 'green' : status === '待审核' ? 'amber' : 'gray'}">${status}</span></div><div class="shot-status-row"><span>资产编号</span><strong>${shotCode}</strong></div><div class="shot-status-row"><span>更新时间</span><span>${updated}</span></div></section><section class="panel shot-side-panel"><h3>来源与引用</h3><div class="shot-source"><strong>${icon('quote')} 来源片段</strong>来自团队镜头讨论中的结构化整理，保留原始对话与提炼过程。</div></section><section class="panel shot-side-panel"><h3>关联内容</h3><div class="shot-related">${icon('file-text')} <span>镜头库录入规范</span></div><div class="shot-related">${icon('message-circle')} <span>关于镜头语言的灵感对话</span></div></section></aside></div></div>`;
  document.querySelector('[data-action="back-shots"]').addEventListener('click', () => switchView('shots'));
  document.querySelector('[data-action="save-shot"]').addEventListener('click', () => showToast('镜头资产已保存'));
  bindViewButtons();
}

function renderDocuments() {
  const docs = [
    { title: '镜头库录入规范', meta: '团队知识 · 2026-08-22', icon: 'file-text', status: '已发布' },
    { title: '内容审核与发布流程', meta: '流程文档 · 2026-08-21', icon: 'file-edit', status: '待审核' },
    { title: 'Agent 整理提示词模板', meta: '智能应用 · 2026-08-19', icon: 'wand-sparkles', status: '已发布' },
    { title: '本周创作例会纪要', meta: '会议记录 · 2026-08-18', icon: 'notebook-pen', status: '草稿' },
    { title: '品牌视觉参考清单', meta: '参考资料 · 2026-08-15', icon: 'file-text', status: '已归档' }
  ];
  content.innerHTML = `<div class="page-wrap">${pageHeading('文档库', '全部文档', '按最近编辑时间浏览和筛选 Markdown 文档。', '<button class="btn-primary" data-action="new-doc">'+icon('file-plus-2')+' 新建文档</button>')}<div class="doc-filter-row"><label class="search-input">${icon('search')}<input id="docSearch" placeholder="搜索文档标题、类型..." /></label><select class="filter-select" id="docStatus"><option value="all">全部状态</option><option value="已发布">已发布</option><option value="待审核">待审核</option><option value="草稿">草稿</option><option value="已归档">已归档</option></select><select class="filter-select" id="docSort"><option value="recent">最近编辑</option><option value="title">标题排序</option></select></div><section class="panel doc-list-panel"><div class="panel-head"><div><h2>全部文档</h2><small id="docCount">24 篇文档</small></div><button class="icon-btn" data-action="toast" data-message="筛选条件已应用" title="筛选">${icon('filter')}</button></div><div class="doc-list" id="docList"></div><div class="table-foot"><span>最近编辑优先</span><div class="pagination"><button class="page-btn active">1</button><button class="page-btn">2</button><button class="page-btn">3</button></div></div></section></div>`;
  const list = document.getElementById('docList');
  const search = document.getElementById('docSearch');
  const status = document.getElementById('docStatus');
  const sort = document.getElementById('docSort');
  const draw = () => {
    const query = search.value.trim().toLowerCase();
    const filtered = docs.filter(doc => (!query || `${doc.title} ${doc.meta}`.toLowerCase().includes(query)) && (status.value === 'all' || doc.status === status.value));
    if (sort.value === 'title') filtered.sort((a, b) => a.title.localeCompare(b.title, 'zh-CN'));
    list.innerHTML = filtered.length ? filtered.map(doc => `<div class="doc-item" data-doc-title="${escapeHtml(doc.title)}"><span class="doc-icon">${icon(doc.icon)}</span><div class="doc-item-copy"><strong>${escapeHtml(doc.title)}</strong><small>${escapeHtml(doc.meta)}</small></div><span class="chip ${doc.status === '已发布' ? 'green' : doc.status === '待审核' ? 'amber' : doc.status === '草稿' ? 'gray' : 'blue'}">${doc.status}</span><span class="doc-time">${escapeHtml(doc.meta.split(' · ')[1])}</span>${icon('more-horizontal')}</div>`).join('') : '<div class="doc-empty">没有匹配的文档</div>';
    document.getElementById('docCount').textContent = filtered.length === docs.length ? '24 篇文档' : `${filtered.length} / 24 篇文档`;
    list.querySelectorAll('[data-doc-title]').forEach(item => item.addEventListener('click', () => openEditor(item.dataset.docTitle)));
  };
  search.addEventListener('input', draw);
  status.addEventListener('change', draw);
  sort.addEventListener('change', draw);
  document.querySelector('[data-action="new-doc"]').addEventListener('click', () => openEditor('未命名文档'));
  draw();
}

function paragraphMarkup(id, html) {
  return `<div class="rich-paragraph" data-paragraph-id="${id}"><p>${html}</p><button class="paragraph-action" data-paragraph-action="open" title="处理这段内容" aria-label="处理这段内容">${icon('sparkles')}</button></div>`;
}

function createDerivedThought(title, source = null) {
  const pendingList = document.querySelector('.pending-chats');
  if (pendingList && !Array.from(pendingList.querySelectorAll('.pending-chat')).some(item => item.dataset.thoughtTitle === title)) {
    const item = document.createElement('button');
    item.className = 'tree-item pending-chat';
    item.dataset.thoughtTitle = title;
    item.innerHTML = `${icon('message-circle')}<span>${escapeHtml(title)}</span><span class="pending-dot"></span>`;
    item.addEventListener('click', () => { activeThoughtTitle = title; switchView('thoughts', { preserveThought: true }); });
    pendingList.prepend(item);
    const count = document.querySelector('[data-group="pending"] .nav-group-head .item-count');
    if (count) count.textContent = pendingList.querySelectorAll('.pending-chat').length;
  }
  activeThoughtTitle = title;
  activeThoughtSource = source;
  switchView('thoughts', { preserveThought: true });
  showToast('已创建新思考，来源摘要已保留');
}

function openParagraphActionMenu(paragraph) {
  document.querySelectorAll('.paragraph-action-menu').forEach(menu => menu.remove());
  const sourceText = paragraph.querySelector('p, h2, h3, blockquote, li')?.innerText || paragraph.innerText || '当前段落';
  paragraph.insertAdjacentHTML('beforeend', `<div class="paragraph-action-menu"><button data-paragraph-menu-action="save">${icon('notebook-pen')}存笔记</button><button data-paragraph-menu-action="derive">${icon('git-branch')}新思考</button><button data-paragraph-menu-action="discard">${icon('x')}忽略</button></div>`);
  const menu = paragraph.querySelector('.paragraph-action-menu');
  menu.querySelectorAll('[data-paragraph-menu-action]').forEach(button => button.addEventListener('click', () => {
    const action = button.dataset.paragraphMenuAction;
    if (action === 'save') { paragraph.classList.add('paragraph-saved'); showToast('已将这段内容存入当前笔记'); }
    if (action === 'derive') createDerivedThought('从当前段落派生的思考', { kind: '对话段落', title: activeThoughtTitle || '新思考', summary: sourceText.slice(0, 88) });
    if (action === 'discard') { paragraph.classList.add('paragraph-discarded'); showToast('已从当前笔记草稿中忽略这段内容'); }
    menu.remove();
  }));
  setTimeout(() => document.addEventListener('click', function closeMenu(event) {
    if (!paragraph.contains(event.target)) { menu.remove(); document.removeEventListener('click', closeMenu); }
  }), 0);
}

function renderThoughts() {
  const title = activeThoughtTitle || '新思考';
  const derived = Boolean(activeThoughtTitle);
  const source = activeThoughtSource || { kind: '主干对话', title: '关于团队性格周期的探讨', summary: '已保留相关讨论片段，默认不带入全文。' };
  const sourceCard = derived ? `<div class="thought-source-card">${icon('quote')}<div><strong>来源：${escapeHtml(source.kind)} · 已保留引用摘要</strong><p><span class="source-summary">来自「${escapeHtml(source.title)}」：${escapeHtml(source.summary)}</span><span class="source-full">系统保留完整内容、关键转折和原始引用，当前只带入轻量摘要，避免新对话被旧内容淹没。</span></p><div class="source-actions"><button data-source-action="expand">展开全文</button><button data-source-action="open-trunk">查看来源</button></div></div></div>` : '';
  const relatedDocs = [
    { title: '镜头库录入规范', meta: 'Markdown · 已发布', icon: 'file-text' },
    { title: '低机位情绪拆解', meta: 'Markdown · 待审核', icon: 'notebook-pen' },
    { title: '内容审核与发布流程', meta: '流程文档 · 已发布', icon: 'file-edit' }
  ];
  content.innerHTML = `<div class="thought-workbench"><section class="thought-main"><div class="chat-heading"><div class="chat-orb thought-orb">${icon('message-circle')}</div><div><h1>${escapeHtml(title)}</h1><p>${derived ? '待整理对话 · 可继续深聊并保留来源引用' : '新的对话会自动保存为思考主干'}</p></div></div>${sourceCard}<div class="chat-stream" id="thoughtStream"><div class="message"><div class="message-avatar thought-avatar">${icon('sparkles')}</div><div class="message-content"><div class="message-bubble rich-bubble"><div class="telegram-meta">AI 助手 <span>刚刚</span></div><div class="rich-text">${paragraphMarkup('p1', '我先把这段想法拆成几个可以继续追问的部分，保留原始语境，再逐步整理成可复用的内容。')}${paragraphMarkup('p2', '<strong>当前判断：</strong>低机位与缓慢推近会改变观众的观看权力关系，适合表现压迫、紧张和不确定感。')}${paragraphMarkup('p3', '如果主体在画面里保持克制，镜头本身的运动就会成为情绪来源。可以先记录为 <code>camera_angle: low</code>，焦段和运动速度等信息再通过追问补齐。')}<div class="rich-list"><span>保留原始观点与上下文</span><span>标出仍需确认的字段</span><span>随时从关键段落收割资产</span></div>${paragraphMarkup('p4', '<em>建议先继续聊，不要急着填表；当某个结论说透时，再点击段落右侧的处理按钮。</em>')}</div><div class="message-source">${icon('check')} 已保存到思考主干 · 可继续收割</div></div></div></div></div><div class="thought-suggestions"><button class="suggestion" data-thought-prompt="我刚刚想到一个关于镜头的观点">记录一个刚刚想到的观点</button><button class="suggestion" data-thought-prompt="帮我把这个想法继续往下推">继续追问这个想法</button><button class="suggestion" data-thought-prompt="从这段对话里提炼一个派生思考">从对话中派生思考</button></div><div class="thought-compose-zone"><div class="compose-tools"><span class="compose-tools-label">整理动作</span><button data-compose-action="derive">${icon('git-branch')} 派生思考</button><button data-compose-action="organize">${icon('wand-sparkles')} 整理这轮对话</button><button data-compose-action="archive">${icon('archive')} 归档主干</button></div><div class="chat-composer"><div class="composer-box"><textarea id="thoughtInput" rows="1" placeholder="说出你正在想的事..."></textarea><div class="composer-actions"><button class="mode-pill" data-action="toast" data-message="内容会自动记录到思考主干">${icon('sparkles')} 自动记录</button><button class="icon-btn" data-action="toast" data-message="语音输入功能即将开放" title="语音输入">${icon('mic')}</button><span class="composer-spacer"></span><button class="send-btn" id="thoughtSend" disabled title="发送">${icon('send')}</button></div></div><div class="composer-note">对话会自动保存，成熟观点可以随时派生</div></div></div></section><aside class="related-doc-float"><div class="related-doc-head"><div><strong>关联文档</strong><small>当前对话引用的内容</small></div>${icon('paperclip')}</div><div class="related-doc-list">${relatedDocs.map(doc => `<button class="related-doc-row" data-related-doc="${escapeHtml(doc.title)}"><span class="related-doc-icon">${icon(doc.icon)}</span><span class="related-doc-copy"><strong>${escapeHtml(doc.title)}</strong><small>${escapeHtml(doc.meta)}</small></span>${icon('chevron-right')}</button>`).join('')}</div><div class="related-doc-foot">点击文档可进入独立查看页</div></aside></div>`;
  bindThoughtChat();
  bindViewButtons();
}

function bindThoughtChat() {
  const input = document.getElementById('thoughtInput');
  const send = document.getElementById('thoughtSend');
  const stream = document.getElementById('thoughtStream');
  const appendMessage = markup => { stream.insertAdjacentHTML('beforeend', markup); stream.scrollTop = stream.scrollHeight; };
  const sendMessage = () => { const value = input.value.trim(); if (!value) return; appendMessage(`<div class="message user"><div class="message-avatar">林</div><div class="message-content"><div class="message-bubble user-bubble"><div class="rich-text">${paragraphMarkup('user-' + Date.now(), escapeHtml(value))}</div><div class="telegram-meta user-meta">林默 <span>刚刚 · 已发送</span></div></div></div></div>`); input.value = ''; send.disabled = true; setTimeout(() => appendMessage(`<div class="message"><div class="message-avatar thought-avatar">${icon('sparkles')}</div><div class="message-content"><div class="message-bubble rich-bubble"><div class="telegram-meta">AI 助手 <span>刚刚</span></div><div class="rich-text">${paragraphMarkup('ai-' + Date.now(), '我先把这条内容记下来。你可以继续补充，也可以点击段落右侧的按钮，把它收割成笔记、资产或新的派生思考。')}</div></div></div></div>`), 360); };
  input.addEventListener('input', () => { send.disabled = !input.value.trim(); });
  input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); sendMessage(); } });
  send.addEventListener('click', sendMessage);
  document.querySelectorAll('[data-thought-prompt]').forEach(button => button.addEventListener('click', () => { input.value = button.dataset.thoughtPrompt; send.disabled = false; input.focus(); }));
  stream.addEventListener('click', event => { const paragraph = event.target.closest('.rich-paragraph'); if (paragraph && event.target.closest('[data-paragraph-action]')) { event.stopPropagation(); openParagraphActionMenu(paragraph); } });
  document.querySelectorAll('[data-source-action]').forEach(button => button.addEventListener('click', () => { const card = button.closest('.thought-source-card'); if (button.dataset.sourceAction === 'expand') { const expanded = card.classList.toggle('is-expanded'); button.textContent = expanded ? '收起摘要' : '展开全文'; } else { activeThoughtTitle = ''; switchView('thoughts'); } }));
  document.querySelectorAll('[data-compose-action]').forEach(button => button.addEventListener('click', () => { const action = button.dataset.composeAction; if (action === 'derive') createDerivedThought('从当前对话派生的思考'); if (action === 'organize') showToast('整理建议已生成，已标注待确认字段'); if (action === 'archive') showToast('主干对话已归档为内部笔记'); }));
  document.querySelectorAll('[data-related-doc]').forEach(button => button.addEventListener('click', () => openEditor(button.dataset.relatedDoc, { returnView: 'thoughts' })));
}

function answerActionsMarkup() {
  return `<div class="answer-actions"><button data-answer-action="save">${icon('notebook-pen')} 存笔记</button><button data-answer-action="extract">${icon('file-plus-2')} 转为资产</button><button data-answer-action="derive">${icon('git-branch')} 新思考</button></div>`;
}

function relatedContentMarkup(title, derived) {
  const noteTitle = derived ? `${title} · 整理笔记` : '关于镜头语言的随想';
  const sessionTitle = derived ? `继续深聊：${title}` : '从当前对话派生的思考';
  return `${derived ? `<div class="related-group"><span class="related-group-label">派生来源</span><button class="related-doc-row" data-related-action="source"><span class="related-doc-icon source">${icon('git-branch')}</span><span class="related-doc-copy"><strong>关于团队性格周期的探讨</strong><small>主干对话 · 保留引用摘要</small></span>${icon('chevron-right')}</button></div>` : ''}<div class="related-group"><span class="related-group-label">对应笔记文档</span><button class="related-doc-row" data-related-action="note" data-related-title="${escapeHtml(noteTitle)}"><span class="related-doc-icon">${icon('file-text')}</span><span class="related-doc-copy"><strong>${escapeHtml(noteTitle)}</strong><small>Markdown · 自动整理中</small></span>${icon('chevron-right')}</button></div><div class="related-group"><span class="related-group-label">对话派生的新会话</span><button class="related-doc-row" data-related-action="session" data-related-title="${escapeHtml(sessionTitle)}"><span class="related-doc-icon session">${icon('message-circle')}</span><span class="related-doc-copy"><strong>${escapeHtml(sessionTitle)}</strong><small>待整理对话 · 可继续深聊</small></span>${icon('chevron-right')}</button></div><div class="related-group"><span class="related-group-label">提炼出的资产数据</span><button class="related-doc-row" data-related-action="asset"><span class="related-doc-icon asset">${icon('clapperboard')}</span><span class="related-doc-copy"><strong>低机位压迫感镜头</strong><small>镜头条目 · 待审核</small></span>${icon('chevron-right')}</button></div>`;
}

function renderThoughts() {
  const title = activeThoughtTitle || '新思考';
  const derived = Boolean(activeThoughtTitle);
  const source = activeThoughtSource || { kind: '主干对话', title: '关于团队性格周期的探讨', summary: '已保留相关讨论片段，默认不带入全文。' };
  const sourceCard = derived ? `<div class="thought-source-card">${icon('quote')}<div><strong>来源：${escapeHtml(source.kind)} · 已保留引用摘要</strong><p><span class="source-summary">来自「${escapeHtml(source.title)}」：${escapeHtml(source.summary)}</span><span class="source-full">系统保留完整内容、关键转折和原始引用，当前只带入轻量摘要，避免新对话被旧内容淹没。</span></p><div class="source-actions"><button data-source-action="expand">展开全文</button><button data-source-action="open-trunk">查看来源</button></div></div></div>` : '';
  content.innerHTML = `<div class="thought-workbench"><section class="thought-main"><div class="chat-heading"><div class="chat-orb thought-orb">${icon('message-circle')}</div><div><h1>${escapeHtml(title)}</h1><p>${derived ? '待整理对话 · 可继续深聊并保留来源引用' : '新的对话会自动保存为思考主干'}</p></div></div>${sourceCard}<div class="chat-stream" id="thoughtStream"><div class="message"><div class="message-avatar thought-avatar">${icon('sparkles')}</div><div class="message-content"><div class="message-bubble rich-bubble"><div class="telegram-meta">AI 助手 <span>刚刚</span></div><div class="rich-text">${paragraphMarkup('p1', '我先把这段想法拆成几个可以继续追问的部分，保留原始语境，再逐步整理成可复用的内容。')}${paragraphMarkup('p2', '<strong>当前判断：</strong>低机位与缓慢推近会改变观众的观看权力关系，适合表现压迫、紧张和不确定感。')}${paragraphMarkup('p3', '如果主体在画面里保持克制，镜头本身的运动就会成为情绪来源。可以先记录为 <code>camera_angle: low</code>，焦段和运动速度等信息再通过追问补齐。')}<div class="rich-list"><span>保留原始观点与上下文</span><span>标出仍需确认的字段</span><span>随时从关键段落收割资产</span></div>${paragraphMarkup('p4', '<em>建议先继续聊，不要急着填表；当某个结论说透时，再点击段落右侧的处理按钮。</em>')}</div>${answerActionsMarkup()}</div></div></div></div><div class="thought-compose-zone"><div class="chat-composer"><div class="composer-box"><textarea id="thoughtInput" rows="1" placeholder="说出你正在想的事..."></textarea><div class="composer-actions"><button class="mode-pill" data-action="toast" data-message="内容会自动记录到思考主干">${icon('sparkles')} 自动记录</button><button class="icon-btn" data-action="toast" data-message="语音输入功能即将开放" title="语音输入">${icon('mic')}</button><span class="composer-spacer"></span><button class="send-btn" id="thoughtSend" disabled title="发送">${icon('send')}</button></div></div><div class="composer-note">对话会自动保存，成熟观点可以随时派生</div></div></div></section><aside class="related-doc-float"><div class="related-doc-head"><div><strong>关联内容</strong><small>当前会话的来源与产出</small></div>${icon('paperclip')}</div><div class="related-doc-list">${relatedContentMarkup(title, derived)}</div></aside></div>`;
  bindThoughtChat();
  bindViewButtons();
}

function bindThoughtChat() {
  const input = document.getElementById('thoughtInput');
  const send = document.getElementById('thoughtSend');
  const stream = document.getElementById('thoughtStream');
  const appendMessage = markup => { stream.insertAdjacentHTML('beforeend', markup); stream.scrollTop = stream.scrollHeight; };
  const sendMessage = () => { const value = input.value.trim(); if (!value) return; appendMessage(`<div class="message user"><div class="message-avatar">林</div><div class="message-content"><div class="message-bubble user-bubble"><div class="rich-text">${paragraphMarkup('user-' + Date.now(), escapeHtml(value))}</div><div class="telegram-meta user-meta">林默 <span>刚刚 · 已发送</span></div></div></div></div>`); input.value = ''; send.disabled = true; setTimeout(() => appendMessage(`<div class="message"><div class="message-avatar thought-avatar">${icon('sparkles')}</div><div class="message-content"><div class="message-bubble rich-bubble"><div class="telegram-meta">AI 助手 <span>刚刚</span></div><div class="rich-text">${paragraphMarkup('ai-' + Date.now(), '我先把这条内容记下来。你可以继续补充，也可以点击段落右侧的按钮，把它收割成笔记或开启一个新的思考。')}</div>${answerActionsMarkup()}</div></div></div>`), 360); };
  input.addEventListener('input', () => { send.disabled = !input.value.trim(); });
  input.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.shiftKey) { event.preventDefault(); sendMessage(); } });
  send.addEventListener('click', sendMessage);
  stream.addEventListener('click', event => {
    const paragraph = event.target.closest('.rich-paragraph');
    if (paragraph && event.target.closest('[data-paragraph-action]')) { event.stopPropagation(); openParagraphActionMenu(paragraph); return; }
    const answerButton = event.target.closest('[data-answer-action]');
    if (!answerButton) return;
    const message = answerButton.closest('.message');
    const action = answerButton.dataset.answerAction;
    if (action === 'save') { message.classList.add('answer-saved'); showToast('已将整条回答存入当前笔记'); }
    if (action === 'extract') openExtractModal(message.querySelector('.message-bubble')?.innerText || '当前回答', message);
    if (action === 'derive') createDerivedThought('从当前回答派生的思考');
  });
  document.querySelectorAll('[data-source-action]').forEach(button => button.addEventListener('click', () => { const card = button.closest('.thought-source-card'); if (button.dataset.sourceAction === 'expand') { const expanded = card.classList.toggle('is-expanded'); button.textContent = expanded ? '收起摘要' : '展开全文'; } else { activeThoughtTitle = ''; switchView('thoughts'); } }));
  document.querySelectorAll('[data-related-action]').forEach(button => button.addEventListener('click', () => { const action = button.dataset.relatedAction; if (action === 'source') { activeThoughtTitle = '关于团队性格周期的探讨'; switchView('thoughts', { preserveThought: true }); } if (action === 'note') openEditor(button.dataset.relatedTitle, { returnView: 'thoughts' }); if (action === 'session') { activeThoughtTitle = button.dataset.relatedTitle; switchView('thoughts', { preserveThought: true }); } if (action === 'asset') openShotDetail(['低机位压迫感镜头', '中近景 · 低机位 · 缓慢推进', '压迫感, 镜头语言', '待审核', '刚刚']); }));
}

function renderMyDocuments() {
  const docs = [
    { title: '关于镜头语言的随想', meta: '个人笔记 · 2026-08-23', icon: 'notebook-pen', status: '草稿' },
    { title: '低机位情绪拆解', meta: '个人整理 · 2026-08-22', icon: 'file-edit', status: '待审核' },
    { title: '我的参考片单', meta: '参考资料 · 2026-08-20', icon: 'file-text', status: '已发布' },
    { title: '创作复盘：本周三条片段', meta: '创作复盘 · 2026-08-18', icon: 'notebook-pen', status: '草稿' },
    { title: '待补充的镜头字段', meta: '录入草稿 · 2026-08-16', icon: 'file-edit', status: '待审核' },
    { title: '灵感摘录：留白与停顿', meta: '灵感笔记 · 2026-08-12', icon: 'sparkles', status: '已归档' }
  ];
  content.innerHTML = `<div class="page-wrap">${pageHeading('我的', '我的文档', '只查看由你创建或收藏的文档。', '<button class="btn-primary" data-action="new-my-doc">'+icon('file-plus-2')+' 新建文档</button>')}<div class="doc-filter-row"><label class="search-input">${icon('search')}<input id="myDocSearch" placeholder="搜索我的文档..." /></label><select class="filter-select" id="myDocStatus"><option value="all">全部状态</option><option value="已发布">已发布</option><option value="待审核">待审核</option><option value="草稿">草稿</option><option value="已归档">已归档</option></select><select class="filter-select" id="myDocSort"><option value="recent">最近编辑</option><option value="title">标题排序</option></select></div><section class="panel doc-list-panel"><div class="panel-head"><div><h2>我的文档</h2><small id="myDocCount">6 篇文档</small></div><button class="icon-btn" data-action="toast" data-message="筛选条件已应用" title="筛选">${icon('filter')}</button></div><div class="doc-list" id="myDocList"></div><div class="table-foot"><span>最近编辑优先</span><div class="pagination"><button class="page-btn active">1</button><button class="page-btn">2</button></div></div></section></div>`;
  const list = document.getElementById('myDocList');
  const search = document.getElementById('myDocSearch');
  const status = document.getElementById('myDocStatus');
  const sort = document.getElementById('myDocSort');
  const draw = () => {
    const query = search.value.trim().toLowerCase();
    const filtered = docs.filter(doc => (!query || `${doc.title} ${doc.meta}`.toLowerCase().includes(query)) && (status.value === 'all' || doc.status === status.value));
    if (sort.value === 'title') filtered.sort((a, b) => a.title.localeCompare(b.title, 'zh-CN'));
    list.innerHTML = filtered.length ? filtered.map(doc => `<div class="doc-item" data-my-doc-title="${escapeHtml(doc.title)}"><span class="doc-icon">${icon(doc.icon)}</span><div class="doc-item-copy"><strong>${escapeHtml(doc.title)}</strong><small>${escapeHtml(doc.meta)}</small></div><span class="chip ${doc.status === '已发布' ? 'green' : doc.status === '待审核' ? 'amber' : doc.status === '草稿' ? 'gray' : 'blue'}">${doc.status}</span><span class="doc-time">${escapeHtml(doc.meta.split(' · ')[1])}</span>${icon('more-horizontal')}</div>`).join('') : '<div class="doc-empty">没有匹配的文档</div>';
    document.getElementById('myDocCount').textContent = filtered.length === docs.length ? '6 篇文档' : `${filtered.length} / 6 篇文档`;
    list.querySelectorAll('[data-my-doc-title]').forEach(item => item.addEventListener('click', () => openEditor(item.dataset.myDocTitle)));
  };
  search.addEventListener('input', draw);
  status.addEventListener('change', draw);
  sort.addEventListener('change', draw);
  document.querySelector('[data-action="new-my-doc"]').addEventListener('click', () => openEditor('未命名文档'));
  draw();
}

function openEditorParagraphActionMenu(paragraph, docTitle) {
  document.querySelectorAll('.paragraph-action-menu').forEach(menu => menu.remove());
  const sourceText = paragraph.querySelector('p, h2, h3, blockquote, li')?.innerText || paragraph.innerText || '当前段落';
  paragraph.insertAdjacentHTML('beforeend', `<div class="paragraph-action-menu editor-paragraph-menu" contenteditable="false"><button data-editor-paragraph-menu-action="derive">${icon('git-branch')}新思考</button></div>`);
  const menu = paragraph.querySelector('.editor-paragraph-menu');
  menu.querySelectorAll('[data-editor-paragraph-menu-action]').forEach(button => button.addEventListener('click', event => {
    event.preventDefault();
    event.stopPropagation();
    const action = button.dataset.editorParagraphMenuAction;
    if (action === 'derive') {
      menu.remove();
      createDerivedThought(`从「${docTitle}」派生的思考`, { kind: '文档段落', title: docTitle, summary: sourceText.slice(0, 88) });
      return;
    }
    menu.remove();
  }));
  setTimeout(() => document.addEventListener('click', function closeMenu(event) {
    if (!paragraph.contains(event.target)) { menu.remove(); document.removeEventListener('click', closeMenu); }
  }), 0);
}

function bindEditorParagraphActions(docTitle) {
  document.querySelectorAll('[data-editor-paragraph-action]').forEach(button => button.addEventListener('click', event => {
    event.preventDefault();
    event.stopPropagation();
    const paragraph = button.closest('.editor-paragraph');
    if (paragraph) openEditorParagraphActionMenu(paragraph, docTitle);
  }));
}

// Documents open as a rendered, directly editable surface. Markdown is still the storage format,
// but its syntax never becomes part of the editing experience.
function openEditor(title, options = {}) {
  const sourceView = activeView;
  const returnView = options.returnView || (sourceView === 'thoughts' ? 'documents' : sourceView);
  currentDoc = title;
  breadcrumbTitle.textContent = title;
  syncNavigationSelection();
  const children = options.children || [];
  const childSection = children.length ? `<section class="doc-children"><div class="doc-children-head"><div><strong>子文档</strong><small>${children.length} 篇</small></div><button class="btn-secondary" data-action="new-child">${icon('file-plus-2')} 新建子文档</button></div><div class="doc-child-list">${children.map(child => `<button class="doc-child-row" data-child-doc="${escapeHtml(child)}">${icon('file-text')}<span>${escapeHtml(child)}</span>${icon('chevron-right')}</button>`).join('')}</div></section>` : '';
  const initialTitle = escapeHtml(title);
  content.innerHTML = `<div class="editor-layout">${pageHeading('文档库', '编辑文档', '直接在页面上编辑渲染后的内容。', '<button class="btn-secondary" data-action="back-doc">取消</button><button class="btn-primary" data-action="save-doc">'+icon('check')+' 保存文档</button>')}<div class="editor-toolbar" role="toolbar" aria-label="文本格式"><button class="icon-btn editor-tool" data-editor-command="bold" title="加粗"><strong>B</strong></button><button class="icon-btn editor-tool" data-editor-command="italic" title="斜体"><em>I</em></button><button class="icon-btn editor-tool" data-editor-command="formatBlock" data-editor-value="blockquote" title="引用">${icon('quote')}</button><button class="icon-btn editor-tool" data-editor-command="insertUnorderedList" title="项目列表">•</button><span class="toolbar-divider"></span><select class="editor-block-select" aria-label="段落样式"><option value="p">正文</option><option value="h2">小标题</option><option value="h3">三级标题</option></select><button class="icon-btn editor-tool" data-action="toast" data-message="附件功能即将开放" title="添加附件">${icon('paperclip')}</button></div><article class="editor-surface"><h1 class="editor-title" contenteditable="true" role="textbox" aria-label="文档标题">${initialTitle}</h1><div class="editor-meta"><span class="tag">文档库</span><span>林默创建</span><span>·</span><span>自动保存已开启</span></div><div class="editor-body" contenteditable="true" role="textbox" aria-label="文档正文"><p>记录你的想法、资料和上下文。</p><h2>为什么值得沉淀</h2><p>好的内容不只是被保存下来，还应该能够被团队搜索、引用，并在新的创作里继续发挥作用。</p><ul><li>保留原始来源与上下文</li><li>使用清晰的内容结构</li><li>添加可检索的标签和关联镜头</li></ul><blockquote>这是一段可以继续编辑的文档内容。</blockquote></div>${childSection}<div class="editor-bottom"><span class="save-state"><span class="sync-dot"></span><span data-save-label>已自动保存 · 刚刚</span></span><span data-char-count>约 186 字</span></div></article></div>`;
  const editorBody = document.querySelector('.editor-body');
  const editorTitle = document.querySelector('.editor-title');
  const saveLabel = document.querySelector('[data-save-label]');
  const charCount = document.querySelector('[data-char-count]');
  const paragraphAction = () => `<button class="editor-paragraph-action" contenteditable="false" data-editor-paragraph-action title="处理这段内容" aria-label="处理这段内容">${icon('sparkles')}</button>`;
  if (editorBody) editorBody.innerHTML = `<div class="editor-paragraph"><p>记录你的想法、资料和上下文。</p>${paragraphAction()}</div><div class="editor-paragraph"><h2>为什么值得沉淀</h2>${paragraphAction()}</div><div class="editor-paragraph"><p>好的内容不只是被保存下来，还应该能够被团队搜索、引用，并在新的创作里继续发挥作用。</p>${paragraphAction()}</div><ul><li class="editor-paragraph">保留原始来源与上下文${paragraphAction()}</li><li class="editor-paragraph">使用清晰的内容结构${paragraphAction()}</li><li class="editor-paragraph">添加可检索的标签和关联镜头${paragraphAction()}</li></ul><div class="editor-paragraph"><blockquote>这是一段可以继续编辑的文档内容。</blockquote>${paragraphAction()}</div>`;
  const updateCount = () => {
    const count = `${editorTitle?.innerText || ''}${editorBody?.innerText || ''}`.replace(/\s/g, '').length;
    if (charCount) charCount.textContent = `约 ${count || 0} 字`;
    if (saveLabel) saveLabel.textContent = '未保存更改';
  };
  editorBody?.addEventListener('input', updateCount);
  editorTitle?.addEventListener('input', updateCount);
  document.querySelectorAll('[data-editor-command]').forEach(button => {
    button.addEventListener('mousedown', event => event.preventDefault());
    button.addEventListener('click', () => {
      editorBody?.focus();
      document.execCommand(button.dataset.editorCommand, false, button.dataset.editorValue || null);
      updateCount();
    });
  });
  document.querySelector('.editor-block-select')?.addEventListener('change', event => {
    editorBody?.focus();
    document.execCommand('formatBlock', false, event.target.value);
    updateCount();
  });
  document.querySelector('[data-action="back-doc"]').addEventListener('click', () => switchView(returnView));
  document.querySelector('[data-action="save-doc"]').addEventListener('click', () => {
    if (saveLabel) saveLabel.textContent = '已自动保存 · 刚刚';
    showToast('文档已保存');
  });
  document.querySelector('[data-action="new-child"]')?.addEventListener('click', () => openEditor('未命名子文档', { parent: title }));
  document.querySelectorAll('[data-child-doc]').forEach(child => child.addEventListener('click', () => openDocumentNode(child.dataset.childDoc)));
  bindEditorParagraphActions(editorTitle?.innerText.trim() || title);
  bindViewButtons();
}
