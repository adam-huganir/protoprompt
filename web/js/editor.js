// Editor pane: file list, autosave, rev dropdown, diff render, disk-change banner.
import { fetchJSON } from './api.js';
import { state, emit } from './state.js';

const $ = (sel, root) => (root || document).querySelector(sel);

let ui = null;              // DOM refs
let hooks = null;           // { flush(fn) }
let currentPath = null;     // path of open file, or null
let fileData = null;        // GET /api/files/{name} payload
let lastText = '';          // last saved buffer contents
let revMode = false;        // viewing a past revision (textarea readonly)
let dirtyTimer = 0;
let savePromise = null;
let lastRevText = '';       // text of selected rev (for raw view mode)

export function mountEditor(root, h) {
  hooks = h || {};
  ui = {
    fileSelect: $('#file-select', root),
    btnNew: $('#btn-new-file', root),
    revSelect: $('#rev-select', root),
    btnDiff: $('#btn-diff-toggle', root),
    editor: $('#editor', root),
    revView: $('#rev-view', root),
    diffView: $('#diff-view', root),
    edMeta: $('#ed-meta', root),
    saveInd: $('#save-ind', root),
    btnReload: $('#btn-reload', root),
    empty: $('#editor-empty', root),
    newFileInput: $('#new-file-input', root),
    btnNew2: $('#btn-new-file-2', root),
    overlay: $('#editor-diff-overlay', root),
    overlayBody: $('#editor-diff', root),
    overlayClose: $('#diff-close', root),
  };

  ui.fileSelect.addEventListener('change', () => { if (ui.fileSelect.value) openFile(ui.fileSelect.value); });
  ui.btnNew.addEventListener('click', () => promptNewFile());
  ui.btnNew2.addEventListener('click', () => createFile(ui.newFileInput.value));
  ui.newFileInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); createFile(ui.newFileInput.value); }
  });
  ui.revSelect.addEventListener('change', () => selectRev(ui.revSelect.value));
  ui.btnDiff.addEventListener('click', toggleDiff);
  ui.editor.addEventListener('input', onInput);
  ui.btnReload.addEventListener('click', reloadFromDisk);
  ui.overlayClose.addEventListener('click', closeOverlay);

  // Ctrl+S flushes pending edits anywhere in the app.
  document.addEventListener('keydown', (e) => {
    if ((e.ctrlKey || e.metaKey) && !e.altKey && String(e.key).toLowerCase() === 's') {
      e.preventDefault();
      flush();
    }
  });

  return {
    refresh,
    open: openFile,
    flush,
    currentFile: () => currentPath,
    getRevInfo: () => ({
      path: currentPath,
      revs: fileData ? fileData.revs : [],
      latestRevId: fileData ? fileData.latestRevId : null,
    }),
  };
}

async function refresh() {
  try {
    state.files = await fetchJSON('/api/files');
  } catch (e) {
    toastError('could not load files: ' + e.message);
    state.files = [];
  }
  renderFileList();
  if (!state.files.length) {
    showEmpty();
    return;
  }
  const still = currentPath && state.files.some(f => f.path === currentPath);
  if (still) {
    await openFile(currentPath);
  } else {
    await openFile(state.files[0].path);
  }
  if (hooks && hooks.onCurrentFileChanged) hooks.onCurrentFileChanged(currentPath);
}

function renderFileList() {
  const sel = ui.fileSelect;
  sel.innerHTML = '';
  if (!state.files.length) {
    const o = document.createElement('option');
    o.textContent = '— no files —';
    sel.appendChild(o);
    return;
  }
  for (const f of state.files) {
    const o = document.createElement('option');
    o.value = f.path;
    o.textContent = f.name;
    if (f.path === currentPath) o.selected = true;
    sel.appendChild(o);
  }
}

async function openFile(path) {
  // best-effort flush before switching buffers
  try { await flush(); } catch (/* already surfaced */) { /* ignore */ }
  currentPath = path;
  state.currentFile = path;
  revMode = false;
  ui.revSelect.value = '';
  ui.btnDiff.disabled = true;
  ui.revView.hidden = true;
  ui.diffView.hidden = true;
  ui.editor.hidden = false;
  ui.editor.readOnly = false;
  ui.overlay.hidden = true;
  try {
    const name = encodeURIComponent(path);
    fileData = await fetchJSON('/api/files/' + name);
  } catch (e) {
    fileData = null;
    toastError('could not open ' + path + ': ' + e.message);
    return;
  }
  lastText = fileData.text;
  ui.editor.value = fileData.text;
  setDirty(false);
  renderRevs();
  renderMeta();
  ui.btnReload.hidden = !fileData.diskChangedSinceLatestRev;
  ui.empty.hidden = true;
  renderFileList();
  emit();
}

function renderRevs() {
  const sel = ui.revSelect;
  sel.innerHTML = '';
  const head = document.createElement('option');
  head.value = '';
  head.textContent = 'current';
  sel.appendChild(head);
  const revs = fileData ? fileData.revs : [];
  revs.forEach((rv, i) => {
    const o = document.createElement('option');
    o.value = rv.id;
    o.textContent = '#' + (i + 1) + ' · ' + fmtHM(rv.savedAt);
    sel.appendChild(o);
  });
}

async function selectRev(id) {
  if (!id) {
    // back to editor buffer
    revMode = false;
    ui.editor.readOnly = false;
    ui.editor.hidden = false;
    ui.revView.hidden = true;
    ui.diffView.hidden = true;
    ui.btnDiff.disabled = true;
    lastRevText = '';
    return;
  }
  revMode = true;
  ui.btnDiff.disabled = false;
  ui.editor.hidden = true;
  ui.diffView.hidden = true;
  try {
    const rv = await fetchJSON('/api/revs/' + encodeURIComponent(id));
    lastRevText = rv.text;
    ui.revView.textContent = rv.text;
    ui.revView.hidden = false;
  } catch (e) {
    toastError('revision load failed: ' + e.message);
  }
}

async function toggleDiff() {
  const id = ui.revSelect.value;
  if (!id) return;
  const on = !ui.btnDiff.classList.contains('active');
  if (!on) {
    ui.btnDiff.classList.remove('active');
    ui.diffView.hidden = true;
    ui.revView.hidden = false;
    return;
  }
  ui.btnDiff.classList.add('active');
  ui.revView.hidden = true;
  ui.diffView.hidden = false;
  ui.diffView.textContent = 'Loading diff…';
  try {
    const path = encodeURIComponent(currentPath);
    const diff = await fetchJSON('/api/files/' + path + '/diff?from=' + encodeURIComponent(id));
    renderDiff(ui.diffView, diff, 'diff: rev → current');
  } catch (e) {
    ui.diffView.textContent = 'diff failed: ' + e.message;
  }
}

// Render diff hunks; head = descriptive label line. Diff lines have t=ctx|add|del;
// "n" (line number in from-text) only present on ctx lines per API contract.
export function renderDiff(container, diff, head) {
  container.innerHTML = '';
  if (head) {
    const h = document.createElement('div');
    h.className = 'diff-head';
    h.textContent = head;
    container.appendChild(h);
  }
  const hunks = (diff && diff.hunks) || [];
  if (!hunks.length) {
    const d = document.createElement('div');
    d.className = 'diff-empty';
    d.textContent = 'no changes';
    container.appendChild(d);
    return;
  }
  for (const hunk of hunks) {
    const pre = document.createElement('pre');
    pre.className = 'diff-hunk';
    const info = document.createElement('div');
    info.className = 'diff-info';
    info.textContent = '@@ line ' + hunk.start + ' · ' + hunk.count + ' lines';
    pre.appendChild(info);
    let n = hunk.start || 1;
    for (const ln of hunk.lines || []) {
      const row = document.createElement('div');
      row.className = 'dl dl-' + ln.t;
      row.textContent = (ln.t === 'add' ? '+' : ln.t === 'del' ? '-' : ' ') + (ln.text == null ? '' : ln.text);
      pre.appendChild(row);
      if (typeof ln.n === 'number') {
        const num = document.createElement('span');
        num.className = 'lnum';
        num.textContent = String(ln.n);
        row.insertBefore(num, row.firstChild);
      } else if (ln.t === 'add') {
        const num = document.createElement('span');
        num.className = 'lnum';
        num.textContent = String(n);
        row.insertBefore(num, row.firstChild);
      }
      if (ln.t !== 'del') n++;
    }
    container.appendChild(pre);
  }
}

async function promptNewFile() {
  const name = prompt('New prompt file name (must end in .md):');
  if (!name) return;
  await createFile(name);
}

async function createFile(rawName) {
  let name = (rawName || '').trim();
  if (!name) return;
  if (!/\.md$/i.test(name)) name += '.md';
  try {
    await fetchJSON('/api/files', { method: 'POST', body: JSON.stringify({ path: name }) });
  } catch (e) {
    toastError(e.message);
    return;
  }
  ui.newFileInput.value = '';
  try {
    state.files = await fetchJSON('/api/files');
  } catch (/* keep existing */) { /* ignore */ }
  renderFileList();
  await openFile(name);
  if (hooks && hooks.onCurrentFileChanged) hooks.onCurrentFileChanged(currentPath);
}

function onInput() {
  if (revMode) return;
  setDirty(true);
  renderMeta();
  clearTimeout(dirtyTimer);
  dirtyTimer = setTimeout(() => { save().catch(() => {}); }, 800);
}

function setDirty(v) {
  state.dirty = v;
  renderMeta();
  emit();
}

// Persist buffer. Returns a promise that resolves once the server has the text.
async function save() {
  if (!currentPath) return;
  if (savePromise) return savePromise;
  savePromise = (async () => {
    try {
      do {
        const text = ui.editor.value;
        if (!state.dirty) break;
        state.saving = true;
        renderSaving(true);
        try {
          await fetchJSON('/api/files/' + encodeURIComponent(currentPath) + '/save', {
            method: 'POST',
            body: JSON.stringify({ text }),
          });
          lastText = text;
          if (ui.editor.value === text) setDirty(false);
          const name = encodeURIComponent(currentPath);
          fileData = await fetchJSON('/api/files/' + name);
          lastText = ui.editor.value === lastText ? fileData.text : lastText;
          ui.btnReload.hidden = !fileData.diskChangedSinceLatestRev;
          renderRevs();
          renderMeta();
          const f = state.files.find(x => x.path === currentPath);
          if (f) { f.revCount = (fileData.revs || []).length; f.latestSavedAt = Date.now(); }
        } finally {
          state.saving = false;
          renderSaving(false);
        }
      } while (ui.editor.value !== lastText);
    } finally {
      savePromise = null;
    }
  })();
  return savePromise;
}

// Flush pending edits — awaited before any generate/resend.
async function flush() {
  clearTimeout(dirtyTimer);
  if (savePromise) await savePromise;
  if (state.dirty && currentPath) await save();
}

async function reloadFromDisk() {
  if (!currentPath) return;
  try {
    await openFile(currentPath);
  } catch (/* surfaced */) { /* ignore */ }
}

function closeOverlay() {
  ui.overlay.hidden = true;
  ui.overlay._msgId = null;
}

// Inline diff opened from the chat side (stale prompt badge).
export async function showInlineDiff(h, msgId, promptRevId) {
  if (!currentPath) { toastError('no prompt file open'); return; }
  ui.overlay.hidden = false;
  ui.overlay._msgId = msgId;
  ui.overlayBody.textContent = 'Loading diff…';
  try {
    const path = encodeURIComponent(currentPath);
    const diff = await fetchJSON('/api/files/' + path + '/diff?from=' + encodeURIComponent(promptRevId));
    renderDiff(ui.overlayBody, diff, 'diff: reply prompt rev → current file');
  } catch (e) {
    ui.overlayBody.textContent = 'diff failed: ' + e.message;
  }
}

function renderMeta() {
  if (!ui || !ui.edMeta) return;
  const n = ui.editor.value.length;
  ui.edMeta.textContent = n + ' chars · ≈' + Math.ceil(n / 4) + ' tokens' + (state.dirty ? ' · unsaved' : '');
}

function renderSaving(on) {
  ui.saveInd.textContent = on ? 'saving…' : '';
}

function toastError(msg) {
  document.dispatchEvent(new CustomEvent('cv:toast', { detail: { kind: 'error', msg } }));
}

export function fmtHM(ts) {
  if (ts == null) return '';
  const d = new Date(ts);
  const p = (v) => String(v).padStart(2, '0');
  return p(d.getHours()) + ':' + p(d.getMinutes());
}
