# Tailscale Funnel Manager

A small in-cluster web UI + API that toggles Tailscale funnel on and off for
labeled Ingress resources, without committing a Flux change per toggle.

## How it works

For each source Ingress labeled `funnel-manager.toby.cloud/enabled=true`,
funnel-manager maintains a companion Ingress named `<source>-funnel` in the
same namespace. The companion has `ingressClassName: tailscale` and its
`tailscale.com/funnel` annotation is flipped between `"true"` and `"false"` by
the UI.

The companion **persists for the life of the opt-in**. Toggles patch the one
annotation in place so the underlying Tailscale device keeps its node key and
stays signed under tailnet-lock. When funnel is OFF the service is still
reachable on your tailnet (tailnet-internal HTTPS) but not publicly. To remove
a service from the tailnet entirely, drop the opt-in label in Git — a 60s
reconciler GCs the orphaned companion.

## Opting a service in

Add the label and annotations to the service's existing private Ingress:

```yaml
metadata:
  labels:
    funnel-manager.toby.cloud/enabled: "true"
  annotations:
    funnel-manager.toby.cloud/hostname: "litellm"              # required — Tailscale hostname
    funnel-manager.toby.cloud/tags: "tag:live-k3s-funnel"      # optional — defaults to FUNNEL_MANAGER_DEFAULT_TAGS
    funnel-manager.toby.cloud/path-prefix: "/v1"               # optional — restricts the funnel to this path
```

The funnel-manager mirrors the private Ingress's rules (dropping `host:` and
rewriting TLS to the Tailscale hostname). If `path-prefix` is set, the mirror
uses that single prefix and preserves the backend reference; otherwise the
paths are copied verbatim.

## API

All `/api/*` endpoints require `Authorization: Bearer <token>`.

| Method | Path | Description |
| ------ | ---- | ----------- |
| GET    | `/api/services` | List opted-in services with current funnel state and public URL |
| POST   | `/api/services/{namespace}/{name}/funnel` | Body `{"enabled": true\|false}` — first call creates the companion, subsequent calls patch the annotation |
| GET    | `/healthz` | Liveness (no auth) |
| GET    | `/readyz` | Readiness — lists Ingresses to confirm RBAC (no auth) |

The UI at `/` polls `/api/services` every 10s and calls `POST /funnel` when a
toggle is flipped.

## Configuration

Environment variables (required unless noted):

| Var | Default | Purpose |
| --- | ------- | ------- |
| `FUNNEL_MANAGER_TOKEN` | _required_ | Bearer token accepted by the API middleware |
| `FUNNEL_MANAGER_ADDR` | `:8080` | Listen address |
| `FUNNEL_MANAGER_DEFAULT_TAGS` | `tag:live-k3s-funnel` | Tailscale tags when a source Ingress doesn't specify its own |
| `FUNNEL_MANAGER_TAILNET` | _unset_ | Tailnet domain (e.g. `taild6db24.ts.net`), used to render public URLs in the UI |
| `FUNNEL_MANAGER_KUBECONFIG` | _unset_ | Local kubeconfig path for development; unset uses in-cluster service account config |
| `FUNNEL_MANAGER_RECONCILE_INTERVAL` | `60s` | How often the orphan-GC reconciler runs (Go duration) |

In production both `FUNNEL_MANAGER_TOKEN` and `FUNNEL_MANAGER_TAILNET` come
from the `funnel-manager-auth` Secret, populated by ExternalSecrets from the
1Password item `Funnel Manager Live Auth`.

## RBAC

Cluster-scoped, Ingresses only:

```yaml
apiGroups:   ["networking.k8s.io"]
resources:   ["ingresses"]
verbs:       ["get", "list", "watch", "create", "update", "patch", "delete"]
```

## Local development

Go 1.26. No local toolchain required — tests and builds run in a container.

```
task test        # go test ./... inside golang:1.26-alpine
task tidy        # go mod tidy
task build       # docker buildx build --platform linux/arm64
task build-load  # build and load to local docker
task push        # build and push to ghcr.io/tobydoescode/tailscale-funnel-manager:<VERSION>
```

To run the manager locally against your current Kubernetes context:

```bash
FUNNEL_MANAGER_TOKEN=dev \
FUNNEL_MANAGER_KUBECONFIG="$HOME/.kube/config" \
FUNNEL_MANAGER_TAILNET=taild6db24.ts.net \
go run .
```

Then open `http://localhost:8080` and enter `dev` when prompted. The kube
identity in the selected context needs the Ingress RBAC listed below.

Version is pinned in `Taskfile.yml` (`VERSION: x.y.z`). CI runs tests,
coverage reporting, source security checks, image smoke tests, Trivy image
scans, multi-arch image builds (`linux/amd64`, `linux/arm64`), and manifest
merge via `.github/workflows/ci.yaml`.

## Deployment

Deployed into the home-lab k3s cluster via Flux manifests kept in the
[lab repo](https://github.com/tobydoescode/lab) under
`deploy/flux/apps/base/funnel-manager/`. Update the image digest there
after a new build if you want the cluster to pull it.

The Deployment runs as a nonroot distroless container (UID 65532), with
read-only root filesystem and all capabilities dropped.

## Project layout

```
.
├── main.go
├── Dockerfile
├── Taskfile.yml
└── internal/
    ├── api/        HTTP handlers
    ├── auth/       bearer-token middleware
    ├── kube/       narrow kubernetes client wrapper
    ├── manager/    mirror Ingress builder + orphan reconciler
    └── web/        embedded HTML/CSS/JS UI
```
