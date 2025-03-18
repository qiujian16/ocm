package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	jsonpatch "github.com/evanphx/json-patch"
	"net/http"
	"strconv"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubetypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"

	clusterv1client "open-cluster-management.io/api/client/cluster/clientset/versioned/typed/cluster/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"

	ktypes "k8s.io/apimachinery/pkg/types"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster/store"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/common"
	cloudeventserrors "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/errors"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// ManagedClusterAgentClient implements the ManagedClusterInterface. It sends the managedclusters status back to source by
// CloudEventAgentClient.
type ManagedClusterAgentClient struct {
	sync.RWMutex

	cloudEventsClient *generic.CloudEventAgentClient[*clusterv1.ManagedCluster]
	watcherStore      store.ClusterClientWatcherStore
}

var _ clusterv1client.ManagedClusterInterface = &ManagedClusterAgentClient{}

func NewManagedClusterAgentClient(
	cloudEventsClient *generic.CloudEventAgentClient[*clusterv1.ManagedCluster],
	watcherStore store.ClusterClientWatcherStore,
	clusterName string,
) *ManagedClusterAgentClient {
	return &ManagedClusterAgentClient{
		cloudEventsClient: cloudEventsClient,
		watcherStore:      watcherStore,
	}
}

func (c *ManagedClusterAgentClient) Create(ctx context.Context, cluster *clusterv1.ManagedCluster, opts metav1.CreateOptions) (*clusterv1.ManagedCluster, error) {
	klog.V(4).Infof("creating managedcluster %s", cluster.Name)
	_, exists, err := c.watcherStore.Get(cluster.Name)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}
	if exists {
		returnErr := errors.NewAlreadyExists(common.ManagedClusterGR, cluster.Name)
		return nil, returnErr
	}

	eventType := types.CloudEventsType{
		CloudEventsDataType: ManagedClusterEventDataType,
		SubResource:         types.SubResourceSpec,
		Action:              common.CreateRequestAction,
	}

	// TODO: validate the managedcluster

	if err := c.cloudEventsClient.Publish(ctx, eventType, cluster); err != nil {
		returnErr := cloudeventserrors.NewPublishError(common.ManagedClusterGR, cluster.Name, err)
		return nil, returnErr
	}

	// add the new cluster to the local cache.
	if err := c.watcherStore.Add(cluster); err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return cluster.DeepCopy(), nil

}

func (c *ManagedClusterAgentClient) Update(ctx context.Context, cluster *clusterv1.ManagedCluster, opts metav1.UpdateOptions) (*clusterv1.ManagedCluster, error) {
	return nil, errors.NewMethodNotSupported(common.ManagedClusterGR, "update")
}

func (c *ManagedClusterAgentClient) UpdateStatus(ctx context.Context, cluster *clusterv1.ManagedCluster, opts metav1.UpdateOptions) (*clusterv1.ManagedCluster, error) {
	return nil, errors.NewMethodNotSupported(common.ManagedClusterGR, "updatestatus")
}

func (c *ManagedClusterAgentClient) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return errors.NewMethodNotSupported(common.ManagedClusterGR, "delete")
}

func (c *ManagedClusterAgentClient) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	return errors.NewMethodNotSupported(common.ManagedClusterGR, "deletecollection")
}

func (c *ManagedClusterAgentClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*clusterv1.ManagedCluster, error) {
	klog.V(4).Infof("getting managedcluster %s", name)
	cluster, exists, err := c.watcherStore.Get(name)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}
	if !exists {
		returnErr := errors.NewNotFound(common.ManagedClusterGR, name)
		return nil, returnErr
	}

	return cluster, nil
}

func (c *ManagedClusterAgentClient) List(ctx context.Context, opts metav1.ListOptions) (*clusterv1.ManagedClusterList, error) {
	klog.V(4).Info("list managedcluster")
	clusters, err := c.watcherStore.List(opts)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return clusters, nil
}

func (c *ManagedClusterAgentClient) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	klog.V(4).Info("watch managedcluster")
	watcher, err := c.watcherStore.GetWatcher(opts)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return watcher, nil
}

func (c *ManagedClusterAgentClient) Patch(ctx context.Context, name string, pt kubetypes.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (result *clusterv1.ManagedCluster, err error) {
	klog.V(4).Infof("patching managedcluster %s", name)
	lastCluster, exists, err := c.watcherStore.Get(name)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}
	if !exists {
		returnErr := errors.NewNotFound(common.ManagedClusterGR, name)
		return nil, returnErr
	}

	patchedCluster, err := Patch(pt, lastCluster, data)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	eventType := types.CloudEventsType{
		CloudEventsDataType: ManagedClusterEventDataType,
		SubResource:         types.SubResourceStatus,
	}

	newCluster := patchedCluster.DeepCopy()

	statusUpdated, err := isStatusUpdate(subresources)
	if err != nil {
		returnErr := errors.NewGenericServerResponse(http.StatusMethodNotAllowed, "patch", common.ManagedClusterGR, name, err.Error(), 0, false)
		return nil, returnErr
	}

	if statusUpdated {
		// avoid race conditions among the agent's go routines
		c.Lock()
		defer c.Unlock()

		eventType.Action = common.UpdateRequestAction
		// publish the status update event to source, source will check the resource version
		// and reject the update if it's status update is outdated.
		if err := c.cloudEventsClient.Publish(ctx, eventType, newCluster); err != nil {
			returnErr := cloudeventserrors.NewPublishError(common.ManagedClusterGR, name, err)
			return nil, returnErr
		}

		// Fetch the latest cluster from the store and verify the resource version to avoid updating the store
		// with outdated cluster. Return a conflict error if the resource version is outdated.
		// Due to the lack of read-modify-write guarantees in the store, race conditions may occur between
		// this update operation and one from the agent informer after receiving the event from the source.
		lastCluster, exists, err := c.watcherStore.Get(name)
		if err != nil {
			returnErr := errors.NewInternalError(err)
			return nil, returnErr
		}
		if !exists {
			returnErr := errors.NewNotFound(common.ManagedClusterGR, name)
			return nil, returnErr
		}
		lastResourceVersion, err := strconv.ParseInt(lastCluster.GetResourceVersion(), 10, 64)
		if err != nil {
			returnErr := errors.NewInternalError(err)
			return nil, returnErr
		}
		newResourceVersion, err := strconv.ParseInt(newCluster.GetResourceVersion(), 10, 64)
		if err != nil {
			returnErr := errors.NewInternalError(err)
			return nil, returnErr
		}
		// ensure the resource version of the cluster is not outdated
		if newResourceVersion < lastResourceVersion {
			// It's safe to return a conflict error here, even if the status update event
			// has already been sent. The source may reject the update due to an outdated resource version.
			returnErr := errors.NewConflict(common.ManagedClusterGR, name, fmt.Errorf("the resource version of the cluster is outdated"))
			return nil, returnErr
		}
		if err := c.watcherStore.Update(newCluster); err != nil {
			returnErr := errors.NewInternalError(err)
			return nil, returnErr
		}

		return newCluster, nil
	}

	// the finalizers of a deleting managedcluster are removed, marking the managedcluster status to deleted and sending
	// it back to source
	if !newCluster.DeletionTimestamp.IsZero() && len(newCluster.Finalizers) == 0 {
		meta.SetStatusCondition(&newCluster.Status.Conditions, metav1.Condition{
			Type:    common.ConditionDeleted,
			Status:  metav1.ConditionTrue,
			Reason:  "ManagedClusterDeleted",
			Message: fmt.Sprintf("The manifests are deleted from the cluster %s", newCluster.Namespace),
		})

		eventType.Action = common.DeleteRequestAction
		if err := c.cloudEventsClient.Publish(ctx, eventType, newCluster); err != nil {
			returnErr := cloudeventserrors.NewPublishError(common.ManagedClusterGR, name, err)
			return nil, returnErr
		}

		if err := c.watcherStore.Delete(newCluster); err != nil {
			returnErr := errors.NewInternalError(err)
			return nil, returnErr
		}

		return newCluster, nil
	}

	if err := c.watcherStore.Update(newCluster); err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return newCluster, nil
}

func isStatusUpdate(subresources []string) (bool, error) {
	if len(subresources) == 0 {
		return false, nil
	}

	if len(subresources) == 1 && subresources[0] == "status" {
		return true, nil
	}

	return false, fmt.Errorf("unsupported subresources %v", subresources)
}

// Patch applies the patch to a cluster with the patch type.
func Patch(patchType ktypes.PatchType, cluster *clusterv1.ManagedCluster, patchData []byte) (*clusterv1.ManagedCluster, error) {
	clusterData, err := json.Marshal(cluster)
	if err != nil {
		return nil, err
	}

	var patchedData []byte
	switch patchType {
	case ktypes.JSONPatchType:
		var patchObj jsonpatch.Patch
		patchObj, err = jsonpatch.DecodePatch(patchData)
		if err != nil {
			return nil, err
		}
		patchedData, err = patchObj.Apply(clusterData)
		if err != nil {
			return nil, err
		}

	case ktypes.MergePatchType:
		patchedData, err = jsonpatch.MergePatch(clusterData, patchData)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported patch type: %s", patchType)
	}

	patchedCluster := &clusterv1.ManagedCluster{}
	if err := json.Unmarshal(patchedData, patchedCluster); err != nil {
		return nil, err
	}

	return patchedCluster, nil
}
