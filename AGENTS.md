# Repository Guidelines

## Project Structure & Module Organization
- This repo will be a Go API with a lightweight embedded frontend shipped as a single binary.
- Recommended layout:
  - `cmd/satsplit/` for the main entrypoint.
  - `internal/` for private app packages (handlers, services, storage).
  - `web/` for frontend source assets (HTML/CSS/JS).
  - `static/` for built frontend output (embedded with `embed`).
  - `configs/` for example config files and `.env.example`.
- Keep packages small and domain-focused (e.g., `internal/wallet`, `internal/http`).

## Build, Test, and Development Commands
- Standardize on Go tooling:
  - `go run ./cmd/satsplit` to run locally.
  - `go test ./...` to run all tests.
  - `go test ./internal/...` for faster inner-loop runs.
  - `go vet ./...` for static checks.
- If a frontend build step exists, wire it into `make dev`/`make build` (or a `go:generate` step) so the embedded assets stay in sync.

## Coding Style & Naming Conventions
- Use `gofmt` (tabs, Go standard formatting). Run `gofmt -w` before committing.
- Package names are short, lowercase, no underscores (e.g., `wallet`, `httpapi`).
- Exported identifiers use `CamelCase`; keep API structs and JSON fields explicit.
- Keep HTTP handlers thin; move logic into services with clear interfaces.

## Testing Guidelines
- Use the Go standard testing package.
- Name tests `TestXxx` and place them in `*_test.go`.
- Prefer table-driven tests for handlers and services.
- Add tests for new endpoints and any parsing/validation logic.

## Commit & Pull Request Guidelines
- No commit convention is established yet. Use short, imperative summaries (e.g., “Add transaction list endpoint”).
- PRs should include: scope summary, test results, and screenshots for UI changes.
- Link related issues when applicable.

## Embedded Frontend Notes
- Use `//go:embed static/*` to bundle the built frontend into the binary.
- Keep frontend lightweight: one HTML entrypoint, minimal JS, and small CSS.
- UI needs are basic (text input, photo capture/upload, QR display, multi-select list, copy-to-clipboard output). This fits well in an embedded static frontend.
- Use vanilla JS; avoid heavy frameworks unless requirements expand.
- If the UI grows complex or needs frequent iteration, consider a separate frontend repo; otherwise, embedded assets keep deployment simple.

## Security & Configuration Tips
- Keep secrets out of the repo; use `.env` files locally and document required variables in `.env.example`.
- For crypto workflows, document key-handling boundaries and avoid logging sensitive material.
