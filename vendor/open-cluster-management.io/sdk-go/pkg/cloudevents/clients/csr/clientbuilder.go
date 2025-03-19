package csr

import (
	"context"
	"fmt"
	"k8s.io/client-go/tools/cache"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr/store"
	"time"

	certificatev1 "k8s.io/api/certificates/v1"
	"k8s.io/klog/v2"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// ClientHolder holds a csr client that implements the CSRInterface based on different configuration
//
// ClientHolder also implements the CertificateSigningRequestsGetter interface.
type ClientHolder struct {
	informer cache.SharedIndexInformer
	client   *CSRAgentClient
}

// kubeClientInterface returns a kubeclientset Interface
func (h *ClientHolder) Informer() cache.SharedIndexInformer {
	return h.informer
}

// CertificateSigningRequests returns a certificatev1client Interface
func (h *ClientHolder) Clients() *CSRAgentClient {
	return h.client
}

// ClientHolderBuilder builds the ClientHolder with different configuration.
type ClientHolderBuilder struct {
	config       any
	watcherStore *store.AgentInformerWatcherStore
	codec        generic.Codec[*certificatev1.CertificateSigningRequest]
	clusterName  string
	clientID     string
	resync       bool
}

// NewClientHolderBuilder returns a ClientHolderBuilder with a given configuration.
//
// Available configurations:
//   - MQTTOptions (*mqtt.MQTTOptions): builds a csr client based on cloudevents with MQTT
//   - GRPCOptions (*grpc.GRPCOptions): builds a csr client based on cloudevents with GRPC
//   - KafkaOptions (*kafka.KafkaOptions): builds a csr client based on cloudevents with Kafka
//
// TODO using a specified config instead of any
func NewClientHolderBuilder(config any) *ClientHolderBuilder {
	return &ClientHolderBuilder{
		config: config,
		resync: true,
	}
}

// WithClientID set the client ID for source/agent cloudevents client.
func (b *ClientHolderBuilder) WithClientID(clientID string) *ClientHolderBuilder {
	b.clientID = clientID
	return b
}

// WithClusterName set the managed cluster name when building a csr client for an agent.
func (b *ClientHolderBuilder) WithClusterName(clusterName string) *ClientHolderBuilder {
	b.clusterName = clusterName
	return b
}

// WithCodec add codec when building a csr client based on cloudevents.
func (b *ClientHolderBuilder) WithCodec(codec generic.Codec[*certificatev1.CertificateSigningRequest]) *ClientHolderBuilder {
	b.codec = codec
	return b
}

// WithCSRClientWatcherStore set the CSRClientWatcherStore. The client will use this store to caches the csrs and
// watch the csr events.
func (b *ClientHolderBuilder) WithCSRClientWatcherStore(store *store.AgentInformerWatcherStore) *ClientHolderBuilder {
	b.watcherStore = store
	return b
}

// WithResyncEnabled control the client resync (Default is true), if it's true, the resync happens when
//  1. after the client's store is initiated
//  2. the client reconnected
func (b *ClientHolderBuilder) WithResyncEnabled(resync bool) *ClientHolderBuilder {
	b.resync = resync
	return b
}

// NewAgentClientHolder returns a ClientHolder for an agent
func (b *ClientHolderBuilder) NewAgentClientHolder(ctx context.Context) (*ClientHolder, error) {
	if len(b.clientID) == 0 {
		return nil, fmt.Errorf("client id is required")
	}

	if len(b.clusterName) == 0 {
		return nil, fmt.Errorf("cluster name is required")
	}

	if b.watcherStore == nil {
		return nil, fmt.Errorf("watcher store is required")
	}

	options, err := generic.BuildCloudEventsAgentOptions(b.config, b.clusterName, b.clientID)
	if err != nil {
		return nil, err
	}

	cloudEventsClient, err := generic.NewCloudEventAgentClient[*certificatev1.CertificateSigningRequest](
		ctx,
		options,
		store.NewWatcherStoreLister(b.watcherStore),
		CSRStatusHash,
		b.codec,
	)
	if err != nil {
		return nil, err
	}

	// start to subscribe
	cloudEventsClient.Subscribe(ctx, b.watcherStore.HandleReceivedCSR)

	csrClient := NewCSRAgentClient(cloudEventsClient, b.watcherStore, b.clusterName)

	// start a go routine to receive client reconnect signal
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-cloudEventsClient.ReconnectedChan():
				if !b.resync {
					klog.V(4).Infof("resync is disabled, do nothing")
					continue
				}

				// when receiving a client reconnected signal, we resync all sources for this agent
				// TODO after supporting multiple sources, we should only resync agent known sources
				if err := cloudEventsClient.Resync(ctx, types.SourceAll); err != nil {
					klog.Errorf("failed to send resync request, %v", err)
				}
			}
		}
	}()

	csrInformer := cache.NewSharedIndexInformer(
		csrClient, &certificatev1.CertificateSigningRequest{}, 30*time.Second,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})

	b.watcherStore.SetInformer(csrInformer)
	
	if !b.resync {
		return &ClientHolder{client: csrClient, informer: csrInformer}, nil
	}

	// start a go routine to resync the csrs after this client's store is initiated
	go func() {
		if store.WaitForStoreInit(ctx, b.watcherStore.HasInitiated) {
			if err := cloudEventsClient.Resync(ctx, types.SourceAll); err != nil {
				klog.Errorf("failed to send resync request, %v", err)
			}
		}
	}()

	return &ClientHolder{client: csrClient, informer: csrInformer}, nil
}
