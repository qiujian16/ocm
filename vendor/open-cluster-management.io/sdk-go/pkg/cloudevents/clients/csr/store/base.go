package store

import (
	"fmt"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	certificatev1 "k8s.io/api/certificates/v1"
)

const CSRFinalizer = "cloudevents.open-cluster-management.io/certificatesigningrequest-cleanup"

type baseStore struct {
	sync.RWMutex

	store     cache.Store
	initiated bool
}

// List the csrs from the store with the list options
func (b *baseStore) List(opts metav1.ListOptions) (*certificatev1.CertificateSigningRequestList, error) {
	b.RLock()
	defer b.RUnlock()

	csrs, err := ListCSRsWithOptions(b.store, opts)
	if err != nil {
		return nil, err
	}

	items := []certificatev1.CertificateSigningRequest{}
	for _, csr := range csrs {
		items = append(items, *csr)
	}

	return &certificatev1.CertificateSigningRequestList{Items: items}, nil
}

// Get a csr from the store
func (b *baseStore) Get(name string) (*certificatev1.CertificateSigningRequest, bool, error) {
	b.RLock()
	defer b.RUnlock()

	obj, exists, err := b.store.GetByKey(name)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		return nil, false, nil
	}

	csr, ok := obj.(*certificatev1.CertificateSigningRequest)
	if !ok {
		return nil, false, fmt.Errorf("unknown type %T", obj)
	}

	return csr, true, nil
}

// List all of csr from the store
func (b *baseStore) ListAll() ([]*certificatev1.CertificateSigningRequest, error) {
	b.RLock()
	defer b.RUnlock()

	csrs := []*certificatev1.CertificateSigningRequest{}
	for _, obj := range b.store.List() {
		if csr, ok := obj.(*certificatev1.CertificateSigningRequest); ok {
			csrs = append(csrs, csr)
		}
	}

	return csrs, nil
}

// csrWatcher implements the watch.Interface.
type csrWatcher struct {
	sync.RWMutex

	result  chan watch.Event
	done    chan struct{}
	stopped bool
}

var _ watch.Interface = &csrWatcher{}

func newCSRWatcher() *csrWatcher {
	return &csrWatcher{
		// It's easy for a consumer to add buffering via an extra
		// goroutine/channel, but impossible for them to remove it,
		// so nonbuffered is better.
		result: make(chan watch.Event),
		// If the watcher is externally stopped there is no receiver anymore
		// and the send operations on the result channel, especially the
		// error reporting might block forever.
		// Therefore a dedicated stop channel is used to resolve this blocking.
		done: make(chan struct{}),
	}
}

// ResultChan implements Interface.
func (w *csrWatcher) ResultChan() <-chan watch.Event {
	return w.result
}

// Stop implements Interface.
func (w *csrWatcher) Stop() {
	// Call Close() exactly once by locking and setting a flag.
	w.Lock()
	defer w.Unlock()
	// closing a closed channel always panics, therefore check before closing
	select {
	case <-w.done:
		close(w.result)
	default:
		w.stopped = true
		close(w.done)
	}
}

// Receive a event from the csr client and sends down the result channel.
func (w *csrWatcher) Receive(evt watch.Event) {
	if w.isStopped() {
		// this watcher is stopped, do nothing.
		return
	}

	if klog.V(4).Enabled() {
		obj, _ := meta.Accessor(evt.Object)
		klog.V(4).Infof("Receive the event %v for %v", evt.Type, obj.GetName())
	}

	w.result <- evt
}

func (w *csrWatcher) isStopped() bool {
	w.RLock()
	defer w.RUnlock()

	return w.stopped
}

// ListCSRsWithOptions retrieves the csrs from store which matches the options.
func ListCSRsWithOptions(store cache.Store, opts metav1.ListOptions) ([]*certificatev1.CertificateSigningRequest, error) {
	var err error

	labelSelector := labels.Everything()
	fieldSelector := fields.Everything()

	if len(opts.LabelSelector) != 0 {
		labelSelector, err = labels.Parse(opts.LabelSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid labels selector %q: %v", opts.LabelSelector, err)
		}
	}

	if len(opts.FieldSelector) != 0 {
		fieldSelector, err = fields.ParseSelector(opts.FieldSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid fields selector %q: %v", opts.FieldSelector, err)
		}
	}

	csrs := []*certificatev1.CertificateSigningRequest{}
	// list with labels
	if err := cache.ListAll(store, labelSelector, func(obj interface{}) {
		csr, ok := obj.(*certificatev1.CertificateSigningRequest)
		if !ok {
			return
		}

		csrFieldSet := fields.Set{
			"metadata.name": csr.Name,
		}

		if !fieldSelector.Matches(csrFieldSet) {
			return
		}

		csrs = append(csrs, csr)
	}); err != nil {
		return nil, err
	}

	return csrs, nil
}
