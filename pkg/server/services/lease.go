package services

import (
	"context"
	"fmt"
	cloudevents "github.com/cloudevents/sdk-go/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	leasece "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/lease"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/server"
)

type LeaseService struct {
	client kubernetes.Interface
	codec  leasece.LeaseCodec
}

func (l LeaseService) Get(ctx context.Context, resourceID string) (*cloudevents.Event, error) {
	namespace, name, err := cache.SplitMetaNamespaceKey(resourceID)
	if err != nil {
		return nil, err
	}
	lease, err := l.client.CoordinationV1().Leases(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return l.codec.Encode(source, types.CloudEventsType{CloudEventsDataType: leasece.LeaseEventDataType}, lease)
}

func (l LeaseService) List(listOpts types.ListOptions) ([]*cloudevents.Event, error) {
	if len(listOpts.ClusterName) == 0 {
		return nil, fmt.Errorf("cluster name is empty")
	}
	leases, err := l.client.CoordinationV1().Leases(listOpts.ClusterName).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	var cloudevts []*cloudevents.Event
	for _, lease := range leases.Items {
		cloudevt, err := l.codec.Encode(source, types.CloudEventsType{CloudEventsDataType: leasece.LeaseEventDataType}, &lease)
		if err != nil {
			return nil, err
		}
		cloudevts = append(cloudevts, cloudevt)
	}
	return cloudevts, nil
}

func (l LeaseService) HandleStatusUpdate(ctx context.Context, evt *cloudevents.Event) error {
	eventType, err := types.ParseCloudEventsType(evt.Type())
	if err != nil {
		return fmt.Errorf("failed to parse cloud event type %s, %v", evt.Type(), err)
	}
	lease, err := l.codec.Decode(evt)
	if err != nil {
		return err
	}

	// only create and update action
	switch eventType.Action {
	case updateRequestAction:
		_, err := l.client.CoordinationV1().Leases(lease.Namespace).Create(ctx, lease, metav1.CreateOptions{})
		if err != nil {
			return err
		}
	}
	return nil
}

func (l LeaseService) RegisterHandler(handler server.EventHandler) {
	return
}

func NewLeaseService(client kubernetes.Interface) *LeaseService {
	return &LeaseService{
		client: client,
	}
}

var _ server.Service = &LeaseService{}
