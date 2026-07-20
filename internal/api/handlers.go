// Package api contains the HTTP handlers for the funnel-manager API.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/kube"
	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
	mirrors, err := h.cfg.Kube.ListMirrors(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Join mirrors by source in memory: two API calls total instead of one
	// GetMirror per source on every poll. Same ownership assertions as
	// GetMirror, minus detection of unmanaged name collisions (the toggle
	// path still catches those).
	mirrorsBySource := make(map[string]*networkingv1.Ingress, len(mirrors))
	for i := range mirrors {
		m := &mirrors[i]
		sourceName := m.Labels[manager.LabelSource]
		if m.Labels[manager.LabelManaged] != "true" || sourceName == "" || m.Name != manager.MirrorName(sourceName) {
			continue
		}
		mirrorsBySource[m.Namespace+"/"+sourceName] = m
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
		mirror := mirrorsBySource[src.Namespace+"/"+src.Name]
		if mirror != nil && mirror.Annotations[manager.TSFunnel] == "true" {
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
	Enabled *bool `json:"enabled"`
}

func decodeSetFunnelRequest(r *http.Request) (bool, error) {
	var req setFunnelRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return false, fmt.Errorf("invalid body: %w", err)
	}
	if req.Enabled == nil {
		return false, errors.New("invalid body: enabled is required")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false, errors.New("invalid body: trailing JSON")
	}
	return *req.Enabled, nil
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

	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	enabled, err := decodeSetFunnelRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	src, err := h.cfg.Kube.GetSource(ctx, namespace, name)
	if apierrors.IsNotFound(err) {
		writeError(w, http.StatusNotFound, err)
		return
	}
	if apierrors.IsForbidden(err) {
		writeError(w, http.StatusForbidden, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
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
		built, err := manager.Build(src, h.cfg.DefaultTags, enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := h.cfg.Kube.CreateMirror(ctx, built); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Re-fetch to re-run GetMirror's ownership checks before
				// patching: the conflicting Ingress may not be ours.
				existing, getErr := h.cfg.Kube.GetMirror(ctx, namespace, name)
				if getErr != nil {
					writeError(w, http.StatusInternalServerError, getErr)
					return
				}
				if existing == nil {
					writeError(w, http.StatusConflict, fmt.Errorf("ingress %s/%s changed while toggling; retry", namespace, manager.MirrorName(name)))
					return
				}
				if err := h.cfg.Kube.PatchFunnel(ctx, namespace, name, enabled); err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				slog.Info("mirror existed after create race; funnel patched", "namespace", namespace, "name", name, "enabled", enabled)
				writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("mirror created", "namespace", namespace, "name", name, "enabled", enabled)
	} else {
		if err := h.cfg.Kube.PatchFunnel(ctx, namespace, name, enabled); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		slog.Info("funnel patched", "namespace", namespace, "name", name, "enabled", enabled)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"enabled": enabled})
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
