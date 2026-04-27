package main

import "testing"

func TestKubeconfigPathPrefersFunnelManagerEnv(t *testing.T) {
	t.Setenv("FUNNEL_MANAGER_KUBECONFIG", "/tmp/funnel-manager")
	t.Setenv("KUBECONFIG", "/tmp/standard")

	if got := kubeconfigPath(); got != "/tmp/funnel-manager" {
		t.Fatalf("kubeconfigPath() = %q, want FUNNEL_MANAGER_KUBECONFIG", got)
	}
}

func TestKubeconfigPathFallsBackToStandardEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "/tmp/standard")

	if got := kubeconfigPath(); got != "/tmp/standard" {
		t.Fatalf("kubeconfigPath() = %q, want KUBECONFIG", got)
	}
}

func TestKubeconfigPathEmptyForInClusterDefault(t *testing.T) {
	if got := kubeconfigPath(); got != "" {
		t.Fatalf("kubeconfigPath() = %q, want empty", got)
	}
}
