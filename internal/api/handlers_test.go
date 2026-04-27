package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

// fakeKube implements kube.Client for tests without pulling in client-go fakes.
type fakeKube struct {
	mu                   sync.Mutex
	sources              []networkingv1.Ingress
	mirrors              map[string]*networkingv1.Ingress // key "<ns>/<mirror-name>"
	createErr            error
	createConflictMirror *networkingv1.Ingress
	getMirrorErr         error
	patches              int
}

func newFake(src ...networkingv1.Ingress) *fakeKube {
	return &fakeKube{sources: src, mirrors: map[string]*networkingv1.Ingress{}}
}

func (f *fakeKube) key(ns, mirrorName string) string { return ns + "/" + mirrorName }

func (f *fakeKube) ListSources(_ context.Context) ([]networkingv1.Ingress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]networkingv1.Ingress, len(f.sources))
	copy(out, f.sources)
	return out, nil
}

func (f *fakeKube) GetSource(_ context.Context, ns, name string) (*networkingv1.Ingress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.sources {
		if f.sources[i].Namespace == ns && f.sources[i].Name == name {
			cp := f.sources[i]
			return &cp, nil
		}
	}
	return nil, &notFoundError{}
}

func (f *fakeKube) GetMirror(_ context.Context, ns, sourceName string) (*networkingv1.Ingress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getMirrorErr != nil {
		return nil, f.getMirrorErr
	}
	return f.mirrors[f.key(ns, manager.MirrorName(sourceName))], nil
}

func (f *fakeKube) CreateMirror(_ context.Context, m *networkingv1.Ingress) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		if f.createConflictMirror != nil {
			f.mirrors[f.key(f.createConflictMirror.Namespace, f.createConflictMirror.Name)] = f.createConflictMirror
		}
		return f.createErr
	}
	// Simulate server-assigned UID so tests can detect accidental recreation.
	if m.UID == "" {
		m.UID = types.UID(fmt.Sprintf("mirror-uid-%d", len(f.mirrors)+1))
	}
	f.mirrors[f.key(m.Namespace, m.Name)] = m
	return nil
}

func (f *fakeKube) UpdateMirror(_ context.Context, m *networkingv1.Ingress) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mirrors[f.key(m.Namespace, m.Name)] = m
	return nil
}

func (f *fakeKube) DeleteMirror(_ context.Context, ns, sourceName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.mirrors, f.key(ns, manager.MirrorName(sourceName)))
	return nil
}

func (f *fakeKube) ListMirrors(_ context.Context) ([]networkingv1.Ingress, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]networkingv1.Ingress, 0, len(f.mirrors))
	for _, m := range f.mirrors {
		out = append(out, *m)
	}
	return out, nil
}

func (f *fakeKube) PatchFunnel(_ context.Context, ns, sourceName string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches++
	m, ok := f.mirrors[f.key(ns, manager.MirrorName(sourceName))]
	if !ok {
		return &notFoundError{}
	}
	if m.Annotations == nil {
		m.Annotations = map[string]string{}
	}
	if enabled {
		m.Annotations[manager.TSFunnel] = "true"
	} else {
		m.Annotations[manager.TSFunnel] = "false"
	}
	return nil
}

func (f *fakeKube) Ready(_ context.Context) error { return nil }

type notFoundError struct{}

func (notFoundError) Error() string { return "not found" }

func sampleSource() networkingv1.Ingress {
	pt := networkingv1.PathTypePrefix
	return networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "litellm",
			Namespace: "litellm",
			UID:       types.UID("uid-1"),
			Labels:    map[string]string{manager.LabelEnabled: "true"},
			Annotations: map[string]string{
				manager.AnnHostname:   "litellm",
				manager.AnnPathPrefix: "/v1",
			},
		},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				Host: "litellm.example.com",
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pt,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: "litellm-svc",
									Port: networkingv1.ServiceBackendPort{Number: 4000},
								},
							},
						}},
					},
				},
			}},
		},
	}
}

func TestListServices(t *testing.T) {
	fk := newFake(sampleSource())
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default", Tailnet: "tail-abc.ts.net"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/services", nil)
	h.ListServices(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []ServiceView
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	v := got[0]
	if v.Namespace != "litellm" || v.Name != "litellm" {
		t.Errorf("ns/name: %+v", v)
	}
	if v.Hostname != "litellm" || v.PathPrefix != "/v1" || v.Tags != "tag:default" {
		t.Errorf("annotations not surfaced: %+v", v)
	}
	if v.FunnelEnabled {
		t.Errorf("funnel should be disabled initially")
	}
	if v.FunnelURL != "" {
		t.Errorf("funnelURL = %q, want empty when disabled", v.FunnelURL)
	}
	if len(v.Paths) != 1 || v.Paths[0] != "/" {
		t.Errorf("paths: %+v", v.Paths)
	}
}

func postFunnel(h *Handler, enabled bool) *httptest.ResponseRecorder {
	body := `{"enabled":false}`
	if enabled {
		body = `{"enabled":true}`
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/services/litellm/litellm/funnel", strings.NewReader(body))
	req.SetPathValue("namespace", "litellm")
	req.SetPathValue("name", "litellm")
	h.SetFunnel(rec, req)
	return rec
}

// First enable creates the mirror; subsequent toggles patch in-place so the
// underlying tailnet device (and its tailnet-lock signature) is not churned.
func TestSetFunnel_CreateOnFirstEnable(t *testing.T) {
	fk := newFake(sampleSource())
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default", Tailnet: "tail-abc.ts.net"})

	rec := postFunnel(h, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, body=%s", rec.Code, rec.Body.String())
	}

	m := fk.mirrors["litellm/litellm-funnel"]
	if m == nil {
		t.Fatalf("mirror not created")
	}
	if m.Annotations[manager.TSFunnel] != "true" {
		t.Errorf("funnel annotation = %q, want true", m.Annotations[manager.TSFunnel])
	}
	if m.Labels[manager.LabelManaged] != "true" {
		t.Errorf("managed label missing: %+v", m.Labels)
	}
	if len(m.Spec.Rules) != 1 || m.Spec.Rules[0].HTTP.Paths[0].Path != "/v1" {
		t.Errorf("path-prefix not honored: %+v", m.Spec.Rules)
	}

	rec2 := httptest.NewRecorder()
	h.ListServices(rec2, httptest.NewRequest("GET", "/api/services", nil))
	var views []ServiceView
	_ = json.NewDecoder(rec2.Body).Decode(&views)
	if !views[0].FunnelEnabled {
		t.Errorf("expected funnelEnabled after create")
	}
	if views[0].FunnelURL != "https://litellm.tail-abc.ts.net/v1" {
		t.Errorf("funnelURL = %q", views[0].FunnelURL)
	}
}

func TestSetFunnel_CreateOnFirstDisable(t *testing.T) {
	// Toggling OFF on a source that has never been mirrored still creates
	// the mirror (with funnel=false) so state is explicit. Alternatively
	// this could be a no-op; we prefer an explicit record.
	fk := newFake(sampleSource())
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := postFunnel(h, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, body=%s", rec.Code, rec.Body.String())
	}
	m := fk.mirrors["litellm/litellm-funnel"]
	if m == nil {
		t.Fatalf("mirror not created on first disable")
	}
	if got := m.Annotations[manager.TSFunnel]; got != "false" {
		t.Errorf("funnel annotation = %q, want false", got)
	}
}

func TestSetFunnel_PatchInPlace(t *testing.T) {
	// After the mirror exists, toggles must patch the existing resource
	// rather than delete/recreate — otherwise tailnet-lock forces a fresh
	// device signing on every cycle.
	fk := newFake(sampleSource())
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default", Tailnet: "tail-abc.ts.net"})

	if rec := postFunnel(h, true); rec.Code != http.StatusOK {
		t.Fatalf("enable: %s", rec.Body.String())
	}
	originalUID := fk.mirrors["litellm/litellm-funnel"].UID

	if rec := postFunnel(h, false); rec.Code != http.StatusOK {
		t.Fatalf("disable: %s", rec.Body.String())
	}
	m := fk.mirrors["litellm/litellm-funnel"]
	if m == nil {
		t.Fatalf("mirror deleted on disable — should be patched")
	}
	if got := m.Annotations[manager.TSFunnel]; got != "false" {
		t.Errorf("funnel annotation after disable = %q, want false", got)
	}
	if m.UID != originalUID {
		t.Errorf("mirror replaced (UID changed) instead of patched: %q -> %q", originalUID, m.UID)
	}

	if rec := postFunnel(h, true); rec.Code != http.StatusOK {
		t.Fatalf("re-enable: %s", rec.Body.String())
	}
	if got := fk.mirrors["litellm/litellm-funnel"].Annotations[manager.TSFunnel]; got != "true" {
		t.Errorf("re-enable annotation = %q, want true", got)
	}

	// ListServices must only report enabled when annotation is actually "true".
	rec2 := httptest.NewRecorder()
	h.ListServices(rec2, httptest.NewRequest("GET", "/api/services", nil))
	var views []ServiceView
	_ = json.NewDecoder(rec2.Body).Decode(&views)
	if !views[0].FunnelEnabled {
		t.Errorf("expected funnelEnabled after re-enable")
	}

	// Simulate annotation=false and check the view reflects it.
	fk.mirrors["litellm/litellm-funnel"].Annotations[manager.TSFunnel] = "false"
	rec3 := httptest.NewRecorder()
	h.ListServices(rec3, httptest.NewRequest("GET", "/api/services", nil))
	_ = json.NewDecoder(rec3.Body).Decode(&views)
	if views[0].FunnelEnabled {
		t.Errorf("expected funnelEnabled=false when annotation=false")
	}
}

func TestSetFunnel_PatchesAfterCreateConflict(t *testing.T) {
	fk := newFake(sampleSource())
	existing, err := manager.Build(&fk.sources[0], "tag:default", false)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	fk.createConflictMirror = existing
	fk.createErr = apierrors.NewAlreadyExists(schema.GroupResource{Resource: "ingresses"}, "litellm-funnel")
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := postFunnel(h, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := fk.mirrors["litellm/litellm-funnel"].Annotations[manager.TSFunnel]; got != "true" {
		t.Fatalf("funnel annotation = %q, want true", got)
	}
}

func TestSetFunnel_DoesNotPatchWhenMirrorOwnershipInvalid(t *testing.T) {
	fk := newFake(sampleSource())
	fk.getMirrorErr = fmt.Errorf("ingress litellm/litellm-funnel exists but source label is %q, want %q", "other", "litellm")
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := postFunnel(h, true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rec.Code, rec.Body.String())
	}
	if fk.patches != 0 {
		t.Fatalf("patches = %d, want 0", fk.patches)
	}
}

func TestSetFunnel_RefusesUnlabeled(t *testing.T) {
	// Source exists but lacks the opt-in label.
	unlabeled := sampleSource()
	unlabeled.Labels = nil
	fk := newFake(unlabeled)
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/services/litellm/litellm/funnel", strings.NewReader(`{"enabled":true}`))
	req.SetPathValue("namespace", "litellm")
	req.SetPathValue("name", "litellm")
	h.SetFunnel(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestSetFunnel_RefusesAmbiguousPathPrefix(t *testing.T) {
	src := sampleSource()
	src.Spec.Rules[0].HTTP.Paths = append(src.Spec.Rules[0].HTTP.Paths, src.Spec.Rules[0].HTTP.Paths[0])
	src.Spec.Rules[0].HTTP.Paths[1].Path = "/admin"
	src.Spec.Rules[0].HTTP.Paths[1].Backend.Service.Name = "admin-svc"
	fk := newFake(src)
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := postFunnel(h, true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(fk.mirrors) != 0 {
		t.Fatalf("mirror should not be created for ambiguous path-prefix source")
	}
}

func TestSetFunnel_BadBody(t *testing.T) {
	fk := newFake(sampleSource())
	h := NewHandler(Config{Kube: fk, DefaultTags: "tag:default"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/services/litellm/litellm/funnel", strings.NewReader(`not-json`))
	req.SetPathValue("namespace", "litellm")
	req.SetPathValue("name", "litellm")
	h.SetFunnel(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
