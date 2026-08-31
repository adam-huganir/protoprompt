# Chatvolution backend API (authoritative contract)

All JSON. Errors: `{"error": "message"}` with appropriate status.
Message rows are the full object:

```json
{
  "id": "...", "parentId": "..." , "role": "user|assistant",
  "text": "...", "reasoningText": "...", "status": "done|streaming|error",
  "preview": false, "promptRevId": "...", "model": "...",
  "reasoningEffort": "", "temperature": 1, "maxTokens": null,
  "error": "", "promptTokens": null, "outputTokens": null,
  "latencyMs": null, "createdAt": 0, "finishedAt": null,
  "siblingCount": 1, "siblingIndex": 1,
  "siblings": [{"id": "...", "siblingIndex": 1}],
  "previewMessage": null
}
```

`siblings` lists ALL accepted same-parent, same-role rows ordered by
creation (each entry: its `siblingIndex`). `previewMessage` (set only in
the session payload, only on assistant messages) is a full message row
with `"preview": true`.

## Files & revisions

- `GET /api/files` → `[{name, path, revCount, latestSavedAt}]`
- `POST /api/files` `{path}` → 201 entry | 400 bad name | 409 duplicate
- `GET /api/files/{name}` → `{path, text, latestRevId, revs: [{id, savedAt}], diskChangedSinceLatestRev}`
- `POST /api/files/{name}/save` `{text}` → `{revId}` (null when unchanged)
- `GET /api/files/{name}/diff?from=REV&to=REV` → `{hunks: [...]}` (to omitted → latest rev)
- `GET /api/revs/{id}` → `{id, file, text, savedAt}`

`{name}` may contain slashes (URL-encode each segment or not — the path
is taken verbatim after `/api/files/`).

## Sessions

- `GET /api/sessions?file=X` → `[Session]`
- `POST /api/sessions` `{file, title?, model?, reasoningEffort?, temperature?, maxTokens?, extraBody?}` → 201 Session
- `GET /api/sessions/{id}` → `{session, messages: [Message...]}` — messages are the linear accepted transcript, oldest→newest, each filled with `siblings` and (assistant rows) `previewMessage`
- `PUT /api/sessions/{id}` `{title, model, reasoningEffort, temperature, maxTokens, extraBody}` → Session
- `POST /api/sessions/{id}/model` `{model}` → Session — switches the model used by subsequent generations; 400 when `model` is blank, 404 when the session is unknown. The value is never checked against the upstream list.
- `DELETE /api/sessions/{id}` → `{ok: true}`
- `POST /api/sessions/{id}/head` `{messageId, toLeaf}` → Session (409 when the message is not an accepted row of this session)
- `POST /api/sessions/{id}/messages` `{parentId?, text}` → 201 Message row

```json
Session: { "id","file","title","model","reasoningEffort","temperature",
  "maxTokens","extraBody","headMessageId","createdAt","updatedAt" }
```

## Messages

- `POST /api/messages/{id}/accept` → Session (400 when row is not a done preview)
- `DELETE /api/messages/{id}` → `{ok: true}` (cascades to descendants)
- `GET /api/messages/{id}/context` → `{system, history: [{role, content}]}` — exactly what the next generation would send (system comes from the message's `promptRevId`, falling back to the file's latest revision)

## Generation streams (SSE over POST)

Response is `text/event-stream`; frames use `event:` + `data:` (JSON).
Events: `meta` (row created), `delta` (`{kind:"reasoning"|"content", text}`), `done` (`{message: Message}` final row), `error` (`{message}`), and `complete` (resend only, `{headMessageId}` after the whole chain).

- `POST /api/sessions/{id}/generate` `{previewFor?: MSGID}`
  - No `previewFor`: append a fresh assistant generation after the current head (creates the row, streams it, head moves to it).
  - With `previewFor` (must be an accepted row of this session): stream a preview for that row's position. Any older preview of the same position is deleted first; a currently *streaming* preview returns 409 before the stream starts.
- `POST /api/sessions/{id}/resend` `{messageId, text}`
  - `messageId` must be an accepted row; `text` replaces it as a new sibling user message under the same parent; the primary path after it is regenerated assistant-by-assistant. Streams `meta` (the new user row), `delta`…, `done`, then `meta`/`done` pairs for each regenerated assistant, finally `complete`.

On upstream failure mid-stream: `done` with `{message: {...status:"error", error:"..."}}`.

## Health / config

- `GET /api/health` → `{ok: true}`
- `GET /api/config` → `{baseUrl, models, defaultModel, reasoningEffort, hasKey, dir}`
- `GET /api/models` → `{models: ["id", ...]}` (case-insensitively sorted, deduped) — 501 when no baseUrl or no API key is configured, 502 when the upstream call fails.

The UI merges this list with `models`/`defaultModel` from `/api/config` plus the current session's model, so a fetch failure never empties the picker.
