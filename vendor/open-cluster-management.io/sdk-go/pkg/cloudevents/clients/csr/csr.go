package csr

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog/v2"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/common"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr/store"
	cloudeventserrors "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/errors"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"

	certificatev1 "k8s.io/api/certificates/v1"
)

// CSRAgentClient implements the CSRInterface. It sends the csr to source by
// CloudEventAgentClient.
type CSRAgentClient struct {
	sync.RWMutex

	cloudEventsClient *generic.CloudEventAgentClient[*certificatev1.CertificateSigningRequest]
	watcherStore      store.CSRClientWatcherStore
}

var _ cache.ListerWatcher = &CSRAgentClient{}

func NewCSRAgentClient(
	cloudEventsClient *generic.CloudEventAgentClient[*certificatev1.CertificateSigningRequest],
	watcherStore store.CSRClientWatcherStore,
	clusterName string,
) *CSRAgentClient {
	return &CSRAgentClient{
		cloudEventsClient: cloudEventsClient,
		watcherStore:      watcherStore,
	}
}

func (c *CSRAgentClient) Create(ctx context.Context, csr *certificatev1.CertificateSigningRequest, opts metav1.CreateOptions) (*certificatev1.CertificateSigningRequest, error) {
	klog.V(4).Infof("creating certificatesigningrequest %s", csr.Name)
	_, exists, err := c.watcherStore.Get(csr.Name)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}
	if exists {
		returnErr := errors.NewAlreadyExists(common.CSRGR, csr.Name)
		return nil, returnErr
	}

	eventType := types.CloudEventsType{
		CloudEventsDataType: CSREventDataType,
		SubResource:         types.SubResourceSpec,
		Action:              common.CreateRequestAction,
	}

	// TODO: validate the csr

	if err := c.cloudEventsClient.Publish(ctx, eventType, csr); err != nil {
		returnErr := cloudeventserrors.NewPublishError(common.CSRGR, csr.Name, err)
		return nil, returnErr
	}

	// add the new csr to the local cache.
	if err := c.watcherStore.Add(csr); err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return csr.DeepCopy(), nil

}

func (c *CSRAgentClient) Get(ctx context.Context, name string, opts metav1.GetOptions) (*certificatev1.CertificateSigningRequest, error) {
	klog.V(4).Infof("getting csr %s", name)
	csr, exists, err := c.watcherStore.Get(name)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}
	if !exists {
		returnErr := errors.NewNotFound(common.CSRGR, name)
		return nil, returnErr
	}

	return csr, nil
}

func (c *CSRAgentClient) List(opts metav1.ListOptions) (runtime.Object, error) {
	klog.V(4).Info("list csr")
	csrs, err := c.watcherStore.List(opts)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return csrs, nil
}

func (c *CSRAgentClient) Watch(opts metav1.ListOptions) (watch.Interface, error) {
	klog.V(4).Info("watch csr")
	watcher, err := c.watcherStore.GetWatcher(opts)
	if err != nil {
		returnErr := errors.NewInternalError(err)
		return nil, returnErr
	}

	return watcher, nil
}
