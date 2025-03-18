package cluster

import (
	"context"
	"fmt"

	"k8s.io/klog/v2"

	clusterclientset "open-cluster-management.io/api/client/cluster/clientset/versioned"
	clusterv1client "open-cluster-management.io/api/client/cluster/clientset/versioned/typed/cluster/v1"
	clusterv1 "open-cluster-management.io/api/cluster/v1"

	"open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster/store"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/types"
)

// ClientHolder holds a managedcluster client that implements the ManagedClusterInterface based on different configuration
//
// ClientHolder also implements the ManagedClustersGetter interface.
type ClientHolder struct {
	clusterClientSet clusterclientset.Interface
}

var _ clusterv1client.ManagedClustersGetter = &ClientHolder{}

// ClusterInterface returns a clusterclientset Interface
func (h *ClientHolder) ClusterInterface() clusterclientset.Interface {
	return h.clusterClientSet
}

// ManagedClusters returns a ManagedClusterInterface
func (h *ClientHolder) ManagedClusters() clusterv1client.ManagedClusterInterface {
	return h.clusterClientSet.ClusterV1().ManagedClusters()
}

// ClientHolderBuilder builds the ClientHolder with different configuration.
type ClientHolderBuilder struct {
	config       any
	watcherStore store.ClusterClientWatcherStore
	codec        generic.Codec[*clusterv1.ManagedCluster]
	clusterName  string
	clientID     string
	resync       bool
}

// NewClientHolderBuilder returns a ClientHolderBuilder with a given configuration.
//
// Available configurations:
//   - MQTTOptions (*mqtt.MQTTOptions): builds a managedcluster client based on cloudevents with MQTT
//   - GRPCOptions (*grpc.GRPCOptions): builds a managedcluster client based on cloudevents with GRPC
//   - KafkaOptions (*kafka.KafkaOptions): builds a managedcluster client based on cloudevents with Kafka
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

// WithClusterName set the managed cluster name when building a managedcluster client for an agent.
func (b *ClientHolderBuilder) WithClusterName(clusterName string) *ClientHolderBuilder {
	b.clusterName = clusterName
	return b
}

// WithCodec add codec when building a managedcluster client based on cloudevents.
func (b *ClientHolderBuilder) WithCodec(codec generic.Codec[*clusterv1.ManagedCluster]) *ClientHolderBuilder {
	b.codec = codec
	return b
}

// WithClusterClientWatcherStore set the ClusterClientWatcherStore. The client will use this store to caches the clusters and
// watch the cluster events.
func (b *ClientHolderBuilder) WithClusterClientWatcherStore(store store.ClusterClientWatcherStore) *ClientHolderBuilder {
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

	cloudEventsClient, err := generic.NewCloudEventAgentClient[*clusterv1.ManagedCluster](
		ctx,
		options,
		store.NewWatcherStoreLister(b.watcherStore),
		ManagedClusterStatusHash,
		b.codec,
	)
	if err != nil {
		return nil, err
	}

	// start to subscribe
	cloudEventsClient.Subscribe(ctx, b.watcherStore.HandleReceivedCluster)

	managedClusterClient := NewManagedClusterAgentClient(cloudEventsClient, b.watcherStore, b.clusterName)
	clusterClient := &ClusterV1ClientWrapper{ManagedClusterClient: managedClusterClient}
	clusterClientSet := &ClusterClientSetWrapper{ClusterV1ClientWrapper: clusterClient}

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

	if !b.resync {
		return &ClientHolder{clusterClientSet: clusterClientSet}, nil
	}

	// start a go routine to resync the clusters after this client's store is initiated
	go func() {
		if store.WaitForStoreInit(ctx, b.watcherStore.HasInitiated) {
			if err := cloudEventsClient.Resync(ctx, types.SourceAll); err != nil {
				klog.Errorf("failed to send resync request, %v", err)
			}
		}
	}()

	return &ClientHolder{clusterClientSet: clusterClientSet}, nil
}
