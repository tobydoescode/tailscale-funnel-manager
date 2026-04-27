package kube

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNewClientUsesKubeconfigWhenProvided(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "config")
	if err := os.WriteFile(kubeconfig, []byte(`
apiVersion: v1
kind: Config
clusters:
- name: local
  cluster:
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
contexts:
- name: local
  context:
    cluster: local
    user: local
current-context: local
users:
- name: local
  user:
    token: test-token
`), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	client, err := NewClient(kubeconfig)
	if err != nil {
		t.Fatalf("NewClient returned error: %v", err)
	}
	if client == nil {
		t.Fatalf("client is nil")
	}
}

func TestNewClientReportsInvalidKubeconfig(t *testing.T) {
	_, err := NewClient(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatalf("expected error for missing kubeconfig")
	}
}

func testMirror(ns, source string, labels map[string]string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      manager.MirrorName(source),
			Namespace: ns,
			Labels:    labels,
		},
	}
}

func TestGetMirrorReturnsManagedSourceMirror(t *testing.T) {
	cs := fake.NewSimpleClientset(testMirror("default", "app", map[string]string{
		manager.LabelManaged: "true",
		manager.LabelSource:  "app",
	}))
	c := NewClientFromClientset(cs)

	got, err := c.GetMirror(context.Background(), "default", "app")
	if err != nil {
		t.Fatalf("GetMirror returned error: %v", err)
	}
	if got == nil || got.Name != "app-funnel" {
		t.Fatalf("mirror = %#v, want app-funnel", got)
	}
}

func TestGetMirrorReturnsNilWhenMissing(t *testing.T) {
	c := NewClientFromClientset(fake.NewSimpleClientset())

	got, err := c.GetMirror(context.Background(), "default", "app")
	if err != nil {
		t.Fatalf("GetMirror returned error: %v", err)
	}
	if got != nil {
		t.Fatalf("mirror = %#v, want nil", got)
	}
}

func TestGetMirrorRejectsUnmanagedNameCollision(t *testing.T) {
	cs := fake.NewSimpleClientset(testMirror("default", "app", map[string]string{}))
	c := NewClientFromClientset(cs)

	got, err := c.GetMirror(context.Background(), "default", "app")
	if err == nil {
		t.Fatalf("expected error for unmanaged name collision")
	}
	if got != nil {
		t.Fatalf("mirror = %#v, want nil on error", got)
	}
}

func TestGetMirrorRejectsMismatchedSourceLabel(t *testing.T) {
	cs := fake.NewSimpleClientset(testMirror("default", "app", map[string]string{
		manager.LabelManaged: "true",
		manager.LabelSource:  "other",
	}))
	c := NewClientFromClientset(cs)

	got, err := c.GetMirror(context.Background(), "default", "app")
	if err == nil {
		t.Fatalf("expected error for mismatched source label")
	}
	if got != nil {
		t.Fatalf("mirror = %#v, want nil on error", got)
	}
}
