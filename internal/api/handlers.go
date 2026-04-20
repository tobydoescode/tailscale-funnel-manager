// Package api contains the HTTP handlers for the funnel-manager API.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/kube"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"

	networkingv1 "k8s.io/api/networking/v1"
)

// Config wires a Handler's dependencies.
type Config struct {
	Kube        kube.Client
	DefaultTags string
	// Tailnet is used to construct the public funnel URL shown in the UI
	// (e.g. "tail-abc.ts.net"). Optional; if empty the URL is omitted.
	Tailnet string
}

// Handler serves /api/services and /api/services/{ns}/{name}/funnel.
type Handler struct {
	cfg Config
}

// NewHandler constructs a Handler.
func NewHandler(cfg Config) *Handler {
	return &Handler{cfg: cfg}
}

// ServiceView is the wire shape returned by ListServices.
type ServiceView struct {
	Namespace     string   `json:"namespace"`
	Name          string   `json:"name"`
	Hostname      string   `json:"hostname"`
	Tags          string   `json:"tags"`
	PathPrefix    string   `json:"pathPrefix,omitempty"`
	FunnelEnabled bool     `json:"funnelEnabled"`
	FunnelURL     string   `json:"funnelURL,omitempty"`
	Paths         []string `json:"paths"`
	Error         string   `json:"error,omitempty"`
}

// ListServices handles GET /api/services.
func (h *Handler) ListServices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sources, err := h.cfg.Kube.ListSources(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Namespace != sources[j].Namespace {
			return sources[i].Namespace < sources[j].Namespace
		}
		return sources[i].Name < sources[j].Name
	})

	out := make([]ServiceView, 0, len(sources))
	for i := range sources {
		src := &sources[i]
		view := ServiceView{
			Namespace:  src.Namespace,
			Name:       src.Name,
			Hostname:   src.Annotations[manager.AnnHostname],
			Tags:       src.Annotations[manager.AnnTags],
			PathPrefix: src.Annotations[manager.AnnPathPrefix],
			Paths:      extractPaths(src),
		}
		if view.Tags == "" {
			view.Tags = h.cfg.DefaultTags
		}
		if err := manager.ValidateSource(src); err != nil {
			view.Error = err.Error()
		}
		mirror, err := h.cfg.Kube.GetMirror(ctx, src.Namespace, src.Name)
		if err != nil {
			view.Error = err.Error()
		} else if mirror != nil && mirror.Annotations[manager.TSFunnel] == "true" {
			view.FunnelEnabled = true
			if h.cfg.Tailnet != "" && view.Hostname != "" {
				view.FunnelURL = fmt.Sprintf("https://%s.%s%s", view.Hostname, h.cfg.Tailnet, view.PathPrefix)
			}
		}
		out = append(out, view)
	}

	writeJSON(w, http.StatusOK, out)
}

type setFunnelRequest struct {
	Enabled bool `json:"enabled"`
}

// SetFunnel handles POST /api/services/{namespace}/{name}/funnel.
func (h *Handler) SetFunnel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")
	if namespace == "" || name == "" {
		writeError(w, http.StatusBadRequest, errors.New("namespace and name required"))
		return
	}

	var req setFunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid body: %w", err))
		return
	}

	src, err := h.cfg.Kube.GetSource(ctx, namespace, name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if src.Labels[manager.LabelEnabled] != "true" {
		writeError(w, http.StatusForbidden, fmt.Errorf("ingress %s/%s is not opted in (missing %s=true)", namespace, name, manager.LabelEnabled))
		return
	}

	if err := manager.ValidateSource(src); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	mirror, err := h.cfg.Kube.GetMirror(ctx, namespace, name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	if mirror == nil {
		// First toggle for this source — create the mirror in the requested state.
		// The tailnet device is minted here; subsequent toggles only patch the
		// annotation so the device (and its tailnet-lock signature) persist.
		built, err := manager.Build(src, h.cfg.DefaultTags, req.Enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := h.cfg.Kube.CreateMirror(ctx, built); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("mirror created", "namespace", namespace, "name", name, "enabled", req.Enabled)
	} else {
		if err := h.cfg.Kube.PatchFunnel(ctx, namespace, name, req.Enabled); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("funnel patched", "namespace", namespace, "name", name, "enabled", req.Enabled)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"enabled": req.Enabled})
}

func extractPaths(src *networkingv1.Ingress) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range src.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			if _, ok := seen[p.Path]; ok {
				continue
			}
			seen[p.Path] = struct{}{}
			out = append(out, p.Path)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
