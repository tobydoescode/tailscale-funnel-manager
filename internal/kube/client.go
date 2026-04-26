// Package kube wraps the Kubernetes client with the small surface this app needs:
// listing labeled Ingresses and CRUD on the mirror Ingress.
package kube

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tobydoescode/tailscale-funnel-manager/internal/manager"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Client is the narrow interface exposed to handlers.
type Client interface {
	ListSources(ctx context.Context) ([]networkingv1.Ingress, error)
	ListMirrors(ctx context.Context) ([]networkingv1.Ingress, error)
	GetSource(ctx context.Context, namespace, name string) (*networkingv1.Ingress, error)
	GetMirror(ctx context.Context, namespace, sourceName string) (*networkingv1.Ingress, error)
	CreateMirror(ctx context.Context, mirror *networkingv1.Ingress) error
	UpdateMirror(ctx context.Context, mirror *networkingv1.Ingress) error
	PatchFunnel(ctx context.Context, namespace, sourceName string, enabled bool) error
	DeleteMirror(ctx context.Context, namespace, sourceName string) error
	Ready(ctx context.Context) error
}

type client struct {
	cs kubernetes.Interface
}

// NewInClusterClient builds a Client using the pod's in-cluster config.
func NewInClusterClient() (Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return &client{cs: cs}, nil
}

// NewClientFromClientset wraps an existing clientset. Useful in tests.
func NewClientFromClientset(cs kubernetes.Interface) Client {
	return &client{cs: cs}
}

func (c *client) ListSources(ctx context.Context) ([]networkingv1.Ingress, error) {
	list, err := c.cs.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{
		LabelSelector: manager.LabelEnabled + "=true",
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *client) GetSource(ctx context.Context, namespace, name string) (*networkingv1.Ingress, error) {
	return c.cs.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
}

func (c *client) GetMirror(ctx context.Context, namespace, sourceName string) (*networkingv1.Ingress, error) {
	ing, err := c.cs.NetworkingV1().Ingresses(namespace).Get(ctx, manager.MirrorName(sourceName), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Only treat it as "our" mirror if we own it — protects against name collisions.
	if ing.Labels[manager.LabelManaged] != "true" {
		return nil, fmt.Errorf("ingress %s/%s exists but is not managed by funnel-manager", namespace, ing.Name)
	}
	return ing, nil
}

func (c *client) ListMirrors(ctx context.Context) ([]networkingv1.Ingress, error) {
	list, err := c.cs.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{
		LabelSelector: manager.LabelManaged + "=true",
	})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (c *client) CreateMirror(ctx context.Context, mirror *networkingv1.Ingress) error {
	_, err := c.cs.NetworkingV1().Ingresses(mirror.Namespace).Create(ctx, mirror, metav1.CreateOptions{FieldManager: "funnel-manager"})
	return err
}

func (c *client) UpdateMirror(ctx context.Context, mirror *networkingv1.Ingress) error {
	_, err := c.cs.NetworkingV1().Ingresses(mirror.Namespace).Update(ctx, mirror, metav1.UpdateOptions{FieldManager: "funnel-manager"})
	return err
}

// PatchFunnel flips the tailscale.com/funnel annotation on the mirror.
// Uses a merge patch so we touch exactly that one field; the tailnet
// device keeps its node key and stays signed under tailnet-lock.
func (c *client) PatchFunnel(ctx context.Context, namespace, sourceName string, enabled bool) error {
	val := "false"
	if enabled {
		val = "true"
	}
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				manager.TSFunnel: val,
			},
		},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = c.cs.NetworkingV1().Ingresses(namespace).Patch(
		ctx,
		manager.MirrorName(sourceName),
		types.MergePatchType,
		body,
		metav1.PatchOptions{FieldManager: "funnel-manager"},
	)
	return err
}

func (c *client) DeleteMirror(ctx context.Context, namespace, sourceName string) error {
	err := c.cs.NetworkingV1().Ingresses(namespace).Delete(ctx, manager.MirrorName(sourceName), metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// Ready issues a lightweight list call to confirm API access + RBAC.
func (c *client) Ready(ctx context.Context) error {
	_, err := c.cs.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{Limit: 1})
	return err
}
