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
	clusterce "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/server"
)

const source = "kube"

type ClusterService struct {
	clusterClient   clusterclient.Interface
	clusterLister   listerv1.ManagedClusterLister
	clusterInformer informerv1.ManagedClusterInformer
	codec           *clusterce.ManagedClusterCodec
}

func NewClusterService(clusterClient clusterclient.Interface, clusterInformer informerv1.ManagedClusterInformer) server.Service {
	return &ClusterService{
		clusterClient:   clusterClient,
		clusterLister:   clusterInformer.Lister(),
		clusterInformer: clusterInformer,
		codec:           clusterce.NewManagedClusterCodec(),
	}
}

func (c *ClusterService) Get(_ context.Context, resourceID string) (*cloudevents.Event, error) {
	cluster, err := c.clusterLister.Get(resourceID)
	if err != nil {
		return nil, err
	}

	evt, err := c.codec.Encode(source, types.CloudEventsType{CloudEventsDataType: clusterce.ManagedClusterEventDataType}, cluster)
	if err != nil {
		return nil, err
	}

	return evt, nil
}

func (c *ClusterService) List(listOpts types.ListOptions) ([]*cloudevents.Event, error) {
	var evts []*cloudevents.Event
	//only get single cluster
	cluster, err := c.clusterLister.Get(listOpts.ClusterName)
	if err != nil {
		return nil, err
	}

	evt, err := c.codec.Encode(source, types.CloudEventsType{CloudEventsDataType: clusterce.ManagedClusterEventDataType}, cluster)
	if err != nil {
		return nil, err
	}

	return append(evts, evt), nil
}

// q if there is resourceVersion, this will return directly to the agent as conflict?
func (c *ClusterService) HandleStatusUpdate(ctx context.Context, evt *cloudevents.Event) error {
	eventType, err := types.ParseCloudEventsType(evt.Type())
	if err != nil {
		return fmt.Errorf("failed to parse cloud event type %s, %v", evt.Type(), err)
	}
	cluster, err := c.codec.Decode(evt)
	if err != nil {
		return err
	}

	klog.V(4).Infof("cluster status update (%s) %s", eventType.Action, cluster.Name)

	// only create and update action
	switch eventType.Action {
	case types.CreateRequestAction:
		_, err := c.clusterClient.ClusterV1().ManagedClusters().Create(ctx, cluster, metav1.CreateOptions{})
		return err
	case types.UpdateRequestAction:
		if eventType.SubResource == types.SubResourceStatus {
			_, err := c.clusterClient.ClusterV1().ManagedClusters().UpdateStatus(ctx, cluster, metav1.UpdateOptions{})
			return err
		}

		_, err := c.clusterClient.ClusterV1().ManagedClusters().Update(ctx, cluster, metav1.UpdateOptions{})
		return err
	default:
		return fmt.Errorf("unsupported cluster event action %s", eventType.Action)
	}
}

func (c *ClusterService) RegisterHandler(handler server.EventHandler) {
	c.clusterInformer.Informer().AddEventHandler(&cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			accessor, _ := meta.Accessor(obj)
			if err := handler.OnCreate(context.Background(), clusterce.ManagedClusterEventDataType, accessor.GetName()); err != nil {
				klog.Error(err)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			accessor, _ := meta.Accessor(newObj)
			if err := handler.OnUpdate(context.Background(), clusterce.ManagedClusterEventDataType, accessor.GetName()); err != nil {
				klog.Error(err)
			}
		},
		// agent does not need to care about delete event
	})
}
