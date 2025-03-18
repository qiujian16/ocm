package store

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// AgentInformerWatcherStore extends the baseStore.
// It gets/lists the clusters from the given informer store and send
// the cluster add/update/delete event to the watch channel directly.
//
// It is used for building ManagedCluster agent client.
type AgentInformerWatcherStore struct {
	baseStore
	informer cache.SharedIndexInformer
	watcher  *clusterWatcher
}

var _ ClusterClientWatcherStore = &AgentInformerWatcherStore{}

func NewAgentInformerWatcherStore() *AgentInformerWatcherStore {
	return &AgentInformerWatcherStore{
		baseStore: baseStore{},
		watcher:   newClusterWatcher(),
	}
}

func (s *AgentInformerWatcherStore) Add(cluster *clusterv1.ManagedCluster) error {
	s.watcher.Receive(watch.Event{Type: watch.Added, Object: cluster})
	return nil
}

func (s *AgentInformerWatcherStore) Update(cluster *clusterv1.ManagedCluster) error {
	s.watcher.Receive(watch.Event{Type: watch.Modified, Object: cluster})
	return nil
}

func (s *AgentInformerWatcherStore) Delete(cluster *clusterv1.ManagedCluster) error {
	s.watcher.Receive(watch.Event{Type: watch.Deleted, Object: cluster})
	return nil
}

func (s *AgentInformerWatcherStore) HandleReceivedCluster(action types.ResourceAction, cluster *clusterv1.ManagedCluster) error {
	switch action {
	case types.Added:
		return s.Add(cluster.DeepCopy())
	case types.Modified:
		lastCluster, exists, err := s.Get(cluster.Name)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("the cluster %s does not exist", cluster.Name)
		}
		// prevent the cluster from being updated if it is deleting
		if !lastCluster.GetDeletionTimestamp().IsZero() {
			klog.Warningf("the cluster %s is deleting, ignore the update", cluster.Name)
			return nil
		}

		updatedCluster := cluster.DeepCopy()

		// restore the fields that are maintained by local agent
		updatedCluster.Labels = lastCluster.Labels
		updatedCluster.Annotations = lastCluster.Annotations
		updatedCluster.Finalizers = lastCluster.Finalizers
		updatedCluster.Status = lastCluster.Status

		return s.Update(updatedCluster)
	case types.Deleted:
		// the cluster is deleting on the source, we just update its deletion timestamp.
		lastCluster, exists, err := s.Get(cluster.Name)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}

		updatedCluster := lastCluster.DeepCopy()
		updatedCluster.DeletionTimestamp = cluster.DeletionTimestamp
		return s.Update(updatedCluster)
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
