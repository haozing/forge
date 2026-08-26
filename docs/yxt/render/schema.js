(function () {
  function fieldDefinitions(schema) {
    if (Array.isArray(schema?.fields)) return schema.fields.map(field => ({ key: field.key || field.name, ...field }));
    return Object.entries(schema?.properties || {}).map(([key, field]) => ({ key, ...field }));
  }

  function valueLabel(value) {
    if (Array.isArray(value)) return value.join(', ');
    if (value && typeof value === 'object') return JSON.stringify(value);
    return value === undefined || value === null ? '' : String(value);
  }

  function renderListValue(item, column) {
    const key = typeof column === 'string' ? column : column.field || column.key;
    if (key === 'title') return item.title || '';
    if (key === 'summary') return item.summary || '';
    if (key === 'tags') return (item.tags || []).map(tag => `<span class="chip gray">${escapeHtml(tag)}</span>`).join(' ');
    return escapeHtml(valueLabel(item.fields?.[key]));
  }

  function renderFormField(field, value) {
    const key = field.key;
    const label = field.label || field.title || key;
    const type = field.type === 'text' ? 'textarea' : 'input';
    if (type === 'textarea') return `<label class="extract-field"><span>${escapeHtml(label)}</span><textarea data-schema-field="${escapeHtml(key)}">${escapeHtml(valueLabel(value))}</textarea></label>`;
    const inputType = field.type === 'boolean' ? 'checkbox' : field.type === 'number' || field.type === 'integer' ? 'number' : 'text';
    return `<label class="extract-field"><span>${escapeHtml(label)}</span><input data-schema-field="${escapeHtml(key)}" type="${inputType}" value="${escapeHtml(valueLabel(value))}" ${inputType === 'checkbox' && value ? 'checked' : ''}/></label>`;
  }

  window.YXTSchema = { fieldDefinitions, renderListValue, renderFormField };
})();
