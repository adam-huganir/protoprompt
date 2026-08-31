# Repository Guidelines

## Project Overview

protoprompt is a local, single-user **prompt-iteration chat workbench**: a chat UI where every assistant reply is reproducible against the exact prompt revision that produced it. Prompt files are markdown on disk with saved revisions and diffs; conversations are SQLite-persisted trees supporting branching, regenerate, preview/accept, resend, and head moving. Backend is Go; the front end is vanilla ES modules embedded into the same binary. No frameworks, no Node build step, no external runtime beyond Go (and Node only for optional scripts).

## Architecture & Data Flow

```
web/js (chat.js, editor.js) --fetchJSON / streamSSE--> internal/api --> internal/store --> SQLite + prompt files
                                   ^                                          |
                                   +---- SSE meta/delta/done/error ----------- internal/llm --> OpenAI-compatible upstream
```

- **Layers** (`main.go` wires everything): `config` → `db` → `store` → `api`; `llm` is the upstream client; `diffx` computes prompt-revision hunks. Constructor injection only — `store.New(dir, db)`, `llm.New(baseURL, apiKey, timeout)`, `api.New(cfg, db, st, web)`.
- **Message tree**: rows in `message` are parent-linked (`parent_id`). `preview = 0` rows are accepted; `preview = 1` are speculative. The visible transcript is a walk from `session.head_message_id` up through parents (`Store.Transcript`). "Siblings" are accepted same-parent, same-role rows in creation order. Deleting a message cascades to its subtree.
- **Ordering invariant**: never use a raw wall-clock for row ordering. `store.nextTS()` returns **strictly increasing** unix-ms so creation order is total even within one millisecond.
- **Prompt drift**: each assistant row snapshots `prompt_rev_id`. The system prompt resolves to that revision (`sessions.go: systemText`), falling back to the file's latest revision. UI flags a reply as stale when its rev differs from the file's newest — see `TestPromptDriftUsesLatestRev`.
- **Generation**: `POST /api/sessions/{id}/generate` inserts a streaming assistant row, then `streamAssistant` relays upstream deltas as SSE and finalizes the row. `previewFor` streams a preview instead of moving the head; accepting (`POST /api/messages/{id}/accept`) flips it and moves the head. `resend` re-creates a user row as a new sibling and regenerates the accepted chain below it.
- **Contract authority**: `docs/api.md` is the authoritative HTTP/SSE contract (request bodies, `{"error": msg}` shape, event names, status codes). Update it in the same change as any route or payload change.

## Key Directories

| Path | Role |
|---|---|
| `internal/api/` | HTTP layer: `server.go` (dispatch, `writeJSON`/`writeErr`), `sessions.go`, `generate.go` (`sseStream`), `resend.go`, `files.go` |
| `internal/store/` | Typed SQLite access + prompt workspace file ops (`prompt.go`, `message.go`, `session.go`), `NewID`, `nextTS` |
| `internal/db/` | `db.Open` (driver + pragmas), `migrations.go` (verbatim `schema` statements) |
| `internal/config/` | Defaults + file + env precedence, validation |
| `internal/llm/` | Streaming chat-completions client, model listing, `ErrCanceled` |
| `internal/diffx/` | Line-diff hunks used for prompt-revision diffs |
| `cmd/stubllm/` | Dev-only echo upstream (`go run ./cmd/stubllm`, binds `127.0.0.1:9876`, `-port` flag) so the UI can be exercised without a paid endpoint |
| `web/` | Front end served from the binary: `index.html`, `app.css`, `hl.css`, `js/*` |
| `docs/` | `api.md` (contract) |
| `prompts/`, `smoke/` | Gitignored user workspace and local test scripts |

`cmd/handler/` and `cmd/handlers/` are empty leftover directories — no code lives there.

## Development Commands

```bash
go build ./...                     # compile everything
go build -o protoprompt.exe .     # the single shippable binary
go test ./...                      # full test suite
go test ./internal/api -run TestPreviewAcceptFlow   # one test
go run . -config protoprompt.json # run the app (flags: -config -dir -db -port -host)

go run ./cmd/stubllm               # echo upstream on 127.0.0.1:9876 (model "stub")
node smoke/mdhl.test.mjs           # front-end highlighter smoke test (prints PASS mdhl)
```

- **Web assets are compiled in**: `main.go` carries `//go:embed all:web`. Editing `web/**` has **no effect until the binary is rebuilt** — there is no live reload. Verify changes with a rebuild, or by fetching `http://127.0.0.1:<port>/js/<file>.js`.
- No Makefile, no CI, no Dockerfile, no linter/vet config — nothing runs automatically.

## Code Conventions & Common Patterns

**Go**
- Standard library over deps: `database/sql` + `modernc.org/sqlite`, `net/http` with Go 1.22 method patterns; no ORM, no interface abstraction except where a real seam helps (`fs.FS` for embedded assets).
- Handlers are methods on `*Server` taking `(w, r, …)`; responses go through `writeJSON` / `writeErr`, whose body is always `{"error": message}`.
- Path params that contain slashes (prompt file paths) are **not** expressible in `ServeMux`, so `/api` is dispatched by hand in `dispatchAPI`; sub-paths are split with `splitSeg`.
- SQL: column lists are constants (`messageCols`) paired with `scan*` helpers; SQLite booleans are integers (`boolInt`); multi-statement invariants use a transaction (see `AcceptPreview`).
- Naming: `snake_case` columns ↔ `camelCase` JSON via explicit tags; files named after the domain type they serve (`prompt.go`, `sessions.go`); IDs from `store.NewID()` (16 hex chars), never from the caller.
- Errors are wrapped with `%w`; sentinels carry meaning — `llm.ErrCanceled` (abort, partial buffers still returned) and `store.ErrAccept` (row is not a done preview).

**Front end (`web/js/`)**
- ES modules, no framework, no bundler. Panes are mounted imperatively: `mountChat(root, deps)` / `mountEditor(root, hooks)` return small API objects.
- Shared helpers: `$` (querySelector), `fetchJSON`, `streamSSE` (POST-based SSE reader in `api.js`), and a minimal mutable store `state.js` (`state`, `subscribe`, `emit`). Cross-pane signals are `document`-level custom events: `cv:toast`, `cv:sessionsChanged`.
- Build DOM with `createElement`; `innerHTML` is acceptable only when the content passed in is already escaped by your own code (the markdown highlighter is the one such case).
- Streaming UI accumulates raw text in a local variable and re-renders from the accumulated string rather than appending text nodes, so a truncated markdown marker never renders as markup.
- Styling: single `app.css` with CSS custom properties for the theme (`--bg`, `--fg`, `--accent`, `--mono`, …); BEM-ish prefixes per component (`msg-*`, `btn-*`, `ed-*`) and per-token classes for syntax highlighting (`md-*`).

**Config precedence** (`internal/config/config.go`): defaults < `protoprompt.json` < env < CLI flags. Env: `PROTOPROMPT_BASE_URL` / `PROTOPROMPT_API_KEY` / `PROTOPROMPT_MODEL`, with `OPENAI_BASE_URL` / `OPENAI_API_KEY` as fallbacks. **Trap:** an inherited `OPENAI_*` env var silently overrides the config file — confirm with `GET /api/config` (reports `baseUrl`, `hasKey`, `dir`) before assuming an upstream problem.

## Important Files

- `main.go` — flags, embed, startup.
- `docs/api.md` — HTTP/SSE contract; the spec to satisfy when changing routes.
- `internal/db/migrations.go` — full schema (`session`, `message`, `prompt_rev`) and indexes; schema changes belong here, statements run verbatim with `IF NOT EXISTS`.
- `internal/api/generate.go` — SSE framing (`sseStream.event`) reused by `resend.go`.
- `internal/store/message.go` / `prompt.go` — tree walk, sibling computation, atomic save-with-revision semantics (`SaveFile` returns `nil` revID when text is unchanged).
- `protoprompt.json`, `.env` — local, gitignored config; `prompts/*.md` are user data, each file's text being the system prompt.
- `web/index.html`, `web/js/chat.js`, `web/js/editor.js` — the two panes; `web/js/state.js` for shared state shape.

## Runtime/Tooling Preferences

- **Go 1.25**, module `protoprompt` (`go 1.25.0`). Pure-Go SQLite driver `modernc.org/sqlite` — **no cgo**, so no compiler toolchain is needed.
- **Node 22 / bun are optional**; `package.json` is literally `{}` and there are no dependencies to install. Do not introduce an npm build step.
- SQLite settings from `db.Open`: WAL journal, `busy_timeout=5000`, `foreign_keys=1`, with a single-pooled-connection fallback when DSN pragmas fail.
- Default ports: app `8420` (per `protoprompt.json`), stub upstream `9876`. The app binds `127.0.0.1`; generation calls respect `HTTP_PROXY`/`NO_PROXY` from the environment, so point local stubs in `NO_PROXY`.

## Testing & QA

- Framework: standard `testing`, **in-package** tests (`package api`, `package store`) so unexported helpers are exercised.
- Isolation: one database per test via `t.TempDir()` — `openTestDB(t)` in `internal/store/helpers_test.go`, `newLiveServer(t)` in `api_test.go` (temp DB + temp workspace dir + in-process echo upstream). Files seeded with `writeFileAbs`; helpers marked `t.Helper()`; generic assertions like `mustJSON[T](t, data)`.
- **Never contact a real upstream.** `internal/api/api_test.go` spins an in-process `httptest` echo server mirroring `cmd/stubllm`; SSE responses are asserted with `parseSSE`.
- Test naming: `Test<Flow>` for behavior-level flows (`TestGenerateFlow`, `TestPreviewAcceptFlow`, `TestResendFlow`, `TestPromptDriftUsesLatestRev`); error paths named for their status (`TestNoKey501`).
- Coverage expectations: no percentage gate, no coverage tooling configured. The bar is behavioral — each user-visible flow (generate, preview/accept/discard, resend chain, head move, prompt drift, model switch, missing-key/model errors) has a test that asserts the payload and status, not just "it ran".
- Manual QA loop for UI work: start `cmd/stubllm`, point a local config's `baseUrl` at `http://127.0.0.1:9876/v1`, rebuild the app binary, then drive the served UI and confirm observable behavior.
