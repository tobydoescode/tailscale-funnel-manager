# Tailscale Funnel Manager

In-cluster web UI + API that toggles Tailscale funnel on/off for labeled
Ingress resources. For each opted-in Ingress, maintains a companion
`<name>-funnel` Ingress with `ingressClassName: tailscale` whose funnel
annotation is flipped by the UI. A 60s reconciler GCs orphaned companions.

## 0. Project

### Quick Reference

[Task](https://taskfile.dev) is the task runner. Run `task --list-all` to see available commands.

Run locally: `task dev` (needs kubeconfig with Ingress RBAC). Run tests: `task test`.

### Tech Stack

- **Go** — stdlib `net/http` (1.22+ path patterns, no router library)
- **Kubernetes** — `client-go` typed client (Ingresses only)
- **Frontend** — vanilla JS, HTML, CSS, embedded via `//go:embed`
- **Observability** — `log/slog` (JSON)
- **Container** — distroless nonroot (UID 65532), multi-arch (amd64/arm64)

### Project Layout

```
main.go                     Entry point: config, k8s client, routes, reconciler, server
main_test.go                kubeconfigPath() tests
internal/
  api/                      HTTP handlers (ListServices, SetFunnel)
  auth/                     Bearer token middleware (constant-time compare)
  kube/                     Narrow typed k8s client wrapper (Ingresses)
  manager/                  Mirror Ingress builder + orphan reconciler
  metrics/                  Minimal Prometheus text-format counters
  web/                      go:embed assets (index.html, app.js, style.css)
.github/
  workflows/ci.yaml         CI pipeline
  actions/                  Composite actions: test, source-security, image-smoke, image-security
scripts/                    Shell helpers (docker-build-needed.sh)
```

### Architecture

**Data flow**: Labeled Ingress → kube client lists → API returns service state → UI polls + toggles → handler patches companion annotation. Reconciler runs on interval, GCs orphans.

**Run exactly one replica** — the reconciler has no leader election; multiple replicas would race on mirror updates/deletes.

**Routes**:
| Path | Auth | Purpose |
|------|------|---------|
| `GET /` | no | Embedded UI (HTML/CSS/JS) |
| `GET /healthz` | no | Liveness probe |
| `GET /readyz` | no | 503 if can't list Ingresses |
| `GET /metrics` | no | Prometheus counters (hand-rolled, `internal/metrics`) |
| `GET /api/services` | bearer | List opted-in services with funnel state |
| `POST /api/services/{namespace}/{name}/funnel` | bearer | Toggle funnel on/off (`{"enabled": true\|false}`) |

Auth always active — `FUNNEL_MANAGER_TOKEN` is required. Repeated wrong-token attempts from one IP are answered 429 for a cooldown window.

**Opt-in**: Source Ingress labeled `funnel-manager.toby.cloud/enabled=true` with annotations for hostname, optional tags, optional path-prefix. Companion Ingress mirrors rules with `ingressClassName: tailscale`.

### Environment Variables

| Var | Default | Purpose |
|-----|---------|---------|
| `FUNNEL_MANAGER_TOKEN` | _required_ | Bearer token for API auth |
| `FUNNEL_MANAGER_ADDR` | `:8080` | Listen address |
| `FUNNEL_MANAGER_DEFAULT_TAGS` | `tag:live-k3s-funnel` | Tailscale tags when source Ingress doesn't specify |
| `FUNNEL_MANAGER_TAILNET` | _unset_ | Tailnet domain for public URL rendering |
| `FUNNEL_MANAGER_KUBECONFIG` | _unset_ | Kubeconfig path (dev only); falls back to `KUBECONFIG`, then in-cluster |
| `FUNNEL_MANAGER_RECONCILE_INTERVAL` | `60s` | Orphan-GC reconciler interval (Go duration) |

### Testing

**Go tests**: `task test` (containerized `go test ./...`) — stdlib `testing`, `httptest`, table-driven tests, hand-written mocks (no mocking library).

**Image smoke tests**: CI runs container against fixture data via `.github/actions/image-smoke`.

### Conventions

- **Error handling**: simple and explicit. `slog.Error` + HTTP status code. `os.Exit(1)` on fatal startup errors. No custom error types.
- **Testing**: `Test{Function}` or `Test{Function}_{Scenario}`. No external frameworks.
- **Naming**: Go standard (PascalCase exported, camelCase unexported). HTML/CSS kebab-case.
- **Commits**: `type: subject` or `type(scope): subject`. Types: fix, feat, docs, ci, chore.
- **No comments** unless the "why" is non-obvious.

### CI Pipeline

`.github/workflows/ci.yaml` — runs on PRs, pushes to `main`, and tag pushes.

```
changes (detect image-relevant files)
├── test (Go tests)
├── source-security (govulncheck, actionlint, shellcheck)
└── image-build (conditional: only when image files changed)
    ├── Build per-arch (amd64, arm64)
    ├── Smoke test
    ├── Trivy scan (CRITICAL/HIGH blocks publish)
    └── Push + merge multi-arch manifest
```

PR builds validate only. `main` pushes also publish images. Tag pushes publish semver.

## 1. Think Before Coding

**Don't assume. Don't hide confusion. Surface tradeoffs.**

Before implementing:
- Read all relevant files first, never edit blind.
- Understand the full requirement before writing anything. If unclear, ask.
- State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- If something is unclear, stop. Name what's confusing. Ask.

## 2. Simplicity First

**Minimum code that solves the problem. Nothing speculative.**

- No features beyond what was asked.
- No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- No error handling for impossible scenarios.
- If you write 200 lines and it could be 50, rewrite it.

Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

## 3. Surgical Changes

**Touch only what you must. Clean up only your own mess.**

When editing existing code:
- Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style, even if you'd do it differently.
- If you notice unrelated dead code, mention it - don't delete it.

When your changes create orphans:
- Remove imports/variables/functions that YOUR changes made unused.
- Don't remove pre-existing dead code unless asked.

The test: Every changed line should trace directly to the user's request.

## 4. Goal-Driven Execution

**Define success criteria. Loop until verified.**

Transform tasks into verifiable goals:
- "Add validation" → "Write tests for invalid inputs, then make them pass"
- "Fix the bug" → "Write a test that reproduces it, then make it pass"
- "Refactor X" → "Ensure tests pass before and after"

For multi-step tasks, state a brief plan:
```
1. [Step] → verify: [check]
2. [Step] → verify: [check]
3. [Step] → verify: [check]
```

Strong success criteria let you loop independently. Weak criteria ("make it work") require constant clarification.

## 5. Completion Criteria

**Ensure complete before moving on**

- Document new features and feature changes.
- Write meaningful tests and ensure adequate coverage.
- Fix errors before moving on. Never skip failures.

## 6. Git Workflow

When dispatching subagents that write code, use `isolation: "worktree"` so they work in an isolated git worktree on a feature branch. Merge to `main` via PR. Renovate pushes directly to `main`, so always `git pull --rebase` before branching.
