package store

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clusterv1 "open-cluster-management.io/api/cluster/v1"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// WatcherStoreLister list the ManagedClusters from ClusterClientWatcherStore
type WatcherStoreLister struct {
	store ClusterClientWatcherStore
}

func NewWatcherStoreLister(store ClusterClientWatcherStore) *WatcherStoreLister {
	return &WatcherStoreLister{
		store: store,
	}
}

// List returns the ManagedClusters from a ClusterClientWatcherStore with list options
func (l *WatcherStoreLister) List(options types.ListOptions) ([]*clusterv1.ManagedCluster, error) {
	opts := metav1.ListOptions{}

	list, err := l.store.List(opts)
	if err != nil {
		return nil, err
	}

	clusters := []*clusterv1.ManagedCluster{}
	for _, cluster := range list.Items {
		// TODO: check event data type
		clusters = append(clusters, &cluster)
	}

	return clusters, nil
}
