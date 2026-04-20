package manager

import (
	"context"
	"errors"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeReconcileClient struct {
	mirrors []networkingv1.Ingress
	sources map[string]*networkingv1.Ingress // "<ns>/<name>"
	deleted []string                         // "<ns>/<source>"
	getErr  error                            // optional transient error for GetSource
}

func (f *fakeReconcileClient) ListMirrors(_ context.Context) ([]networkingv1.Ingress, error) {
	return f.mirrors, nil
}
func (f *fakeReconcileClient) GetSource(_ context.Context, ns, name string) (*networkingv1.Ingress, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if s, ok := f.sources[ns+"/"+name]; ok {
		return s, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "ingresses"}, name)
}
func (f *fakeReconcileClient) DeleteMirror(_ context.Context, ns, sourceName string) error {
	f.deleted = append(f.deleted, ns+"/"+sourceName)
	return nil
}

func mirror(ns, source string) networkingv1.Ingress {
	return networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MirrorName(source),
			Namespace: ns,
			Labels: map[string]string{
				LabelManaged: "true",
				LabelSource:  source,
			},
		},
	}
}

func labeledSource(ns, name string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels: map[string]string{LabelEnabled: "true"},
		},
	}
}

func unlabeledSource(ns, name string) *networkingv1.Ingress {
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
}

func TestReconciler_KeepsMirrorWhenSourceLabeled(t *testing.T) {
	fc := &fakeReconcileClient{
		mirrors: []networkingv1.Ingress{mirror("litellm", "litellm")},
		sources: map[string]*networkingv1.Ingress{
			"litellm/litellm": labeledSource("litellm", "litellm"),
		},
	}
	r := &Reconciler{Client: fc}
	r.reconcileOnce(context.Background())
	if len(fc.deleted) != 0 {
		t.Errorf("unexpected deletions: %v", fc.deleted)
	}
}

func TestReconciler_DeletesWhenSourceMissing(t *testing.T) {
	fc := &fakeReconcileClient{
		mirrors: []networkingv1.Ingress{mirror("litellm", "litellm")},
		sources: map[string]*networkingv1.Ingress{},
	}
	r := &Reconciler{Client: fc}
	r.reconcileOnce(context.Background())
	if len(fc.deleted) != 1 || fc.deleted[0] != "litellm/litellm" {
		t.Errorf("expected 1 deletion of litellm/litellm; got %v", fc.deleted)
	}
}

func TestReconciler_DeletesWhenSourceUnlabeled(t *testing.T) {
	fc := &fakeReconcileClient{
		mirrors: []networkingv1.Ingress{mirror("litellm", "litellm")},
		sources: map[string]*networkingv1.Ingress{
			"litellm/litellm": unlabeledSource("litellm", "litellm"),
		},
	}
	r := &Reconciler{Client: fc}
	r.reconcileOnce(context.Background())
	if len(fc.deleted) != 1 {
		t.Errorf("expected 1 deletion; got %v", fc.deleted)
	}
}

func TestReconciler_KeepsMirrorOnTransientError(t *testing.T) {
	// Don't delete on uncertain state — only on confirmed NotFound or
	// confirmed unlabeled source.
	fc := &fakeReconcileClient{
		mirrors: []networkingv1.Ingress{mirror("litellm", "litellm")},
		getErr:  errors.New("kaboom"),
	}
	r := &Reconciler{Client: fc}
	r.reconcileOnce(context.Background())
	if len(fc.deleted) != 0 {
		t.Errorf("expected no deletions on transient error; got %v", fc.deleted)
	}
}

func TestReconciler_SkipsMirrorWithoutSourceLabel(t *testing.T) {
	// A mirror missing LabelSource is either pre-this-version or
	// externally authored; the reconciler should not guess its owner.
	orphan := mirror("litellm", "litellm")
	delete(orphan.Labels, LabelSource)
	fc := &fakeReconcileClient{mirrors: []networkingv1.Ingress{orphan}}
	r := &Reconciler{Client: fc}
	r.reconcileOnce(context.Background())
	if len(fc.deleted) != 0 {
		t.Errorf("expected no deletions; got %v", fc.deleted)
	}
}
