# protoprompt

A local, single-user **prompt-iteration chat workbench**: a chat UI where every
assistant reply is reproducible against the exact prompt revision that produced
it. Prompt files are markdown on disk with saved revisions and diffs;
conversations are SQLite-persisted trees that support branching, regenerate,
preview/accept, resend, and moving the head.

The backend is Go. The front end is vanilla ES modules embedded into the same
binary — no framework, no bundler, no Node build step. The only runtime
requirement is Go itself (the SQLite driver is pure Go, so no cgo/compiler
toolchain).

## Why

When you iterate on a system prompt, ordinary chat history becomes misleading:
old replies were produced by a prompt you have since changed. protoprompt keeps
every prompt file's revision history and stamps each assistant message with the
`prompt_rev_id` it was generated against, so the UI can flag a reply as stale
when the file has moved on. You edit the prompt in one pane, drive the
conversation in the other, and branch/regenerate freely without losing the
thread you came from.

## Features

- **Prompt workspace** — markdown files under a `prompts/` directory; each save
  creates a revision. Line-diff view between any two revisions.
- **Conversation trees** — messages are parent-linked. Branch at any point;
  siblings are tracked and navigable.
- **Preview / accept** — stream a candidate reply for a position without moving
  the conversation head; accept it to commit, or discard it.
- **Resend** — replace a user message as a new sibling and regenerate the
  accepted chain below it.
- **Prompt-drift detection** — replies generated against an older prompt
  revision are flagged.
- **Model switching** — per session, merged from `/api/config`, `/api/models`,
  and the session's current model so the picker is never empty.
- **OpenAI-compatible upstream** — any chat-completions endpoint. Streaming via
  SSE, including reasoning deltas.
- **Single binary** — web assets are `//go:embed`-ed.

## Requirements

- **Go 1.25+**
- An OpenAI-compatible chat-completions endpoint (base URL + API key). For
  local development without a paid endpoint, use the bundled stub (see below).
- Node/bun are **optional** — only for the front-end smoke script. There are no
  npm dependencies (`package.json` is `{}`).

## Quick start

```bash
# 1. build
go build -o protoprompt.exe .        # or: go build ./...

# 2. configure (see below), then run
go run . -config protoprompt.json
# -> protoprompt listening on http://127.0.0.1:8420
```

Open <http://127.0.0.1:8420>. Create a prompt file, start a session against it,
and chat.

### Run against the dev stub (no real upstream)

```bash
go run ./cmd/stubllm            # echo upstream on 127.0.0.1:9876, model "stub"
```

Point your config's `baseUrl` at `http://127.0.0.1:9876/v1`, set `models` to
`["stub"]`, and run the app. If you have `HTTP_PROXY` set, add `127.0.0.1` to
`NO_PROXY` so generation calls reach the stub.

## Configuration

Precedence: **defaults < `protoprompt.json` < environment < CLI flags.**

`protoprompt.json` (all fields optional; a missing file is not an error):

```json
{
  "baseUrl": "https://your-endpoint.example.com/v1",
  "apiKey": "sk-...",
  "models": ["gpt-4o-mini"],
  "defaultModel": "gpt-4o-mini",
  "reasoningEffort": "",
  "extraBody": {},
  "requestTimeoutSeconds": 300,
  "port": 8420,
  "host": "127.0.0.1"
}
```

Environment variables (`PROTOPROMPT_*` preferred, `OPENAI_*` used as fallback):

| Variable | Fallback | Effect |
|---|---|---|
| `PROTOPROMPT_BASE_URL` | `OPENAI_BASE_URL` | upstream base URL |
| `PROTOPROMPT_API_KEY` | `OPENAI_API_KEY` | upstream API key |
| `PROTOPROMPT_MODEL` | — | default model |

> **Trap:** an inherited `OPENAI_*` variable silently overrides the config file.
> Check `GET /api/config` (reports `baseUrl`, `hasKey`, `dir`) before assuming
> an upstream problem.

CLI flags:

| Flag | Default | Purpose |
|---|---|---|
| `-config` | `./protoprompt.json` | config file path |
| `-dir` | `./prompts` | prompt workspace directory |
| `-db` | `protoprompt.db` | SQLite database file |
| `-port` | from config (`8420`) | listen port |
| `-host` | from config (`127.0.0.1`) | listen host |

## Development

```bash
go build ./...                                      # compile everything
go test ./...                                       # full suite
go test ./internal/api -run TestPreviewAcceptFlow   # a single test
node smoke/mdhl.test.mjs                            # front-end highlighter smoke test
```

Web assets are compiled into the binary (`//go:embed all:web`). **Editing
`web/**` has no effect until the binary is rebuilt** — there is no live reload.

There is no Makefile, CI, Dockerfile, or linter config; nothing runs
automatically.

Tests are standard `testing`, in-package, one SQLite database per test via
`t.TempDir()`. They never contact a real upstream — an in-process `httptest`
echo server stands in for the LLM.

## Project layout

| Path | Role |
|---|---|
| `main.go` | flags, embed, startup wiring (`config` → `db` → `store` → `api`) |
| `internal/api/` | HTTP + SSE layer; `/api` is hand-dispatched (prompt paths contain slashes) |
| `internal/store/` | typed SQLite access + prompt workspace file ops |
| `internal/db/` | `db.Open` (driver + pragmas), full schema in `migrations.go` |
| `internal/config/` | defaults / file / env / flag precedence + validation |
| `internal/llm/` | streaming chat-completions client, model listing |
| `internal/diffx/` | line-diff hunks for prompt-revision diffs |
| `cmd/stubllm/` | dev-only echo upstream (`127.0.0.1:9876`, `-port` flag) |
| `web/` | front end served from the binary (`index.html`, `app.css`, `js/*`) |
| `docs/api.md` | **authoritative** HTTP/SSE contract |

## API

`docs/api.md` is the authoritative contract (request bodies, `{"error": msg}`
shape, SSE event names, status codes). A quick tour:

- `GET /api/health` → `{ok: true}`
- `GET /api/config` → `{baseUrl, models, defaultModel, reasoningEffort, hasKey, dir}`
- `GET /api/models` → `{models: [...]}` (`501` if unconfigured, `502` on upstream failure)
- `GET|POST /api/files`, `GET /api/files/{name}`, `POST /api/files/{name}/save`,
  `GET /api/files/{name}/diff?from=&to=`
- `GET|POST /api/sessions`, `GET|PUT|DELETE /api/sessions/{id}`,
  `POST /api/sessions/{id}/model`, `POST /api/sessions/{id}/head`
- `POST /api/sessions/{id}/generate` — SSE over POST; `meta` / `delta` / `done` / `error`
- `POST /api/sessions/{id}/resend` — SSE; regenerates the accepted chain, ends with `complete`
- `POST /api/messages/{id}/accept`, `DELETE /api/messages/{id}`,
  `GET /api/messages/{id}/context`

## Data & privacy

Everything is local. The SQLite database (`protoprompt.db`), the `prompts/`
workspace, `protoprompt.json`, and `.env` are gitignored user data. The server
binds `127.0.0.1` by default. The only outbound traffic is generation calls to
the upstream you configure (which respect `HTTP_PROXY` / `NO_PROXY`).
