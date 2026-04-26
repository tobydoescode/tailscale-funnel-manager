package manager

import (
	"context"
	"log/slog"
	"reflect"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// reconcileClient is the subset of kube.Client the reconciler needs.
// Defined here (instead of importing kube) to avoid an import cycle:
// kube already imports manager for constants.
type reconcileClient interface {
	ListMirrors(ctx context.Context) ([]networkingv1.Ingress, error)
	GetSource(ctx context.Context, namespace, name string) (*networkingv1.Ingress, error)
	UpdateMirror(ctx context.Context, mirror *networkingv1.Ingress) error
	DeleteMirror(ctx context.Context, namespace, sourceName string) error
}

// Reconciler garbage-collects mirror Ingresses whose source is no longer
// opted in. This closes the loop with the "remove via Git" workflow: a
// user removes the funnel-manager.toby.cloud/enabled label in Git, Flux
// applies, and within one reconcile tick the mirror is deleted and the
// tailnet device is decommissioned.
type Reconciler struct {
	Client      reconcileClient
	DefaultTags string
	Interval    time.Duration
}

// Run loops until ctx is done. Safe to call in a goroutine.
func (r *Reconciler) Run(ctx context.Context) {
	interval := r.Interval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	slog.Info("reconciler started", "interval", interval.String())
	// Run once immediately so startup doesn't wait a full interval.
	r.reconcileOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			slog.Info("reconciler stopped")
			return
		case <-t.C:
			r.reconcileOnce(ctx)
		}
	}
}

// reconcileOnce lists all managed mirrors and deletes those whose source
// is missing or has lost the opt-in label. Errors are logged and the
// next mirror is still considered — one broken source shouldn't stall
// cleanup of the rest.
func (r *Reconciler) reconcileOnce(ctx context.Context) {
	mirrors, err := r.Client.ListMirrors(ctx)
	if err != nil {
		slog.Error("reconciler: list mirrors", "err", err)
		return
	}
	for i := range mirrors {
		m := &mirrors[i]
		sourceName := m.Labels[LabelSource]
		if sourceName == "" {
			// Our Build always stamps LabelSource; a mirror without it
			// is from an older version or an external write — leave it.
			continue
		}
		src, reason := r.sourceForMirror(ctx, m.Namespace, sourceName)
		if reason != "" {
			if err := r.Client.DeleteMirror(ctx, m.Namespace, sourceName); err != nil {
				slog.Error("reconciler: delete mirror", "namespace", m.Namespace, "source", sourceName, "err", err)
				continue
			}
			slog.Info("reconciler: deleted orphan mirror", "namespace", m.Namespace, "source", sourceName, "reason", reason)
			continue
		}
		r.syncMirror(ctx, src, m)
	}
}

// sourceForMirror returns the mirror's source and a non-empty deletion reason
// when the mirror should be garbage-collected.
func (r *Reconciler) sourceForMirror(ctx context.Context, namespace, sourceName string) (*networkingv1.Ingress, string) {
	src, err := r.Client.GetSource(ctx, namespace, sourceName)
	if apierrors.IsNotFound(err) {
		return nil, "source ingress deleted"
	}
	if err != nil {
		// Transient error — do not delete on uncertain state.
		slog.Warn("reconciler: get source failed; skipping", "namespace", namespace, "source", sourceName, "err", err)
		return nil, ""
	}
	if src.Labels[LabelEnabled] != "true" {
		return src, "source ingress no longer opted in"
	}
	return src, ""
}

func (r *Reconciler) syncMirror(ctx context.Context, src *networkingv1.Ingress, current *networkingv1.Ingress) {
	if src == nil || current == nil {
		return
	}
	desired, err := BuildDesiredMirror(src, r.DefaultTags, current)
	if err != nil {
		slog.Warn("reconciler: build desired mirror failed; skipping", "namespace", current.Namespace, "source", src.Name, "err", err)
		return
	}
	if !mirrorNeedsUpdate(current, desired) {
		return
	}
	if err := r.Client.UpdateMirror(ctx, desired); err != nil {
		slog.Error("reconciler: update mirror", "namespace", desired.Namespace, "source", src.Name, "err", err)
		return
	}
	slog.Info("reconciler: updated mirror", "namespace", desired.Namespace, "source", src.Name)
}

func mirrorNeedsUpdate(current, desired *networkingv1.Ingress) bool {
	return !reflect.DeepEqual(current.Labels, desired.Labels) ||
		!reflect.DeepEqual(current.Annotations, desired.Annotations) ||
		!reflect.DeepEqual(current.OwnerReferences, desired.OwnerReferences) ||
		!reflect.DeepEqual(current.Spec, desired.Spec)
}
