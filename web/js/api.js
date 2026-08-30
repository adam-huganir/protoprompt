// HTTP helpers: fetchJSON + streamSSE (fetch POST, reader + TextDecoder, frames split on \n\n).

export async function fetchJSON(path, init) {
  let res;
  try {
    res = await fetch(path, init);
  } catch (e) {
    throw new Error('network error: ' + (e && e.message ? e.message : String(e)));
  }
  if (!res.ok) {
    let msg = res.statusText || ('HTTP ' + res.status);
    try {
      const j = await res.json();
      if (j && typeof j.error === 'string' && j.error) msg = j.error;
    } catch (/* parse failure falls back to status text */) { /* ignore */ }
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  if (res.status === 204) return null;
  try {
    return await res.json();
  } catch (e) {
    throw new Error(res.statusText || 'invalid JSON response');
  }
}

// Split raw SSE frame text into { event, data } (data parsed as JSON when possible).
function parseFrame(buf) {
  let event = 'message';
  const dataLines = [];
  for (const line of buf.split(/\r?\n/)) {
    if (line.startsWith(':')) continue; // SSE comment
    if (line.startsWith('event:')) {
      event = line.slice(6).trim();
    } else if (line.startsWith('data:')) {
      let v = line.slice(5);
      if (v.startsWith(' ')) v = v.slice(1);
      dataLines.push(v);
    }
  }
  if (!dataLines.length) return { event, data: null };
  let data = null;
  try { data = JSON.parse(dataLines.join('\n')); } catch (/* keep null */) { /* ignore */ }
  return { event, data };
}

// Locate the next blank-line frame boundary; returns { i, len } or null.
function findSep(s) {
  const m = /\r?\n\r?\n/.exec(s);
  return m ? { i: m.index, len: m[0].length } : null;
}
// Streams a POST as server-sent events. Callbacks: onMeta, onDelta, onDone, onError,
// onComplete (terminal `event: complete` frame on resend). `signal` aborts the fetch.
// Returns once the stream is fully consumed, aborted, or failed.
export async function streamSSE(url, body, { onMeta, onDelta, onDone, onError, onComplete, signal } = {}) {
  const call = (fn, ...args) => { if (typeof fn === 'function') return fn(...args); };
  let res;
  try {
    res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Accept': 'text/event-stream' },
      body: JSON.stringify(body == null ? {} : body),
      signal,
    });
  } catch (e) {
    if (e && e.name === 'AbortError') return;
    call(onError, { message: 'network error: ' + (e && e.message ? e.message : String(e)) });
    return;
  }
  if (!res.ok) {
    let msg = res.statusText || ('HTTP ' + res.status);
    try {
      const j = await res.json();
      if (j && typeof j.error === 'string' && j.error) msg = j.error;
    } catch (/* fall back to status text */) { /* ignore */ }
    call(onError, { message: msg, status: res.status });
    return;
  }
  if (!res.body) {
    call(onError, { message: 'no response body' });
    return;
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let doneEvent = false;
  const handle = (evName, data) => {
    if (evName === 'meta') { doneEvent = false; call(onMeta, data || {}); }
    else if (evName === 'delta') call(onDelta, data || {});
    else if (evName === 'done') { doneEvent = true; call(onDone, data || {}); }
    else if (evName === 'complete') { doneEvent = true; call(onComplete, data || {}); }
    else if (evName === 'error') { doneEvent = true; call(onError, data || { message: 'stream error' }); }
  };
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let sep;
      while ((sep = findSep(buf)) !== null) {
        const raw = buf.slice(0, sep.i);
        buf = buf.slice(sep.i + sep.len);
        const { event, data } = parseFrame(raw);
        handle(event, data);
      }
    }
    buf += decoder.decode();
    const rest = buf.replace(/\r?\n?$/, '');
    if (rest.trim()) {
      const { event, data } = parseFrame(rest);
      handle(event, data);
    }
  } catch (e) {
    if (!(e && e.name === 'AbortError')) call(onError, { message: 'stream interrupted: ' + (e && e.message ? e.message : String(e)) });
    return;
  }
  // Connection closed with no terminal event → surface as error unless we already finished.
  if (!doneEvent) call(onError, { message: 'connection closed before completion' });
}
