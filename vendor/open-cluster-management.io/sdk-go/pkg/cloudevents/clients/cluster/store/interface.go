package store

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

const syncedPollPeriod = 100 * time.Millisecond

// StoreInitiated is a function that can be used to determine if a store has initiated.
type StoreInitiated func() bool

// ClusterClientWatcherStore provides a watcher with a cluster store.
type ClusterClientWatcherStore interface {
	// GetWatcher returns a watcher to receive cluster changes.
	GetWatcher(opts metav1.ListOptions) (watch.Interface, error)

	// HandleReceivedCluster handles the client received cluster events.
	HandleReceivedCluster(action types.ResourceAction, cluster *clusterv1.ManagedCluster) error

	// Add will be called by cluster client when adding cluster. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Add(cluster *clusterv1.ManagedCluster) error

	// Update will be called by cluster client when updating cluster. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Update(cluster *clusterv1.ManagedCluster) error

	// Delete will be called by cluster client when deleting cluster. The implementation is based on the specific
	// watcher store, in some case, it does not need to update a store, but just send a watch event.
	Delete(cluster *clusterv1.ManagedCluster) error

	// List returns the clusters from store with list options
	List(opts metav1.ListOptions) (*clusterv1.ManagedClusterList, error)

	// ListAll list all of the clusters from store
	ListAll() ([]*clusterv1.ManagedCluster, error)

	// Get returns a cluster from store with cluster name
	Get(name string) (*clusterv1.ManagedCluster, bool, error)

	// HasInitiated marks the store has been initiated, A resync may be required after the store is initiated
	// when building a cluster client.
	HasInitiated() bool
}

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
