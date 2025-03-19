package store

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/util/wait"
	"time"

	certificatev1 "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// AgentInformerWatcherStore extends the baseStore.
// It gets/lists the csrs from the given informer store and send
// the cs add/update/delete event to the watch channel directly.
//
// It is used for building csr agent client.
type AgentInformerWatcherStore struct {
	baseStore
	informer cache.SharedIndexInformer
	watcher  *csrWatcher
}

const syncedPollPeriod = 100 * time.Millisecond

// StoreInitiated is a function that can be used to determine if a store has initiated.
type StoreInitiated func() bool

func WaitForStoreInit(ctx context.Context, cacheSyncs ...StoreInitiated) bool {
	err := wait.PollUntilContextCancel(
		ctx,
		syncedPollPeriod,
		true,
		func(ctx context.Context) (bool, error) {
			for _, syncFunc := range cacheSyncs {
				if !syncFunc() {
					return false, nil
				}
			}
			return true, nil
		},
	)
	if err != nil {
		klog.Errorf("stop WaitForStoreInit, %v", err)
		return false
	}

	return true
}

type CSRClientWatcherStore interface {
	// GetWatcher returns a watcher to receive csr changes.
	GetWatcher(opts metav1.ListOptions) (watch.Interface, error)

	// HandleReceivedCSR handles the client received csr events.
	HandleReceivedCSR(action types.ResourceAction, csr *certificatev1.CertificateSigningRequest) error

	// Add will be called by csr client when adding csr. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Add(csr *certificatev1.CertificateSigningRequest) error

	// Update will be called by csr client when updating csr. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Update(csr *certificatev1.CertificateSigningRequest) error

	// Delete will be called by csr client when deleting csr. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Delete(csr *certificatev1.CertificateSigningRequest) error

	// List returns the csrs from store with list options
	List(opts metav1.ListOptions) (*certificatev1.CertificateSigningRequestList, error)

	// ListAll list all of the csrs from store
	ListAll() ([]*certificatev1.CertificateSigningRequest, error)

	// Get returns a csr from store with csr name
	Get(name string) (*certificatev1.CertificateSigningRequest, bool, error)

	// HasInitiated marks the store has been initiated, A resync may be required after the store is initiated
	// when building a csr client.
	HasInitiated() bool
}

var _ CSRClientWatcherStore = &AgentInformerWatcherStore{}

func NewAgentInformerWatcherStore() *AgentInformerWatcherStore {
	return &AgentInformerWatcherStore{
		baseStore: baseStore{},
		watcher:   newCSRWatcher(),
	}
}

func (s *AgentInformerWatcherStore) Add(csr *certificatev1.CertificateSigningRequest) error {
	s.watcher.Receive(watch.Event{Type: watch.Added, Object: csr})
	return nil
}

func (s *AgentInformerWatcherStore) Update(csr *certificatev1.CertificateSigningRequest) error {
	s.watcher.Receive(watch.Event{Type: watch.Modified, Object: csr})
	return nil
}

func (s *AgentInformerWatcherStore) Delete(csr *certificatev1.CertificateSigningRequest) error {
	s.watcher.Receive(watch.Event{Type: watch.Deleted, Object: csr})
	return nil
}

func (s *AgentInformerWatcherStore) HandleReceivedCSR(action types.ResourceAction, csr *certificatev1.CertificateSigningRequest) error {
	switch action {
	case types.Added:
		return s.Add(csr.DeepCopy())
	case types.Modified:
		lastCSR, exists, err := s.Get(csr.Name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("the csr %s does not exist", csr.Name)
		}
		// prevent the csr from being updated if it is deleting
		if !lastCSR.GetDeletionTimestamp().IsZero() {
			klog.Warningf("the csr %s is deleting, ignore the update", csr.Name)
			return nil
		}

		updatedCSR := csr.DeepCopy()

		// restore the fields that are maintained by local agent
		updatedCSR.Labels = lastCSR.Labels
		updatedCSR.Annotations = lastCSR.Annotations
		updatedCSR.Finalizers = lastCSR.Finalizers
		updatedCSR.Status = lastCSR.Status

		return s.Update(updatedCSR)
	case types.Deleted:
		// the csr is deleting on the source, we just update its deletion timestamp.
		lastCSR, exists, err := s.Get(csr.Name)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}

		updatedCSR := lastCSR.DeepCopy()
		updatedCSR.DeletionTimestamp = csr.DeletionTimestamp
		return s.Update(updatedCSR)
	default:
		return fmt.Errorf("unsupported resource action %s", action)
	}
}

func (s *AgentInformerWatcherStore) GetWatcher(opts metav1.ListOptions) (watch.Interface, error) {
	return s.watcher, nil
}

func (s *AgentInformerWatcherStore) HasInitiated() bool {
	return s.initiated && s.informer.HasSynced()
}

func (s *AgentInformerWatcherStore) SetInformer(informer cache.SharedIndexInformer) {
	s.informer = informer
	s.store = informer.GetStore()
	s.initiated = true
}
