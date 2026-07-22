---
name: dev-build-test
description: How to build, test, and lint the GPUardian Go codebase, build the React UI, and build the gateway Docker image. Use whenever the user asks to build, compile, test, lint, vet, run tests, make the image, or check that the project builds — even if they just say "does it build" or "run the tests".
---

# Build, test, and lint GPUardian

All commands run from the repo root. The module path is `gpuardian` (not
`github.com/...`); Go 1.25+.

## Go — the gate

There is no separate lint config. `go vet` + `go test` are the gates.

```bash
go vet ./...
go test ./...
```

Focused tests (table-driven style throughout `internal/web/*_test.go`):

```bash
go test ./internal/web/...
go test -run TestName ./internal/web/
```

Build the single binary (shared by daemon and CLI; subcommand dispatch is in
`cmd/gpuardian/main.go`):

```bash
go build -buildvcs=false -o gpuardian ./cmd/gpuardian
```

Go tests do **not** need the UI built — they never touch the static handler's
disk path. So `go test ./...` is safe to run without `npm`.

## UI — required before the gateway image, not for Go tests

The frontend is a React 19 + Vite single-file SPA in `web/ui/`. The gateway
serves `web/ui/dist/` from disk at runtime via `GPUARDIAN_WEB_UI_DIR` — there
is **no `go:embed`** of UI assets, so do not add one.

```bash
npm --prefix web/ui ci
npm --prefix web/ui run build      # outputs web/ui/dist/
```

`web/ui/dist/` and `web/ui/node_modules/` are gitignored. The UI has no test
runner configured.

## Gateway Docker image

Build the UI first (the Dockerfile builds it in a `node` stage, but local
non-image dev runs also need `dist/` on disk).

```bash
# Production image (tag gpuardian-web:local)
sudo docker compose -f compose.web.yml build

# Dev image (tag gpuardian-web:dev) — also starts it
sudo docker compose -f compose.web-dev.yml up -d --build
```

The image is non-root (UID/GID `65532`), `read_only: true`, `cap_drop: [ALL]`,
`no-new-privileges`. `Dockerfile.web` builds `CGO_ENABLED=0` with
`-trimpath -ldflags="-s -w"` on `distroless/static-debian12:nonroot`.

## Gotchas

- **Module path is `gpuardian`**, not `github.com/...`. Internal imports look
  like `gpuardian/internal/web`. Keep this style in new code.
- **Go tests don't need the UI.** Don't gate a Go-only change on `npm run build`.
- **UI must be built before the image** — the Dockerfile's `node` stage runs
  `npm ci && npm run build`, but if you're testing the gateway outside Docker
  you also need `dist/` on disk.
- **No CGO.** `internal/history` uses `modernc.org/sqlite` (pure Go). Don't
  switch to a CGO sqlite driver — the gateway image is non-root and CGO-free.

## Read before sensitive edits

- `AGENTS.md` — "Build, test, lint" and "Architecture boundaries" sections.
- `README.md` — production build steps and env-var list.
- `DEVELOPMENT.md` — dev isolation rules before changing dev ports/paths.
