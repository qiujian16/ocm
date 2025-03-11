package services

import (
	"context"
	"fmt"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	clusterclient "open-cluster-management.io/api/client/cluster/clientset/versioned"
	informerv1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1"
	listerv1 "open-cluster-management.io/api/client/cluster/listers/cluster/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/server"
	"strconv"
)

const (
	source                    = "kube"
	createRequestAction       = "create_request"
	updateRequestAction       = "update_request"
	updateStatusRequestAction = "update_status_request"
)

var clusterDataType = types.CloudEventsDataType{
	Group:    clusterv1.GroupName,
	Version:  clusterv1.GroupVersion.Version,
	Resource: "managedclusters",
}

type ClusterService struct {
	clusterClient   clusterclient.Interface
	clusterLister   listerv1.ManagedClusterLister
	clusterInformer informerv1.ManagedClusterInformer
}

func NewClusterService(clusterClient clusterclient.Interface, clusterInformer informerv1.ManagedClusterInformer) server.Service {
	return &ClusterService{
		clusterClient:   clusterClient,
		clusterLister:   clusterInformer.Lister(),
		clusterInformer: clusterInformer,
	}
}

func (c *ClusterService) Get(_ context.Context, resourceID string) (*cloudevents.Event, error) {
	cluster, err := c.clusterLister.Get(resourceID)
	if err != nil {
		return nil, err
	}

	evt, err := convertClusterToEvent(cluster)
	if err != nil {
		return nil, err
	}

	return evt, nil
}

func (c *ClusterService) List(listOpts types.ListOptions) ([]*cloudevents.Event, error) {
	var evts []*cloudevents.Event
	if listOpts.Source != source {
		return evts, nil
	}
	//only get single cluster
	cluster, err := c.clusterLister.Get(listOpts.ClusterName)
	if err != nil {
		return nil, err
	}

	evt, err := convertClusterToEvent(cluster)
	if err != nil {
		return nil, err
	}

	return append(evts, evt), nil
}

// q if there is resourceVersion, this will return directly to the agent as conflict?
func (c *ClusterService) HandleStatusUpdate(ctx context.Context, evt *cloudevents.Event) error {
	cluster, action, err := convertEventToCluster(evt)
	if err != nil {
		return err
	}

	// only create and update action
	switch action {
	case createRequestAction:
		_, err := c.clusterClient.ClusterV1().ManagedClusters().Create(ctx, cluster, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	case updateStatusRequestAction:
		_, err := c.clusterClient.ClusterV1().ManagedClusters().UpdateStatus(ctx, cluster, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	case updateRequestAction:
		_, err := c.clusterClient.ClusterV1().ManagedClusters().Update(ctx, cluster, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *ClusterService) RegisterHandler(handler server.EventHandler) {
	c.clusterInformer.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			accessor, _ := meta.Accessor(obj)
			if err := handler.OnCreate(context.Background(), clusterDataType, accessor.GetName()); err != nil {
				klog.Error(err)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			accessor, _ := meta.Accessor(newObj)
			if err := handler.OnUpdate(context.Background(), clusterDataType, accessor.GetName()); err != nil {
				klog.Error(err)
			}
		},
		// agent does not need to care about delete event
	})
}

func convertEventToCluster(evt *cloudevents.Event) (*clusterv1.ManagedCluster, string, error) {
	eventType, err := types.ParseCloudEventsType(evt.Type())
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse cloud event type %s, %v", evt.Type(), err)
	}

	cluster := &clusterv1.ManagedCluster{}
	if err := evt.DataAs(cluster); err != nil {
		return nil, "", fmt.Errorf("failed to unmarshal event data %s, %v", string(evt.Data()), err)
	}

	return cluster, string(eventType.Action), nil
}

func convertClusterToEvent(cluster *clusterv1.ManagedCluster) (*cloudevents.Event, error) {
	resourceVersion, err := strconv.ParseInt(cluster.ResourceVersion, 10, 64)
	if err != nil {
		return nil, err
	}
	evt := types.NewEventBuilder(source, types.CloudEventsType{}).
		WithClusterName(cluster.Name).
		// question do we need to set uid?
		WithResourceID(cluster.Name).
		WithResourceVersion(resourceVersion).
		NewEvent()
	if !cluster.DeletionTimestamp.IsZero() {
		evt.SetExtension(types.ExtensionDeletionTimestamp, cluster.DeletionTimestamp.Time)
		return &evt, nil
	}

	// can we set the whole cluster resource into the data? what is the impact?
	if err := evt.SetData(cloudevents.ApplicationJSON, cluster); err != nil {
		return nil, fmt.Errorf("failed to encode manifestwork status to a cloudevent: %v", err)
	}

	return &evt, nil
}
