// Minimal mutable store + subscribe/emit. No framework.
export const state = {
  config: null,          // { baseUrl, models, defaultModel, reasoningEffort, hasKey, dir }
  files: [],             // [{ name, path, revCount, latestSavedAt }]
  currentFile: null,     // path of file open in editor
  file: null,            // file detail { path, text, latestRevId, revs, diskChangedSinceLatestRev }
  dirty: false,          // editor buffer differs from last saved text
  saving: false,         // a save request is in flight
  sessions: [],          // session rows, newest first, for currentFile
  session: null,         // { session, messages, latestRevId, diskChangedSinceLatestRev }
  currentSessionId: null,
  streaming: false,      // a generate/resend stream is active
  activeKind: 'chat',    // tab: 'chat' | 'helper'
};

const subs = new Set();

export function subscribe(fn) {
  subs.add(fn);
  return () => subs.delete(fn);
}

export function emit() {
  for (const fn of subs) fn(state);
}
