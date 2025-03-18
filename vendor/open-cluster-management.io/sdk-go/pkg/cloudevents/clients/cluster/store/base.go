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

	clusterv1 "open-cluster-management.io/api/cluster/v1"
)

const ManagedClusterFinalizer = "cloudevents.open-cluster-management.io/managed-cluster-cleanup"

type baseStore struct {
	sync.RWMutex

	store     cache.Store
	initiated bool
}

// List the clusters from the store with the list options
func (b *baseStore) List(opts metav1.ListOptions) (*clusterv1.ManagedClusterList, error) {
	b.RLock()
	defer b.RUnlock()

	clusters, err := ListClustersWithOptions(b.store, opts)
	if err != nil {
		return nil, err
	}

	items := []clusterv1.ManagedCluster{}
	for _, cluster := range clusters {
		items = append(items, *cluster)
	}

	return &clusterv1.ManagedClusterList{Items: items}, nil
}

// Get a cluster from the store
func (b *baseStore) Get(name string) (*clusterv1.ManagedCluster, bool, error) {
	b.RLock()
	defer b.RUnlock()

	obj, exists, err := b.store.GetByKey(name)
	if err != nil {
		return nil, false, err
	}

	if !exists {
		return nil, false, nil
	}

	cluster, ok := obj.(*clusterv1.ManagedCluster)
	if !ok {
		return nil, false, fmt.Errorf("unknown type %T", obj)
	}

	return cluster, true, nil
}

// List all of clusters from the store
func (b *baseStore) ListAll() ([]*clusterv1.ManagedCluster, error) {
	b.RLock()
	defer b.RUnlock()

	clusters := []*clusterv1.ManagedCluster{}
	for _, obj := range b.store.List() {
		if cluster, ok := obj.(*clusterv1.ManagedCluster); ok {
			clusters = append(clusters, cluster)
		}
	}

	return clusters, nil
}

// clusterWatcher implements the watch.Interface.
type clusterWatcher struct {
	sync.RWMutex

	result  chan watch.Event
	done    chan struct{}
	stopped bool
}

var _ watch.Interface = &clusterWatcher{}

func newClusterWatcher() *clusterWatcher {
	return &clusterWatcher{
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
func (w *clusterWatcher) ResultChan() <-chan watch.Event {
	return w.result
}

// Stop implements Interface.
func (w *clusterWatcher) Stop() {
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

// Receive a event from the cluster client and sends down the result channel.
func (w *clusterWatcher) Receive(evt watch.Event) {
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

func (w *clusterWatcher) isStopped() bool {
	w.RLock()
	defer w.RUnlock()

	return w.stopped
}

// ListClustersWithOptions retrieves the managedclusters from store which matches the options.
func ListClustersWithOptions(store cache.Store, opts metav1.ListOptions) ([]*clusterv1.ManagedCluster, error) {
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

	clusters := []*clusterv1.ManagedCluster{}
	// list with labels
	if err := cache.ListAll(store, labelSelector, func(obj interface{}) {
		cluster, ok := obj.(*clusterv1.ManagedCluster)
		if !ok {
			return
		}

		clusterFieldSet := fields.Set{
			"metadata.name": cluster.Name,
		}

		if !fieldSelector.Matches(clusterFieldSet) {
			return
		}

		clusters = append(clusters, cluster)
	}); err != nil {
		return nil, err
	}

	return clusters, nil
}
