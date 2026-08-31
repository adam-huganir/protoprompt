// Markdown SOURCE highlighting for the prompt editor, chat input, edit box and
// revision view, plus the streaming-bubble fallback before server html lands.
// Chat bubbles themselves render server-provided `html` (.msg-rendered), not this
// source view. Tokenizes raw markdown into <span class="md-*"> wrappers: markers
// stay visible, whitespace is preserved (source view, not rendered view). The
// tokenizer is DOM-free so it runs under node smoke tests; only attachOverlay
// touches the browser. No nested-quantifier regexes anywhere — line and inline
// passes are linear scans, so unterminated constructs terminate safely.

const ESCAPABLE = '!\"#$%&\'()*+,-./:;<=>?@[\\]^_`{|}~'; // ASCII punctuation for backslash escapes

function esc(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// One span per token. Empty text produces nothing so no empty spans are emitted.
function span(cls, text) {
  if (text === '') return '';
  return '<span class="' + cls + '">' + esc(text) + '</span>';
}

const isWordChar = (c) => (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9');
const isWs = (c) => c === ' ' || c === '\t';

// Index of the first run of exactly `len` chars `ch` at or after `from`, else -1.
function findRun(s, from, n, ch, len) {
  let k = from;
  while (k < n) {
    if (s[k] !== ch) { k++; continue; }
    let m = k + 1;
    while (m < n && s[m] === ch) m++;
    if (m - k === len) return k;
    k = m;
  }
  return -1;
}

// Index of the ']' that closes a link opened before `from` — it must be
// immediately followed by '('. A bare ']' first means the opener is not a link.
function findLinkClose(s, from, n) {
  let k = from;
  while (k < n) {
    if (s[k] !== ']') { k++; continue; }
    return s[k + 1] === '(' ? k : -1;
  }
  return -1;
}

// Inline tokens for one line. Code spans are never recursed into, and unmatched
// markers (typical mid-stream) stay plain text — they never swallow the rest of
// the line.
function inlineHtml(s) {
  let out = '';
  let i = 0;
  const n = s.length;
  while (i < n) {
    const c = s[i];

    // Code span: `x` or ``x`` — closing run must be exactly the same length.
    if (c === '`') {
      let j = i + 1;
      while (j < n && s[j] === '`') j++;
      const len = j - i;
      const close = findRun(s, j, n, '`', len);
      if (close >= 0) {
        out += span('md-inline-code', s.slice(i, close + len));
        i = close + len;
      } else {
        out += esc(s.slice(i, j)); // unterminated: plain backticks, keep scanning after the run
        i = j;
      }
      continue;
    }

    // Backslash escape: \* etc.
    if (c === '\\' && i + 1 < n && ESCAPABLE.indexOf(s[i + 1]) >= 0) {
      out += span('md-punct', s.slice(i, i + 2));
      i += 2;
      continue;
    }

    // Strikethrough: ~~x~~
    if (c === '~' && s[i + 1] === '~') {
      const close = findRun(s, i + 2, n, '~', 2);
      if (close >= 0) {
        out += span('md-strike', s.slice(i, close + 2));
        i = close + 2;
      } else {
        out += esc('~~'); // unterminated: plain
        i += 2;
      }
      continue;
    }

    // Strong / boldem / em: **x** / ***x*** / *x* — strongest first.
    if (c === '*') {
      let j = i + 1;
      while (j < n && s[j] === '*') j++;
      const openLen = j - i;
      let close = -1, use = 0;
      if (openLen >= 3) { close = findRun(s, j, n, '*', 3); if (close >= 0) use = 3; }
      if (use === 0 && openLen >= 2) { close = findRun(s, j, n, '*', 2); if (close >= 0) use = 2; }
      if (use === 0) {
        close = findRun(s, j, n, '*', 1);
        if (close >= 0) {
          const content = s.slice(j, close);
          // Em: content must not start or end with a space ("a * b * c" stays plain).
          if (!isWs(content[0]) && !isWs(content[content.length - 1])) use = 1;
        }
      }
      if (use > 0) {
        out += span(use === 3 ? 'md-boldem' : use === 2 ? 'md-strong' : 'md-em', s.slice(i, close + use));
        i = close + use;
      } else {
        out += esc(s.slice(i, j)); // unmatched: plain asterisks
        i = j;
      }
      continue;
    }

    // Strong / em with underscores: __x__ / _x_ — must not fire inside a word.
    if (c === '_') {
      let j = i + 1;
      while (j < n && s[j] === '_') j++;
      const openLen = j - i;
      let close = -1;
      const leftOk = i === 0 || !isWordChar(s[i - 1]);
      if (leftOk) {
        close = findRun(s, j, n, '_', openLen);
        if (close >= 0 && (close + openLen < n && isWordChar(s[close + openLen]))) close = -1;
      }
      if (close >= 0) {
        out += span(openLen === 1 ? 'md-em' : 'md-strong', s.slice(i, close + openLen));
        i = close + openLen;
      } else {
        out += esc(s.slice(i, j)); // unmatched or intra-word: plain underscores
        i = j;
      }
      continue;
    }

    // Image: ![alt](url)
    if (c === '!' && s[i + 1] === '[') {
      const close = findLinkClose(s, i + 2, n);
      const e = close >= 0 ? s.indexOf(')', close + 2) : -1;
      if (e >= 0) {
        out += span('md-punct', '![') + span('md-link-text', s.slice(i + 2, close)) +
          span('md-punct', '](') + span('md-url', s.slice(close + 2, e)) + span('md-punct', ')');
        i = e + 1;
        continue;
      }
      out += esc('!');
      i++;
      continue;
    }

    // Link: [text](url)
    if (c === '[') {
      const close = findLinkClose(s, i + 1, n);
      const e = close >= 0 ? s.indexOf(')', close + 2) : -1;
      if (e >= 0) {
        out += span('md-punct', '[') + span('md-link-text', s.slice(i + 1, close)) +
          span('md-punct', '](') + span('md-url', s.slice(close + 2, e)) + span('md-punct', ')');
        i = e + 1;
        continue;
      }
      out += esc('['); // not a link: plain bracket
      i++;
      continue;
    }

    // Autolink: <https://...> — angle markers are punctuation, the URL is md-url.
    if (c === '<' && (s.startsWith('http://', i + 1) || s.startsWith('https://', i + 1))) {
      let e = i + 1;
      while (e < n && !isWs(s[e]) && s[e] !== '>') e++;
      if (e < n && s[e] === '>') {
        out += span('md-punct', '<') + span('md-url', s.slice(i + 1, e)) + span('md-punct', '>');
        i = e + 1;
        continue;
      }
    }

    // Bare http(s) URL.
    if (c === 'h' && (s.startsWith('http://', i) || s.startsWith('https://', i))) {
      let e = i;
      while (e < n && !isWs(s[e]) && '()<>[]'.indexOf(s[e]) < 0) e++;
      while (e > i + 1 && '.,;:!?'.indexOf(s[e - 1]) >= 0) e--; // trim trailing sentence punctuation
      out += span('md-url', s.slice(i, e));
      i = e;
      continue;
    }

    out += esc(c);
    i++;
  }
  return out;
}

export function highlightToHtml(text) {
  if (text === '') return '';
  const lines = text.split('\n');
  const out = new Array(lines.length);
  let fence = null;     // { ch, len } while inside a fenced code block
  let front = false;    // inside YAML front matter (first line was exactly '---')
  let prevKind = 'blank'; // blank | text | heading | other — drives setext detection

  for (let li = 0; li < lines.length; li++) {
    const raw = lines[li];
    const t = raw.replace(/[ \t\r]+$/, ''); // classification copy; spans always slice `raw`

    // Fenced code (state). Closing fence: same char, length >= opening, indent <= 3.
    if (fence) {
      let sp = 0;
      while (sp < 3 && t[sp] === ' ') sp++;
      let k = sp;
      while (k < t.length && t[k] === fence.ch) k++;
      if (t[sp] === fence.ch && k - sp >= fence.len) {
        out[li] = span('md-fence', raw);
        fence = null;
      } else {
        out[li] = raw === '' ? '' : span('md-code', raw);
      }
      prevKind = 'other';
      continue;
    }

    // Front matter (state): through the next --- / ... line.
    if (front) {
      if (t === '---' || t === '...') {
        out[li] = span('md-front', raw);
        front = false;
        prevKind = 'other';
      } else {
        const m = /^([A-Za-z0-9_.-]+):/.exec(raw);
        out[li] = m
          ? span('md-front-key', raw.slice(0, m[0].length)) + span('md-front', raw.slice(m[0].length))
          : (raw === '' ? '' : span('md-front', raw));
        prevKind = 'text';
      }
      continue;
    }

    // Fence open: ``` or ~~~ plus optional info string.
    const fm = /^ {0,3}([`~]{3,})/.exec(t);
    if (fm) {
      fence = { ch: fm[1][0], len: fm[1].length };
      out[li] = span('md-fence', raw);
      prevKind = 'other';
      continue;
    }

    // Front matter open: only when the very first line is exactly '---'.
    if (li === 0 && t === '---') {
      front = true;
      out[li] = span('md-front', raw);
      prevKind = 'other';
      continue;
    }

    // ATX heading: #..######, then end-of-line or whitespace.
    {
      let sp = 0;
      while (sp < 3 && t[sp] === ' ') sp++;
      if (t[sp] === '#') {
        let h2 = sp + 1;
        while (h2 < t.length && t[h2] === '#') h2++;
        const count = h2 - sp;
        if (count <= 6 && (h2 === t.length || isWs(t[h2]))) {
          out[li] = esc(raw.slice(0, sp)) + span('md-hash', raw.slice(sp, h2)) + span('md-h', raw.slice(h2));
          prevKind = 'heading';
          continue;
        }
      }
    }

    // Table separator row: |---|:--| (at least one pipe, cells of optional colons + dashes).
    if (t.includes('|') && /^\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)+\|?$/.test(t)) {
      out[li] = span('md-table-sep', raw);
      prevKind = 'other';
      continue;
    }

    // Setext heading: === / --- underline below a non-blank, non-heading line.
    if (prevKind === 'text' && /^ {0,3}(-+|=+)$/.test(t)) {
      out[li] = span('md-h', raw);
      out[li - 1] = span('md-h', lines[li - 1]);
      prevKind = 'heading';
      continue;
    }

    // Thematic break: --- / *** / ___ alone on a line.
    if (/^ {0,3}(-{3,}|_{3,}|\*{3,})$/.test(t)) {
      out[li] = span('md-hr', raw);
      prevKind = 'other';
      continue;
    }

    // Blockquote: > / >> prefix and the quoted line's remaining text all md-quote.
    {
      let sp = 0;
      while (sp < 3 && t[sp] === ' ') sp++;
      if (t[sp] === '>') {
        out[li] = span('md-quote', raw);
        prevKind = 'text';
        continue;
      }
    }

    // List item: - / * / + / 1. / 1) marker, optional [ ] / [x] task checkbox.
    {
      let sp = 0;
      while (sp < 3 && t[sp] === ' ') sp++;
      const c = t[sp];
      let mLen = 0;
      if (c === '-' || c === '*' || c === '+') mLen = 1;
      else if (c >= '0' && c <= '9') {
        let d = sp + 1;
        while (d < t.length && t[d] >= '0' && t[d] <= '9') d++;
        if (t[d] === '.' || t[d] === ')') mLen = d - sp + 1;
      }
      if (mLen > 0) {
        const after = sp + mLen;
        if (after >= t.length || isWs(t[after])) {
          let html = esc(raw.slice(0, sp)) + span('md-list', raw.slice(sp, after));
          const rest = raw.slice(after);
          const wsM = /^[ \t]+/.exec(rest);
          if (wsM) {
            const cbm = /^\[[ xX]\](?=[ \t]|$)/.exec(rest.slice(wsM[0].length));
            if (cbm) {
              html += esc(wsM[0]) + span('md-check', cbm[0]);
              html += inlineHtml(rest.slice(wsM[0].length + cbm[0].length));
            } else {
              html += inlineHtml(rest);
            }
          }
          out[li] = html;
          prevKind = 'text';
          continue;
        }
      }
    }

    // Plain line with inline tokens.
    out[li] = inlineHtml(raw);
    prevKind = raw.trim() === '' ? 'blank' : 'text';
  }

  return out.join('\n');
}

export function applyHighlight(el, text) {
  el.innerHTML = highlightToHtml(text);
}

// ---------- textarea overlay ----------
// Transparent-text-over-colored-backdrop highlighting. The backdrop <pre> is a
// sibling of the textarea (it never wraps it), positioned and styled to match
// the textarea exactly so wrapped lines line up with the real text.

const overlays = new WeakMap(); // textarea -> handle (idempotent attach)
const METRIC_PROPS = [
  'fontFamily', 'fontSize', 'fontStyle', 'fontWeight', 'lineHeight', 'letterSpacing',
  'wordSpacing', 'textIndent', 'tabSize', 'direction', 'textAlign',
  'padding', 'borderTopWidth', 'borderRightWidth', 'borderBottomWidth', 'borderLeftWidth',
];

export function attachOverlay(ta) {
  const existing = overlays.get(ta);
  if (existing) return existing;

  const pre = document.createElement('pre');
  pre.className = 'hl-backdrop';
  pre.setAttribute('aria-hidden', 'true');

  function ensureAttached() {
    if (pre.isConnected) return true;
    const p = ta.parentElement;
    if (!p) return false; // textarea not in the document yet
    if (getComputedStyle(p).position === 'static') p.style.position = 'relative';
    p.appendChild(pre);
    lastSig = ''; // force a metric re-copy on the now-connected backdrop
    return true;
  }

  ta.classList.add('hl-on');
  ta.style.position = 'relative';
  ta.style.zIndex = '1';
  ta.style.backgroundColor = 'transparent';

  let lastText = null; // cache: one char typed -> exactly one re-highlight
  let lastSig = '';    // cache: metric signature -> no style churn while typing

  function applyMetrics(cs) {
    for (const p of METRIC_PROPS) pre.style[p] = cs[p];
    pre.style.boxSizing = 'border-box';
    pre.style.borderStyle = 'solid';
    pre.style.whiteSpace = 'pre-wrap';
    pre.style.overflowWrap = 'break-word';
    pre.style.wordBreak = cs.wordBreak;
  }

  function sync() {
    if (!ensureAttached()) return; // textarea got its parent after attach
    const cs = getComputedStyle(ta);
    const sig = METRIC_PROPS.map((p) => cs[p]).join('\u0001') + '\u0001' + cs.wordBreak;
    if (sig !== lastSig) {
      applyMetrics(cs);
      lastSig = sig;
    }
    const bt = parseFloat(cs.borderTopWidth) || 0;
    const bb = parseFloat(cs.borderBottomWidth) || 0;
    const bl = parseFloat(cs.borderLeftWidth) || 0;
    const br = parseFloat(cs.borderRightWidth) || 0;
    // clientWidth/Height exclude the border but include padding, so add the
    // border back to get the border-box size that matches the textarea's text area.
    pre.style.left = ta.offsetLeft + 'px';
    pre.style.top = ta.offsetTop + 'px';
    pre.style.width = ta.clientWidth + bl + br + 'px'; // client* accounts for scrollbars so wrapping matches
    pre.style.height = ta.clientHeight + bt + bb + 'px';
    const text = ta.value;
    if (text !== lastText) {
      pre.innerHTML = highlightToHtml(text) + '\n';
      lastText = text;
    }
    pre.scrollTop = ta.scrollTop;
    pre.scrollLeft = ta.scrollLeft;
  }

  let ro = null;
  if (typeof ResizeObserver !== 'undefined') {
    ro = new ResizeObserver(sync);
    ro.observe(ta);
  }
  ta.addEventListener('input', sync);
  ta.addEventListener('scroll', sync);
  window.addEventListener('resize', sync);
  window.addEventListener('scroll', sync, true);

  const handle = {
    sync() { sync(); },
    detach() {
      ta.removeEventListener('input', sync);
      ta.removeEventListener('scroll', sync);
      if (ro) { ro.disconnect(); ro = null; }
      window.removeEventListener('resize', sync);
      window.removeEventListener('scroll', sync, true);
      pre.remove();
      overlays.delete(ta);
    },
  };
  overlays.set(ta, handle);
  sync(); // initial paint (safe on hidden elements: offsets/clients are just 0)
  return handle;
}
