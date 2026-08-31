// App boot: config load, editor+chat mounting, session picker, mode
// toggle, pane splitter, toasts.
import { fetchJSON } from './api.js';
import { state } from './state.js';
import { mountEditor } from './editor.js';
import { mountChat, stop } from './chat.js';

const $ = (sel) => document.querySelector(sel);

function toast(kind, msg) {
  const host = $('#toasts');
  const el = document.createElement('div');
  el.className = 'toast toast-' + kind;
  el.textContent = msg;
  host.appendChild(el);
  setTimeout(() => el.classList.add('toast-out'), 4200);
  setTimeout(() => el.remove(), 5000);
}

document.addEventListener('cv:toast', (e) => {
  const { kind, msg } = e.detail || {};
  toast(kind || 'error', msg || '');
});

const chat = mountChat($('#chat-pane'), {
  toast,
  getRevInfo: () => editorApi.getRevInfo(),
  flush: () => editorApi.flush(),
  openFile: (path) => editorApi.open(path),
});
const editorApi = mountEditor($('#editor-pane'), {
  onCurrentFileChanged: (path) => { onFileChanged(path); },
});

// ---------- config ----------

async function boot() {
  try {
    state.config = await fetchJSON('/api/config');
  } catch (e) {
    toast('error', 'config load failed: ' + e.message);
  }
  // Config carries the fallback model list; the picker may have been built
  // before it arrived (or while the upstream list fetch failed).
  chat.refreshModels();
  buildHeader();
  setupModes();
  setupSplitter();
  setupChatTabs();
  try {
    await editorApi.refresh();
  } catch { /* ignore */ }
  if (state.currentFile) await onFileChanged(state.currentFile);
}

// ---------- header: sessions ----------

function buildHeader() {
  const slot = $('#hdr-chat-slot');
  slot.innerHTML = '';

  const stopBtn = document.createElement('button');
  stopBtn.id = 'btn-stop';
  stopBtn.type = 'button';
  stopBtn.className = 'btn btn-danger btn-sm';
  stopBtn.textContent = 'Stop';
  stopBtn.disabled = true;
  stopBtn.title = 'Stop the running generation';
  stopBtn.addEventListener('click', () => stop());
  slot.appendChild(stopBtn);

  const sel = document.createElement('select');
  sel.id = 'session-select';
  sel.setAttribute('aria-label', 'Session');
  sel.addEventListener('change', () => {
    if (sel.value) chat.loadSession(sel.value);
  });
  slot.appendChild(sel);

  slot.appendChild(headerBtn('＋', 'New session', () => chat.newSession()));
  slot.appendChild(headerBtn('✎', 'Rename session', () => chat.renameSession()));
  slot.appendChild(headerBtn('🗑', 'Delete session', () => chat.deleteSession()));

  document.addEventListener('cv:sessionsChanged', refreshSessions);
}

function headerBtn(label, title, fn) {
  const b = document.createElement('button');
  b.type = 'button';
  b.className = 'btn btn-quiet btn-sm';
  b.textContent = label;
  b.title = title;
  b.addEventListener('click', fn);
  return b;
}

let refreshSeq = 0;
async function refreshSessions() {
  const sel = $('#session-select');
  if (!sel) return;
  const my = ++refreshSeq;
  let list = [];
  if (!state.currentFile) {
    list = [];
  } else {
    try {
      list = await fetchJSON('/api/sessions?file=' + encodeURIComponent(state.currentFile) + '&kind=' + encodeURIComponent(state.activeKind));
    } catch (e) {
      toast('error', 'sessions load failed: ' + e.message);
      list = [];
    }
  }
  if (my !== refreshSeq) return; // a newer refresh superseded this one
  state.sessions = list;
  sel.innerHTML = '';
  if (!state.sessions.length) {
    const o = document.createElement('option');
    o.value = '';
    o.textContent = '— no sessions —';
    sel.appendChild(o);
    if (state.currentSessionId) await chat.loadSession(null);
    return;
  }
  for (const s of state.sessions) {
    const o = document.createElement('option');
    o.value = s.id;
    o.textContent = (s.title || 'session') + ' · ' + fmtDT(s.updatedAt || s.createdAt);
    if (s.id === state.currentSessionId) o.selected = true;
    sel.appendChild(o);
  }
  if (!state.sessions.some(s => s.id === state.currentSessionId)) {
    // Current session is gone (deleted or file switch): load the newest.
    await chat.loadSession(state.sessions[0].id);
    sel.value = state.currentSessionId;
  }
}

function fmtDT(ms) {
  if (!ms) return '';
  const d = new Date(ms);
  const p = (v) => String(v).padStart(2, '0');
  return p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + p(d.getHours()) + ':' + p(d.getMinutes());
}

async function onFileChanged(path) {
  await refreshSessions();
  const slot = $('#hdr-file-slot');
  if (slot) slot.textContent = path || '';
}

// ---------- chat tabs ----------

function setupChatTabs() {
  const saved = localStorage.getItem('cv.chatTab');
  if (saved === 'chat' || saved === 'helper') state.activeKind = saved;
  const tabs = document.querySelectorAll('#chat-tabs button');
  for (const b of tabs) {
    if (b.dataset.kind === state.activeKind) b.classList.add('active'); else b.classList.remove('active');
    b.addEventListener('click', () => {
      state.activeKind = b.dataset.kind;
      localStorage.setItem('cv.chatTab', state.activeKind);
      for (const x of tabs) x.classList.toggle('active', x === b);
      refreshSessions();
    });
  }
}

// ---------- mode toggle + splitter ----------

function setupModes() {
  const seg = $('#mode-seg');
  const main = $('#main');
  const set = (mode) => {
    main.dataset.mode = mode;
    for (const b of seg.querySelectorAll('button')) {
      b.classList.toggle('active', b.dataset.mode === mode);
    }
    try { localStorage.setItem('cv.mode', mode); } catch { /* ignore */ }
  };
  seg.addEventListener('click', (e) => {
    const b = e.target.closest('button[data-mode]');
    if (b) set(b.dataset.mode);
  });
  let mode = 'split';
  try { mode = localStorage.getItem('cv.mode') || 'split'; } catch { /* ignore */ }
  set(mode);
}

function setupSplitter() {
  const div = $('#divider');
  const pane = $('#editor-pane');
  const main = $('#main');
  let startX = 0;
  let startW = 0;
  div.addEventListener('pointerdown', (e) => {
    if (main.dataset.mode !== 'split') return;
    startX = e.clientX;
    startW = pane.getBoundingClientRect().width;
    div.setPointerCapture(e.pointerId);
    div.classList.add('dragging');
  });
  div.addEventListener('pointermove', (e) => {
    if (!div.classList.contains('dragging')) return;
    const w = Math.max(240, Math.min(window.innerWidth - 360, startW + (e.clientX - startX)));
    pane.style.flexBasis = w + 'px';
  });
  div.addEventListener('pointerup', (e) => {
    div.classList.remove('dragging');
    try { div.releasePointerCapture(e.pointerId); } catch { /* ignore */ }
  });
  div.addEventListener('dblclick', () => {
    pane.style.flexBasis = '';
  });
}

boot();
