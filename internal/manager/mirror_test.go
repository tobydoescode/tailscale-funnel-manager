package manager

import (
	"errors"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func sourceIngress() *networkingv1.Ingress {
	pt := networkingv1.PathTypePrefix
	tfk := "traefik"
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "litellm",
			Namespace: "litellm",
			UID:       types.UID("abc-123"),
			Labels: map[string]string{
				LabelEnabled: "true",
			},
			Annotations: map[string]string{
				AnnHostname: "litellm",
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &tfk,
			Rules: []networkingv1.IngressRule{
				{
					Host: "litellm.example.com",
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pt,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: "litellm-svc",
											Port: networkingv1.ServiceBackendPort{Number: 4000},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

func TestBuild_DefaultsAndCoreFields(t *testing.T) {
	src := sourceIngress()
	mirror, err := Build(src, "tag:default-funnel", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mirror.Name != "litellm-funnel" {
		t.Errorf("name = %q, want litellm-funnel", mirror.Name)
	}
	if mirror.Namespace != "litellm" {
		t.Errorf("namespace = %q, want litellm", mirror.Namespace)
	}
	if mirror.Spec.IngressClassName == nil || *mirror.Spec.IngressClassName != "tailscale" {
		t.Errorf("ingressClassName not tailscale: %+v", mirror.Spec.IngressClassName)
	}
	if got := mirror.Annotations[TSFunnel]; got != "true" {
		t.Errorf("tailscale.com/funnel = %q, want true", got)
	}
	if got := mirror.Annotations[TSHostname]; got != "litellm" {
		t.Errorf("tailscale.com/hostname = %q, want litellm", got)
	}
	if got := mirror.Annotations[TSTags]; got != "tag:default-funnel" {
		t.Errorf("tailscale.com/tags = %q, want default", got)
	}
	if got := mirror.Labels[LabelManaged]; got != "true" {
		t.Errorf("managed label = %q, want true", got)
	}
	if got := mirror.Labels[LabelSource]; got != "litellm" {
		t.Errorf("source label = %q, want litellm", got)
	}
}

func TestBuild_TagsOverride(t *testing.T) {
	src := sourceIngress()
	src.Annotations[AnnTags] = "tag:override"
	mirror, err := Build(src, "tag:default-funnel", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := mirror.Annotations[TSTags]; got != "tag:override" {
		t.Errorf("tags = %q, want override", got)
	}
}

func TestBuild_StripsHostAndCopiesBackend(t *testing.T) {
	src := sourceIngress()
	mirror, err := Build(src, "tag:x", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mirror.Spec.Rules) != 1 {
		t.Fatalf("rules len = %d, want 1", len(mirror.Spec.Rules))
	}
	rule := mirror.Spec.Rules[0]
	if rule.Host != "" {
		t.Errorf("host = %q, want empty", rule.Host)
	}
	if rule.HTTP == nil || len(rule.HTTP.Paths) != 1 {
		t.Fatalf("http/paths not preserved: %+v", rule.HTTP)
	}
	p := rule.HTTP.Paths[0]
	if p.Path != "/" {
		t.Errorf("path = %q, want /", p.Path)
	}
	if p.Backend.Service == nil || p.Backend.Service.Name != "litellm-svc" {
		t.Errorf("backend not copied: %+v", p.Backend)
	}
	if p.Backend.Service.Port.Number != 4000 {
		t.Errorf("port = %d, want 4000", p.Backend.Service.Port.Number)
	}
}

func TestBuild_PathPrefixOverride(t *testing.T) {
	src := sourceIngress()
	src.Annotations[AnnPathPrefix] = "/v1"
	mirror, err := Build(src, "tag:x", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	paths := mirror.Spec.Rules[0].HTTP.Paths
	if len(paths) != 1 {
		t.Fatalf("paths len = %d, want 1", len(paths))
	}
	if paths[0].Path != "/v1" {
		t.Errorf("path = %q, want /v1", paths[0].Path)
	}
	if paths[0].PathType == nil || *paths[0].PathType != networkingv1.PathTypePrefix {
		t.Errorf("pathType = %v, want Prefix", paths[0].PathType)
	}
	// Backend must be preserved when paths are overridden.
	if paths[0].Backend.Service.Name != "litellm-svc" || paths[0].Backend.Service.Port.Number != 4000 {
		t.Errorf("backend not preserved under path-prefix override: %+v", paths[0].Backend)
	}
}

func TestBuild_TLSRewritesToTailscaleHostname(t *testing.T) {
	src := sourceIngress()
	src.Spec.TLS = []networkingv1.IngressTLS{
		{Hosts: []string{"litellm.example.com"}, SecretName: "litellm-tls"},
	}
	mirror, err := Build(src, "tag:x", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mirror.Spec.TLS) != 1 {
		t.Fatalf("tls len = %d, want 1", len(mirror.Spec.TLS))
	}
	tls := mirror.Spec.TLS[0]
	if len(tls.Hosts) != 1 || tls.Hosts[0] != "litellm" {
		t.Errorf("tls hosts = %v, want [litellm]", tls.Hosts)
	}
	if tls.SecretName != "" {
		t.Errorf("tls secretName = %q, want empty (Tailscale-managed)", tls.SecretName)
	}
}

func TestBuild_OwnerReference(t *testing.T) {
	src := sourceIngress()
	mirror, err := Build(src, "tag:x", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mirror.OwnerReferences) != 1 {
		t.Fatalf("ownerRefs len = %d, want 1", len(mirror.OwnerReferences))
	}
	or := mirror.OwnerReferences[0]
	if or.Kind != "Ingress" || or.Name != "litellm" || or.UID != "abc-123" {
		t.Errorf("ownerRef mismatch: %+v", or)
	}
	if or.Controller == nil || !*or.Controller {
		t.Errorf("ownerRef.Controller = %v, want true", or.Controller)
	}
}

func TestBuild_FunnelDisabled(t *testing.T) {
	src := sourceIngress()
	mirror, err := Build(src, "tag:x", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := mirror.Annotations[TSFunnel]; got != "false" {
		t.Errorf("tailscale.com/funnel = %q, want false", got)
	}
}

func TestBuild_MissingHostname(t *testing.T) {
	src := sourceIngress()
	delete(src.Annotations, AnnHostname)
	_, err := Build(src, "tag:x", true)
	if !errors.Is(err, ErrMissingHostname) {
		t.Errorf("err = %v, want ErrMissingHostname", err)
	}
}

func TestValidateSource(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if err := ValidateSource(sourceIngress()); err != nil {
			t.Errorf("unexpected err: %v", err)
		}
	})
	t.Run("missing hostname", func(t *testing.T) {
		src := sourceIngress()
		delete(src.Annotations, AnnHostname)
		if err := ValidateSource(src); !errors.Is(err, ErrMissingHostname) {
			t.Errorf("err = %v, want ErrMissingHostname", err)
		}
	})
	t.Run("no rules", func(t *testing.T) {
		src := sourceIngress()
		src.Spec.Rules = nil
		if err := ValidateSource(src); err == nil {
			t.Errorf("expected error for no rules")
		}
	})
	t.Run("no http paths", func(t *testing.T) {
		src := sourceIngress()
		src.Spec.Rules[0].HTTP = nil
		if err := ValidateSource(src); err == nil {
			t.Errorf("expected error for no HTTP paths")
		}
	})
}

func TestMirrorName(t *testing.T) {
	if got := MirrorName("litellm"); got != "litellm-funnel" {
		t.Errorf("MirrorName = %q, want litellm-funnel", got)
	}
}
