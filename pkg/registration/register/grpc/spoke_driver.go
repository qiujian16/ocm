package grpc

import (
	"context"
	"fmt"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/klog/v2"
	clusterv1informers "open-cluster-management.io/api/client/cluster/informers/externalversions"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/ocm/pkg/registration/register"
	"open-cluster-management.io/ocm/pkg/registration/register/csr"
	cloudeventscluster "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster"
	clusterstore "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/cluster/store"
	cloudeventscsr "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr"
	csrstore "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr/store"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"os"
	"path"
	"time"
)

type GRPCDriver struct {
	// csrName is the name of csr created by controller and waiting for approval.
	csrName string

	// keyData is the private key data used to created a csr
	// csrName and keyData store the internal state of the controller. They are set after controller creates a new csr
	// and cleared once the csr is approved and processed by controller. There are 4 combination of their values:
	//   1. csrName empty, keyData empty: means we aren't trying to create a new client cert, our current one is valid
	//   2. csrName set, keyData empty: there was bug
	//   3. csrName set, keyData set: we are waiting for a new cert to be signed.
	//   4. csrName empty, keydata set: the CSR failed to create, this shouldn't happen, it's a bug.
	keyData []byte

	csrDriver *csr.CSRDriver
}

var _ register.RegisterDriver = &GRPCDriver{}
var _ register.CSRDriver = &GRPCDriver{}

func NewGRPCDriver() register.RegisterDriver {
	return &GRPCDriver{
		csrDriver: csr.NewCSRDriver(),
	}
}

func (d *GRPCDriver) CSRControl() register.CSRControl {
	return d.csrDriver.CSRControl()
}

func (d *GRPCDriver) BuildClients(ctx context.Context, secretOption register.SecretOption, bootstrapped bool) (*register.Clients, error) {
	// For cloudevents drivers, we build hub client based on different driver configuration.
	clusterWatcherStore := clusterstore.NewAgentInformerWatcherStore()
	csrWatcherStore := csrstore.NewAgentInformerWatcherStore()
	var config any
	var err error
	if bootstrapped {
		_, config, err = generic.NewConfigLoader("grpc", secretOption.HubBootstrapConfig).
			LoadConfig()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to load hub bootstrap registration config from file %q: %w",
				secretOption.HubBootstrapConfig, err)
		}
	} else {
		_, config, err = generic.NewConfigLoader("grpc", secretOption.HubConfig).
			LoadConfig()
		if err != nil {
			return nil, fmt.Errorf(
				"failed to load hub registration config from file %q: %w",
				secretOption.HubBootstrapConfig, err)
		}
	}

	clientHolder, err := cloudeventscluster.NewClientHolderBuilder(config).
		WithClientID(secretOption.ClusterName).
		WithClusterName(secretOption.ClusterName).
		WithCodec(cloudeventscluster.NewManagedClusterCodec()).
		WithClusterClientWatcherStore(clusterWatcherStore).
		NewAgentClientHolder(ctx)
	if err != nil {
		return nil, err
	}

	csrClientHolder, err := cloudeventscsr.NewClientHolderBuilder(config).
		WithClientID(secretOption.ClusterName).
		WithClusterName(secretOption.ClusterName).
		WithCodec(cloudeventscsr.NewCSRCodec()).
		WithCSRClientWatcherStore(csrWatcherStore).
		NewAgentClientHolder(ctx)
	if err != nil {
		return nil, err
	}

	d.csrDriver.CSRControlFunc = &csrControl{csrClientHolder: csrClientHolder}
	err = d.csrDriver.CSRControlFunc.Informer().AddIndexers(cache.Indexers{
		csr.IndexByCluster: csr.IndexByClusterFunc,
	})
	if err != nil {
		return nil, err
	}
	d.csrDriver.HaltCSRCreation = csr.HaltCSRCreationFunc(
		d.csrDriver.CSRControlFunc.Informer().GetIndexer(), secretOption.ClusterName)

	clients := &register.Clients{}
	clients.ClusterClient = clientHolder.ClusterInterface()
	clients.ClusterInfomerFactory = clusterv1informers.NewSharedInformerFactory(clients.ClusterClient, 10*time.Minute)
	if clusterWatcherStore != nil {
		clusterWatcherStore.SetInformer(clients.ClusterInfomerFactory.Cluster().V1().ManagedClusters().Informer())
	}
	return clients, nil
}

func (c *GRPCDriver) Process(
	ctx context.Context, controllerName string, secret *corev1.Secret, additionalSecretData map[string][]byte,
	recorder events.Recorder, opt any) (*corev1.Secret, *metav1.Condition, error) {
	grpcOption, ok := opt.(*GRPCOption)
	if !ok {
		return nil, nil, fmt.Errorf("option type is not correct")
	}

	return c.csrDriver.Process(ctx, controllerName, secret, additionalSecretData, recorder, grpcOption.csrOption)
}

func (c *GRPCDriver) BuildKubeConfigFromTemplate(template *clientcmdapi.Config) *clientcmdapi.Config {
	return nil
}

func (c *GRPCDriver) InformerHandler(option any) (cache.SharedIndexInformer, factory.EventFilterFunc) {
	return c.csrDriver.CSRControl().Informer(), nil
}

func (c *GRPCDriver) IsHubKubeConfigValid(ctx context.Context, secretOption register.SecretOption) (bool, error) {
	logger := klog.FromContext(ctx)
	keyPath := path.Join(secretOption.HubKubeconfigDir, csr.TLSKeyFile)
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		logger.V(4).Info("TLS key file not found", "keyPath", keyPath)
		return false, nil
	}

	certPath := path.Join(secretOption.HubKubeconfigDir, csr.TLSCertFile)
	certData, err := os.ReadFile(path.Clean(certPath))
	if err != nil {
		logger.V(4).Info("Unable to load TLS cert file", "certPath", certPath)
		return false, nil
	}

	// only set when clustername/agentname are set
	if len(secretOption.ClusterName) > 0 && len(secretOption.AgentName) > 0 {
		// check if the tls certificate is issued for the current cluster/agent
		clusterNameInCert, agentNameInCert, err := csr.GetClusterAgentNamesFromCertificate(certData)
		if err != nil {
			return false, nil
		}
		if secretOption.ClusterName != clusterNameInCert || secretOption.AgentName != agentNameInCert {
			logger.V(4).Info("Certificate in file is issued for different agent",
				"certPath", certPath,
				"issuedFor", fmt.Sprintf("%s:%s", secretOption.ClusterName, secretOption.AgentName),
				"expectedFor", fmt.Sprintf("%s:%s", secretOption.ClusterName, secretOption.AgentName))

			return false, nil
		}
	}
	return csr.IsCertificateValid(logger, certData, nil)
}

func (c *GRPCDriver) ManagedClusterDecorator(cluster *clusterv1.ManagedCluster) *clusterv1.ManagedCluster {
	return cluster
}

func (c *GRPCDriver) reset() {
	c.csrName = ""
	c.keyData = nil
}
