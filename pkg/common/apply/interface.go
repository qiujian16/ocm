package apply

import (
	"context"
	"fmt"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourcehelper"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Getter is a wrapper interface of lister
type Getter[T runtime.Object] interface {
	Get(name string) (T, error)
}

// Client is a wrapper interface of client
type Client[T runtime.Object] interface {
	Create(ctx context.Context, obj T, opts metav1.CreateOptions) (T, error)

	Update(ctx context.Context, obj T, opts metav1.UpdateOptions) (T, error)
}

// CompareFunc compares required and existing, returns the updated required
// and whether updated is needed
type CompareFunc[T runtime.Object] func(required, existing T) (T, bool)

type Applier[T runtime.Object] interface {
	Apply(ctx context.Context, required T, recorder events.Recorder) (runtime.Object, bool, error)
}

// applier implements Applier
type applier[T runtime.Object] struct {
	getter  Getter[T]
	client  Client[T]
	compare CompareFunc[T]
}

func NewApplier[T runtime.Object](getter Getter[T], client Client[T], compareFunc CompareFunc[T]) Applier[T] {
	return &applier[T]{
		getter:  getter,
		client:  client,
		compare: compareFunc,
	}
}

func (a *applier[T]) Apply(ctx context.Context, required T, recorder events.Recorder) (runtime.Object, bool, error) {
	requiredAccessor, err := meta.Accessor(required)
	if err != nil {
		return nil, false, err
	}
	gvk := resourcehelper.GuessObjectGroupVersionKind(required)
	existing, err := a.getter.Get(requiredAccessor.GetName())
	if errors.IsNotFound(err) {
		actual, createErr := a.client.Create(ctx, required, metav1.CreateOptions{})
		if errors.IsAlreadyExists(createErr) {
			return required, false, nil
		}
		if createErr == nil {
			recorder.Eventf(fmt.Sprintf("%sCreated", gvk.Kind), "Created %s because it was missing", resourcehelper.FormatResourceForCLIWithNamespace(actual))
		} else {
			recorder.Warningf(fmt.Sprintf("%sCreateFailed", gvk.Kind), "Failed to create %s: %v", resourcehelper.FormatResourceForCLIWithNamespace(required), createErr)
		}

		return actual, true, createErr
	}

	updated, modified := a.compare(required, existing)
	if !modified {
		return updated, modified, nil
	}

	updated, err = a.client.Update(ctx, updated, metav1.UpdateOptions{})
	switch {
	case err != nil:
		recorder.Warningf(fmt.Sprintf("%sUpdateFailed", gvk.Kind), "Failed to update %s: %v", resourcehelper.FormatResourceForCLIWithNamespace(required), err)
	default:
		recorder.Eventf(fmt.Sprintf("%sUpdated", gvk.Kind), "Updated %s:\n%s", resourcehelper.FormatResourceForCLIWithNamespace(updated))
	}

	return updated, modified, err
}
