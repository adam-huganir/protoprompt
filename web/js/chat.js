// Chat pane: transcript rendering, streaming generate/stop, sibling
// navigation, preview+accept, edit+resend. No framework; DOM directly.
import { fetchJSON, streamSSE } from './api.js';
import { showInlineDiff } from './editor.js';
import { applyHighlight, attachOverlay } from './mdhl.js';
import { state } from './state.js';

const $ = (sel, root) => (root || document).querySelector(sel);

let ui = null;        // DOM refs
let deps = null;      // { toast }
let controller = null; // AbortController for live stream
let busy = false;     // a stream or request is in flight
let modelIds = [];    // ids from the upstream model list
let modelErr = '';    // last /api/models failure, shown as a banner
let pendingModel = ''; // pick used before a session exists (and for new sessions)
let inputHl = null; // highlight overlay handle for ui.input (attached in mountChat)

export function currentSessionId() { return state.currentSessionId; }

export function mountChat(root, d) {
  deps = d || {};
  ui = {
    banners: $('#chat-banners', root),
    list: $('#msg-list', root),
    empty: $('#chat-empty', root),
    input: $('#input', root),
    btnSend: $('#btn-send', root),
    err: $('#input-err', root),
    modelSel: $('#model-select', root),
    btnModelRefresh: $('#btn-model-refresh', root),
  };

  inputHl = attachOverlay(ui.input);
  inputHl.sync();

  ui.btnSend.addEventListener('click', () => send());
  ui.input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(); }
  });
  ui.input.addEventListener('input', autoGrow);

  ui.modelSel.addEventListener('change', () => applyModel(ui.modelSel.value));
  ui.btnModelRefresh.addEventListener('click', () => loadModels());
  loadModels();

  return { refreshBanners, refreshModels: renderModels, loadSession, selectSession, newSession, renameSession, deleteSession, isBusy: () => busy };
}

function toast(kind, msg) { if (deps && deps.toast) deps.toast(kind, msg); }

function autoGrow() {
  ui.input.style.height = 'auto';
  ui.input.style.height = Math.min(ui.input.scrollHeight, 220) + 'px';
  inputHl.sync();
}

function sess() { return state.session && state.session.session; }

// ---------- session lifecycle ----------

let loadSeq = 0;
async function loadSession(id) {
  const my = ++loadSeq;
  if (!id) { state.session = null; state.currentSessionId = null; render(); return; }
  try {
    const payload = await fetchJSON('/api/sessions/' + encodeURIComponent(id));
    if (my !== loadSeq) return; // superseded by a newer load
    state.session = payload;
    state.currentSessionId = id;
  } catch (e) {
    if (my !== loadSeq) return;
    toast('error', 'could not load session: ' + e.message);
    state.session = null;
    state.currentSessionId = null;
  }
  render();
}

async function selectSession(id) {
  await loadSession(id);
  document.dispatchEvent(new CustomEvent('cv:sessionsChanged'));
}

async function newSession() {
  if (!state.currentFile) { toast('error', 'open a prompt file first'); return; }
  try {
    const body = { file: state.currentFile };
    if (pendingModel) body.model = pendingModel;
    const s = await fetchJSON('/api/sessions', {
      method: 'POST',
      body: JSON.stringify(body),
    });
    state.currentSessionId = s.id;
    state.session = { session: s, messages: [] };
    render();
    document.dispatchEvent(new CustomEvent('cv:sessionsChanged'));
  } catch (e) {
    toast('error', 'could not create session: ' + e.message);
  }
}

async function renameSession() {
  const s = sess();
  if (!s) return;
  const title = prompt('Session title:', s.title || '');
  if (title == null) return;
  try {
    const upd = await fetchJSON('/api/sessions/' + encodeURIComponent(s.id), {
      method: 'PUT',
      // PUT is a full replacement, so echo every editable field: sending the
      // title alone would reset a switched model to the config default.
      body: JSON.stringify({
        title,
        model: s.model,
        reasoningEffort: s.reasoningEffort,
        temperature: s.temperature,
        maxTokens: s.maxTokens,
        extraBody: s.extraBody,
      }),
    });
    state.session.session = upd;
    render();
    document.dispatchEvent(new CustomEvent('cv:sessionsChanged'));
  } catch (e) {
    toast('error', e.message);
  }
}

async function deleteSession() {
  const s = sess();
  if (!s) return;
  if (!confirm('Delete this session and all its messages?')) return;
  try {
    await fetchJSON('/api/sessions/' + encodeURIComponent(s.id), { method: 'DELETE' });
    state.session = null;
    state.currentSessionId = null;
    render();
    document.dispatchEvent(new CustomEvent('cv:sessionsChanged'));
  } catch (e) {
    toast('error', e.message);
  }
}

// ---------- model picker ----------

async function loadModels() {
  if (!ui) return;
  ui.modelSel.disabled = true;
  try {
    const res = await fetchJSON('/api/models');
    modelIds = res && Array.isArray(res.models) ? res.models : [];
    modelErr = '';
  } catch (e) {
    modelErr = e.message; // keep whatever options we already had
  }
  ui.modelSel.disabled = busy;
  renderModels();
  refreshBanners();
}

function effectiveModel() {
  const cfg = state.config || {};
  const s = sess();
  return (s ? s.model : pendingModel) || cfg.defaultModel || '';
}

function modelChoices() {
  const cfg = state.config || {};
  const out = [];
  const add = (id) => { if (id && !out.includes(id)) out.push(id); };
  for (const id of modelIds) add(id);
  for (const id of (cfg.models || [])) add(id);
  add(cfg.defaultModel);
  add(effectiveModel());
  return out;
}

function renderModels() {
  if (!ui) return;
  const cur = effectiveModel();
  const list = modelChoices();
  ui.modelSel.innerHTML = '';
  if (!list.length) {
    const o = document.createElement('option');
    o.value = '';
    o.textContent = '— no models —';
    o.disabled = true;
    ui.modelSel.appendChild(o);
    ui.modelSel.value = '';
    return;
  }
  for (const id of list) {
    const o = document.createElement('option');
    o.value = id;
    o.textContent = id;
    ui.modelSel.appendChild(o);
  }
  ui.modelSel.value = cur;
  if (ui.modelSel.selectedIndex < 0) ui.modelSel.selectedIndex = 0;
}

async function applyModel(model) {
  const prev = effectiveModel();
  pendingModel = model;
  if (!sess()) { renderModels(); return; }
  ui.modelSel.disabled = true;
  try {
    const upd = await fetchJSON('/api/sessions/' + encodeURIComponent(sess().id) + '/model', {
      method: 'POST',
      body: JSON.stringify({ model }),
    });
    state.session.session = upd;
  } catch (e) {
    pendingModel = prev;
    toast('error', 'could not switch model: ' + e.message);
  }
  ui.modelSel.disabled = busy;
  renderModels();
}

// ---------- banners ----------

function refreshBanners() {
  if (!ui) return;
  ui.banners.innerHTML = '';
  const cfg = state.config;
  if (cfg && !cfg.hasKey) {
    ui.banners.appendChild(banner('No API key configured — generation is disabled.', 'warn'));
  }
  if (modelErr) {
    ui.banners.appendChild(banner('Model list unavailable (' + modelErr + ') — showing configured models only.', 'warn'));
  }
  if (!cfg) return;
  const latestRev = latestRevId();
  if (cfg.hasKey && state.currentFile && !latestRev) {
    ui.banners.appendChild(banner('Save the prompt file (Ctrl+S) before generating.', 'warn'));
  }
  const stale = countStale();
  if (stale > 0) {
    const b = banner(stale + ' repl' + (stale === 1 ? 'y' : 'ies') + ' used a different prompt revision.', 'info');
    ui.banners.appendChild(b);
  }
}

function banner(text, kind) {
  const d = document.createElement('div');
  d.className = 'banner banner-' + kind;
  d.textContent = text;
  return d;
}

function latestRevId() {
  const info = deps && deps.getRevInfo ? deps.getRevInfo() : null;
  return info ? info.latestRevId : null;
}

function countStale() {
  const lr = latestRevId();
  if (!lr || !state.session) return 0;
  return state.session.messages.filter(m => m.role === 'assistant' && m.promptRevId && m.promptRevId !== lr).length;
}

// ---------- render ----------

function render() {
  if (!ui) return;
  refreshBanners();
  renderModels();
  ui.err.textContent = '';
  ui.list.innerHTML = '';
  const s = sess();
  if (!state.currentFile) {
    showEmpty('No prompt file', 'Create a prompt file in the editor to start a session.');
    return;
  }
  if (!s) {
    showEmpty('No session', 'Start a new chat session for ' + state.currentFile + '.');
    setBusy(busy);
    return;
  }
  ui.empty.hidden = true;
  const msgs = state.session.messages || [];
  if (!msgs.length) {
    showEmpty('Empty session', 'Send a message to get started.');
  } else {
    ui.empty.hidden = true;
  }
  const seenPreview = new Set();
  for (const m of msgs) {
    ui.list.appendChild(renderMsg(m));
    const pv = m.previewMessage;
    if (pv && !seenPreview.has(pv.id)) {
      seenPreview.add(pv.id);
      ui.list.appendChild(renderPreview(pv));
    }
  }
  setBusy(busy);
  scrollToEnd(true);
}

function showEmpty(title, sub) {
  ui.empty.hidden = false;
  ui.empty.innerHTML = '';
  const t = document.createElement('div');
  t.className = 'empty-title';
  t.textContent = title;
  const p = document.createElement('p');
  p.className = 'empty-sub';
  p.textContent = sub;
  const row = document.createElement('div');
  row.className = 'row';
  const btn = document.createElement('button');
  btn.type = 'button';
  btn.className = 'btn btn-accent';
  btn.textContent = 'New session';
  btn.addEventListener('click', () => newSession());
  row.appendChild(btn);
  ui.empty.appendChild(t);
  ui.empty.appendChild(p);
  ui.empty.appendChild(row);
}

function renderMsg(m) {
  const el = document.createElement('div');
  el.className = 'msg msg-' + m.role + (m.id === headId() ? ' msg-head' : '');
  el.dataset.id = m.id;

  const meta = document.createElement('div');
  meta.className = 'msg-meta';
  meta.appendChild(el_text('span', 'msg-role', m.role));
  if (m.role === 'assistant') {
    if (m.model) meta.appendChild(dot(' · ' + m.model));
    if (m.status && m.status !== 'done') meta.appendChild(dot(' · ' + m.status));
    const toks = tokenSummary(m);
    if (toks) meta.appendChild(dot(' · ' + toks));
    if (m.latencyMs) meta.appendChild(dot(' · ' + fmtSecs(m.latencyMs)));
    const lr = latestRevId();
    if (m.promptRevId && lr && m.promptRevId !== lr) {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'chip chip-stale';
      b.textContent = 'stale prompt — diff';
      b.addEventListener('click', () => showInlineDiff(null, m.id, m.promptRevId));
      meta.appendChild(b);
    }
  }
  el.appendChild(meta);

  if (m.role === 'assistant' && m.reasoningText) {
    const det = document.createElement('details');
    det.className = 'reasoning';
    const sum = document.createElement('summary');
    sum.textContent = 'Reasoning (' + m.reasoningText.length + ' chars)';
    const pre = document.createElement('pre');
    applyHighlight(pre, m.reasoningText);
    det.appendChild(sum);
    det.appendChild(pre);
    el.appendChild(det);
  }

  const body = document.createElement('pre');
  body.className = 'msg-text';
  applyHighlight(body, m.text);
  el.appendChild(body);

  if (m.error) {
    const e = document.createElement('div');
    e.className = 'msg-error';
    e.textContent = m.error;
    el.appendChild(e);
  }

  el.appendChild(renderActions(m));
  const nav = renderSiblingNav(m);
  if (nav) el.appendChild(nav);
  return el;
}

function headId() { const s = sess(); return s ? s.headMessageId : null; }

function tokenSummary(m) {
  if (!m.promptTokens && !m.outputTokens) return '';
  return m.promptTokens + '→' + m.outputTokens + ' tok';
}

function dot(t) {
  const s = document.createElement('span');
  s.className = 'dim';
  s.textContent = t;
  return s;
}

function el_text(tag, cls, text) {
  const e = document.createElement(tag);
  e.className = cls;
  e.textContent = text;
  return e;
}

function fmtSecs(ms) {
  if (ms < 1000) return ms + 'ms';
  return (ms / 1000).toFixed(1) + 's';
}

function renderActions(m) {
  const row = document.createElement('div');
  row.className = 'msg-actions';
  if (busy) return row;
  if (m.role === 'user') {
    const b = action('edit & resend', 'Edit this message, branch a new reply chain');
    b.addEventListener('click', () => startEdit(row, m));
    row.appendChild(b);
  } else {
    const b = action('regenerate', 'Generate a preview under the same parent; accept to keep');
    b.addEventListener('click', () => startGenerate({ previewFor: m.id }));
    row.appendChild(b);
  }
  const del = action('✕', 'Delete this message and everything under it');
  del.classList.add('btn-danger');
  del.addEventListener('click', async () => {
    if (!confirm('Delete this message and its subtree?')) return;
    try {
      await fetchJSON('/api/messages/' + encodeURIComponent(m.id), { method: 'DELETE' });
      await loadSession(state.currentSessionId);
    } catch (e) { toast('error', e.message); }
  });
  row.appendChild(del);
  return row;
}

function action(label, title) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn btn-quiet btn-sm';
  b.textContent = label;
  b.title = title;
  return b;
}

function renderSiblingNav(m) {
  const n = m.siblingCount || 0;
  if (n < 2 || !m.siblings || m.siblings.length < 2) return null;
  const nav = document.createElement('div');
  nav.className = 'sibling-nav';
  const prev = document.createElement('button');
  prev.type = 'button';
  prev.className = 'btn btn-quiet btn-sm sib';
  prev.textContent = '‹';
  prev.title = 'previous sibling';
  const idx = document.createElement('span');
  idx.className = 'sib-idx';
  idx.textContent = m.siblingIndex + ' / ' + n;
  const next = document.createElement('button');
  next.type = 'button';
  next.className = 'btn btn-quiet btn-sm sib';
  next.textContent = '›';
  next.title = 'next sibling';
  const step = (delta) => {
    const list = m.siblings;
    const cur = list.findIndex(s => s.id === m.id);
    const nxt = list[cur + delta];
    if (!nxt) return;
    setHeadTo(nxt.id);
  };
  prev.addEventListener('click', () => step(-1));
  next.addEventListener('click', () => step(1));
  prev.disabled = m.siblingIndex <= 1;
  next.disabled = m.siblingIndex >= n;
  nav.appendChild(prev);
  nav.appendChild(idx);
  nav.appendChild(next);
  return nav;
}

async function setHeadTo(messageId) {
  const s = sess();
  if (!s) return;
  try {
    await fetchJSON('/api/sessions/' + encodeURIComponent(s.id) + '/head', {
      method: 'POST',
      body: JSON.stringify({ messageId, toLeaf: true }),
    });
    await loadSession(s.id);
  } catch (e) { toast('error', e.message); }
}

// ---------- preview rendering ----------

function renderPreview(pv) {
  const el = document.createElement('div');
  el.className = 'msg msg-assistant msg-preview';
  el.dataset.id = pv.id;

  const meta = document.createElement('div');
  meta.className = 'msg-meta';
  meta.appendChild(el_text('span', 'msg-role', 'preview'));
  const status = pv.status === 'streaming' ? 'streaming…' : pv.status;
  meta.appendChild(dot(' · ' + status));
  el.appendChild(meta);

  if (pv.reasoningText) {
    const det = document.createElement('details');
    det.className = 'reasoning';
    const sum = document.createElement('summary');
    sum.textContent = 'Reasoning';
    const pre = document.createElement('pre');
    applyHighlight(pre, pv.reasoningText);
    det.appendChild(sum);
    det.appendChild(pre);
    el.appendChild(det);
  }
  const body = document.createElement('pre');
  body.className = 'msg-text';
  applyHighlight(body, pv.text);
  el.appendChild(body);
  if (pv.error) {
    const e = document.createElement('div');
    e.className = 'msg-error';
    e.textContent = pv.error;
    el.appendChild(e);
  }

  const row = document.createElement('div');
  row.className = 'msg-actions';
  if (pv.status === 'done') {
    const acc = action('Accept', 'Make this preview the live reply');
    acc.classList.add('btn-accent');
    acc.addEventListener('click', async () => {
      try {
        await fetchJSON('/api/messages/' + encodeURIComponent(pv.id) + '/accept', { method: 'POST' });
        await loadSession(state.currentSessionId);
      } catch (e) { toast('error', e.message); }
    });
    row.appendChild(acc);
  }
  const drop = action('Discard', 'Delete this preview');
  drop.classList.add('btn-danger');
  drop.addEventListener('click', async () => {
    try {
      await fetchJSON('/api/messages/' + encodeURIComponent(pv.id), { method: 'DELETE' });
      await loadSession(state.currentSessionId);
    } catch (e) { toast('error', e.message); }
  });
  row.appendChild(drop);
  el.appendChild(row);
  return el;
}

// ---------- send + generate ----------

async function send() {
  if (busy) return;
  const text = ui.input.value.trim();
  if (!text) return;
  const s = sess();
  if (!s) { toast('error', 'no session selected'); return; }
  ui.err.textContent = '';
  setBusy(true);
  try {
    if (deps.flush) await deps.flush();
    const body = { text };
    if (s.headMessageId) body.parentId = s.headMessageId;
    const msg = await fetchJSON('/api/sessions/' + encodeURIComponent(s.id) + '/messages', {
      method: 'POST',
      body: JSON.stringify(body),
    });
    ui.input.value = '';
    ui.input.style.height = 'auto';
    inputHl.sync();
    state.session.messages.push(msg);
    render();
    await startGenerate({});
  } catch (e) {
    setBusy(false);
    ui.err.textContent = e.message;
  }
}

async function startGenerate(body) {
  const s = sess();
  if (!s) return;
  setBusy(true);
  ui.err.textContent = '';
  try {
    if (deps.flush) await deps.flush();
  } catch (e) {
    setBusy(false);
    ui.err.textContent = 'could not save prompt file: ' + e.message;
    return;
  }
  controller = new AbortController();
  const stream = startLiveBubble();
  let failed = null;
  await streamSSE('/api/sessions/' + encodeURIComponent(s.id) + '/generate', body, {
    signal: controller.signal,
    onMeta: (d) => stream.init(d.message),
    onDelta: (d) => stream.append(d),
    onError: (d) => { failed = d && d.message; stream.fail(failed); },
  });
  controller = null;
  setBusy(false);
  if (failed) toast('error', failed);
  await loadSession(s.id);
}

// Creates a provisional streaming bubble; returns control functions.
function startLiveBubble() {
  const el = document.createElement('div');
  el.className = 'msg msg-assistant msg-streaming';
  const meta = document.createElement('div');
  meta.className = 'msg-meta';
  meta.appendChild(el_text('span', 'msg-role', 'assistant'));
  meta.appendChild(dot(' · streaming…'));
  const body = document.createElement('pre');
  body.className = 'msg-text';
  const rsum = document.createElement('summary');
  const rpre = document.createElement('pre');
  const det = document.createElement('details');
  det.className = 'reasoning';
  rsum.textContent = 'Reasoning';
  det.appendChild(rsum);
  det.appendChild(rpre);
  det.hidden = true;
  el.appendChild(meta);
  el.appendChild(det);
  el.appendChild(body);
  ui.list.appendChild(el);
  scrollToEnd();
  let bodyText = '';
  let reasonText = '';
  return {
    init() { /* row arrives pre-inserted server-side; nothing extra */ },
    append(d) {
      if (d.kind === 'reasoning') {
        det.hidden = false;
        reasonText += d.text || '';
        applyHighlight(rpre, reasonText);
      } else {
        bodyText += d.text || '';
        applyHighlight(body, bodyText);
      }
      scrollToEnd();
    },
    fail(msg) {
      const e = document.createElement('div');
      e.className = 'msg-error';
      e.textContent = msg || 'stream failed';
      el.appendChild(e);
    },
  };
}

function stop() {
  if (controller) controller.abort();
}

function dropEditHosts() {
  document.querySelectorAll('.edit-box').forEach(n => {
    const h = n.closest('.edit-host');
    if (h._hl) h._hl.detach();
    h.remove();
  });
}

function startEdit(actionsRow, m) {
  if (busy) return;
  dropEditHosts();
  let host = actionsRow.previousElementSibling;
  if (!host || !host.classList.contains('edit-host')) {
    host = document.createElement('div');
    host.className = 'edit-host';
    actionsRow.parentNode.insertBefore(host, actionsRow);
  }
  host.innerHTML = '';
  const ta = document.createElement('textarea');
  ta.className = 'edit-box';
  ta.rows = Math.min(12, Math.max(2, m.text.split('\n').length));
  ta.value = m.text;
  const row = document.createElement('div');
  row.className = 'row row-end';
  const cancel = action('Cancel', '');
  cancel.addEventListener('click', () => { if (host._hl) host._hl.detach(); host.remove(); });
  const go = document.createElement('button');
  go.type = 'button';
  go.className = 'btn btn-accent btn-sm';
  go.textContent = 'Save & resend';
  go.addEventListener('click', () => doResend(m, ta.value));
  row.appendChild(cancel);
  row.appendChild(go);
  host.appendChild(ta);
  host.appendChild(row);
  const h = attachOverlay(ta);   // after insertion: the backdrop needs a parent
  host._hl = h;
  h.sync();
  ta.focus();
  ta.setSelectionRange(ta.value.length, ta.value.length);
}

async function doResend(m, newText) {
  const s = sess();
  if (!s) return;
  if (newText.trim() === m.text) {
    dropEditHosts();
    return;
  }
  setBusy(true);
  ui.err.textContent = '';
  controller = new AbortController();
  const prog = document.createElement('div');
  prog.className = 'resend-progress banner banner-info';
  prog.textContent = 'Resending…';
  ui.list.appendChild(prog);
  scrollToEnd();
  let failed = null;
  await streamSSE('/api/sessions/' + encodeURIComponent(s.id) + '/resend',
    { messageId: m.id, text: newText }, {
    signal: controller.signal,
    onMeta: () => { prog.textContent = 'Regenerating replies…'; scrollToEnd(); },
    onDone: () => { scrollToEnd(); },
    onError: (d) => { failed = d && d.message; },
  });
  controller = null;
  setBusy(false);
  if (failed) toast('error', failed);
  await loadSession(s.id);
}

// ---------- misc ----------

function setBusy(v) {
  busy = v;
  state.streaming = v;
  if (ui) ui.btnSend.disabled = v;
  if (ui) { ui.modelSel.disabled = v; ui.btnModelRefresh.disabled = v; }
  const sb = document.getElementById('btn-stop');
  if (sb) sb.disabled = !v;
}

function scrollToEnd(force) {
  const list = ui.list;
  const nearBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 120;
  if (force || nearBottom) list.scrollTop = list.scrollHeight;
}

export { stop };
