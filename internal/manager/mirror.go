// Package manager builds the mirror (Tailscale funnel) Ingress spec from a
// source Ingress. It is the single source of truth for how funnel Ingresses
// are shaped.
package manager

import (
	"errors"
	"fmt"
	"strings"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Label and annotation keys used to drive discovery and mirror construction.
const (
	LabelEnabled  = "funnel-manager.toby.cloud/enabled"
	LabelManaged  = "funnel-manager.toby.cloud/managed"
	LabelSource   = "funnel-manager.toby.cloud/source"
	AnnHostname   = "funnel-manager.toby.cloud/hostname"
	AnnTags       = "funnel-manager.toby.cloud/tags"
	AnnPathPrefix = "funnel-manager.toby.cloud/path-prefix"

	TSFunnel   = "tailscale.com/funnel"
	TSTags     = "tailscale.com/tags"
	TSHostname = "tailscale.com/hostname"

	TailscaleIngressClass = "tailscale"
	MirrorNameSuffix      = "-funnel"
)

// ErrMissingHostname is returned when the source Ingress lacks the required
// hostname annotation.
var ErrMissingHostname = errors.New("source Ingress missing annotation " + AnnHostname)

// MirrorName returns the deterministic name for the mirror Ingress.
func MirrorName(source string) string {
	return source + MirrorNameSuffix
}

// Build returns the mirror Ingress for a labeled source Ingress.
// defaultTags is used when the source does not specify tags via annotation.
// funnelEnabled controls the initial value of the tailscale.com/funnel
// annotation — callers patch it later instead of deleting and recreating
// the mirror, so the underlying tailnet device persists across toggles.
func Build(source *networkingv1.Ingress, defaultTags string, funnelEnabled bool) (*networkingv1.Ingress, error) {
	if source == nil {
		return nil, errors.New("source Ingress is nil")
	}
	hostname := source.Annotations[AnnHostname]
	if hostname == "" {
		return nil, ErrMissingHostname
	}

	tags := source.Annotations[AnnTags]
	if tags == "" {
		tags = defaultTags
	}

	tsClass := TailscaleIngressClass
	mirror := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MirrorName(source.Name),
			Namespace: source.Namespace,
			Labels: map[string]string{
				LabelManaged: "true",
				LabelSource:  source.Name,
			},
			Annotations: map[string]string{
				TSFunnel:   funnelAnnotationValue(funnelEnabled),
				TSTags:     tags,
				TSHostname: hostname,
			},
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion:         "networking.k8s.io/v1",
					Kind:               "Ingress",
					Name:               source.Name,
					UID:                source.UID,
					BlockOwnerDeletion: ptr(true),
					Controller:         ptr(true),
				},
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &tsClass,
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{hostname}},
			},
			Rules: buildRules(source, source.Annotations[AnnPathPrefix]),
		},
	}
	return mirror, nil
}

// BuildDesiredMirror returns the mirror that should exist for source while
// preserving the current funnel annotation from an existing mirror.
func BuildDesiredMirror(source *networkingv1.Ingress, defaultTags string, existing *networkingv1.Ingress) (*networkingv1.Ingress, error) {
	if err := ValidateSource(source); err != nil {
		return nil, err
	}
	funnelEnabled := false
	if existing != nil && existing.Annotations[TSFunnel] == "true" {
		funnelEnabled = true
	}
	desired, err := Build(source, defaultTags, funnelEnabled)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return desired, nil
	}
	desired.ResourceVersion = existing.ResourceVersion
	desired.UID = existing.UID
	desired.CreationTimestamp = existing.CreationTimestamp
	desired.Labels = mergeStringMap(existing.Labels, desired.Labels)
	desired.Annotations = mergeStringMap(existing.Annotations, desired.Annotations)
	return desired, nil
}

func mergeStringMap(base, overlay map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

func buildRules(source *networkingv1.Ingress, pathPrefix string) []networkingv1.IngressRule {
	out := make([]networkingv1.IngressRule, 0, len(source.Spec.Rules))
	for _, r := range source.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		rule := networkingv1.IngressRule{
			// Host deliberately stripped: Tailscale assigns the hostname
			// via the tailscale.com/hostname annotation.
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: copyPaths(r.HTTP.Paths, pathPrefix),
				},
			},
		}
		out = append(out, rule)
	}
	return out
}

// copyPaths copies backend paths from the source. If pathPrefix is non-empty
// it replaces each path's Path field with the prefix, preserving the backend.
// This supports restricting the funnel to a subset of the private Ingress's
// surface (e.g., /v1 for LiteLLM).
func copyPaths(in []networkingv1.HTTPIngressPath, pathPrefix string) []networkingv1.HTTPIngressPath {
	out := make([]networkingv1.HTTPIngressPath, 0, len(in))
	for _, p := range in {
		np := p
		if pathPrefix != "" {
			np.Path = pathPrefix
			pt := networkingv1.PathTypePrefix
			np.PathType = &pt
		}
		out = append(out, np)
	}
	return out
}

func ptr[T any](v T) *T { return &v }

// funnelAnnotationValue renders the tailscale.com/funnel annotation value.
// "true" and "false" are the only values Tailscale's operator treats as
// intentional; an absent annotation is ambiguous so we always write one.
func funnelAnnotationValue(enabled bool) string {
	if enabled {
		return "true"
	}
	return "false"
}

// ValidateSource returns an error describing why a labeled source Ingress is
// not ready to be mirrored. Used by the API to surface "eligible but invalid"
// rows in the UI with the toggle disabled.
func ValidateSource(source *networkingv1.Ingress) error {
	if source == nil {
		return errors.New("nil Ingress")
	}
	if source.Annotations[AnnHostname] == "" {
		return ErrMissingHostname
	}
	if len(source.Spec.Rules) == 0 {
		return fmt.Errorf("source Ingress %s/%s has no rules", source.Namespace, source.Name)
	}
	pathCount := countHTTPPaths(source)
	if pathCount == 0 {
		return fmt.Errorf("source Ingress %s/%s has no HTTP paths", source.Namespace, source.Name)
	}
	pathPrefix := source.Annotations[AnnPathPrefix]
	if err := validatePathPrefix(pathPrefix); err != nil {
		return err
	}
	if pathPrefix != "" && pathCount != 1 {
		return fmt.Errorf("source Ingress %s/%s has path-prefix but %d HTTP paths; exactly one path is required", source.Namespace, source.Name, pathCount)
	}
	return nil
}

func validatePathPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if strings.TrimSpace(prefix) != prefix {
		return fmt.Errorf("path-prefix %q must not contain leading or trailing whitespace", prefix)
	}
	if prefix[0] != '/' {
		return fmt.Errorf("path-prefix %q must start with /", prefix)
	}
	for _, r := range prefix {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("path-prefix %q must not contain ASCII control characters", prefix)
		}
	}
	return nil
}

func countHTTPPaths(source *networkingv1.Ingress) int {
	count := 0
	for _, r := range source.Spec.Rules {
		if r.HTTP == nil {
			continue
		}
		count += len(r.HTTP.Paths)
	}
	return count
}
