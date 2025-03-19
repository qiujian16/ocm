package grpc

import (
	"context"
	"crypto/tls"
	"fmt"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/cache"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/keyutil"
	"k8s.io/klog/v2"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/ocm/pkg/registration/register"
	"open-cluster-management.io/ocm/pkg/registration/register/csr"
	"os"
	"path"
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
}

func NewGRPCDriver() register.RegisterDriver {
	return &GRPCDriver{}
}

func (c *GRPCDriver) Process(
	ctx context.Context, controllerName string, secret *corev1.Secret, additionalSecretData map[string][]byte,
	recorder events.Recorder, opt any) (*corev1.Secret, *metav1.Condition, error) {
	logger := klog.FromContext(ctx)
	grpcOption, ok := opt.(*GRPCOption)
	if !ok {
		return nil, nil, fmt.Errorf("option type is not correct")
	}

	// reconcile pending csr if exists
	if len(c.csrName) > 0 {
		// build a secret data map if the csr is approved
		newSecretConfig, err := func() (map[string][]byte, error) {
			// skip if there is no ongoing csr
			if len(c.csrName) == 0 {
				return nil, fmt.Errorf("no ongoing csr")
			}

			// skip if csr is not approved yet
			isApproved, err := grpcOption.control.isApproved(c.csrName)
			if err != nil {
				return nil, err
			}
			if !isApproved {
				return nil, nil
			}

			// skip if csr is not issued
			certData, err := grpcOption.control.getIssuedCertificate(c.csrName)
			if err != nil {
				return nil, err
			}
			if len(certData) == 0 {
				return nil, nil
			}

			logger.Info("Sync csr", "name", c.csrName)
			// check if cert in csr status matches with the corresponding private key
			if c.keyData == nil {
				return nil, fmt.Errorf("no private key found for certificate in csr: %s", c.csrName)
			}
			_, err = tls.X509KeyPair(certData, c.keyData)
			if err != nil {
				return nil, fmt.Errorf("private key does not match with the certificate in csr: %s", c.csrName)
			}

			data := map[string][]byte{
				"grpc.yaml":     grpcOption.grpcConfig,
				csr.TLSCertFile: certData,
				csr.TLSKeyFile:  c.keyData,
			}

			return data, nil
		}()

		if err != nil {
			c.reset()
			return secret, &metav1.Condition{
				Type:    "ClusterCertificateRotated",
				Status:  metav1.ConditionFalse,
				Reason:  "ClientCertificateUpdateFailed",
				Message: fmt.Sprintf("Failed to rotated client certificate %v", err),
			}, err
		}
		if len(newSecretConfig) == 0 {
			return nil, nil, nil
		}
		// append additional data into client certificate secret
		for k, v := range newSecretConfig {
			secret.Data[k] = v
		}

		notBefore, notAfter, err := csr.GetCertValidityPeriod(secret)

		cond := &metav1.Condition{
			Type:    "ClusterCertificateRotated",
			Status:  metav1.ConditionTrue,
			Reason:  "ClientCertificateUpdated",
			Message: fmt.Sprintf("client certificate rotated starting from %v to %v", *notBefore, *notAfter),
		}

		if err != nil {
			cond = &metav1.Condition{
				Type:    "ClusterCertificateRotated",
				Status:  metav1.ConditionFalse,
				Reason:  "ClientCertificateUpdateFailed",
				Message: fmt.Sprintf("Failed to rotated client certificate %v", err),
			}
		} else {
			recorder.Eventf("ClientCertificateCreated", "A new client certificate for %s is available", controllerName)
		}
		c.reset()
		return secret, cond, err
	}

	// create a csr to request new client certificate if
	// a. there is no valid client certificate issued for the current cluster/agent;
	// b. client certificate is sensitive to the additional secret data and the data changes;
	// c. client certificate exists and has less than a random percentage range from 20% to 25% of its life remaining;
	shouldCreate, err := csr.ShouldCreateCSR(
		logger,
		controllerName,
		secret,
		recorder,
		grpcOption.Subject,
		additionalSecretData)
	if err != nil {
		return secret, nil, err
	}
	if !shouldCreate {
		return nil, nil, nil
	}

	keyData, createdCSRName, err := func() ([]byte, string, error) {
		// create a new private key
		keyData, err := keyutil.MakeEllipticPrivateKeyPEM()
		if err != nil {
			return nil, "", err
		}

		privateKey, err := keyutil.ParsePrivateKeyPEM(keyData)
		if err != nil {
			return keyData, "", fmt.Errorf("invalid private key for certificate request: %w", err)
		}
		csrData, err := certutil.MakeCSR(privateKey, grpcOption.Subject, grpcOption.DNSNames, nil)
		if err != nil {
			return keyData, "", fmt.Errorf("unable to generate certificate request: %w", err)
		}
		createdCSRName, err := grpcOption.control.create(
			ctx, recorder, grpcOption.ObjectMeta, csrData, grpcOption.SignerName, grpcOption.ExpirationSeconds)
		if err != nil {
			return keyData, "", err
		}
		return keyData, createdCSRName, nil
	}()
	if err != nil {
		return nil, &metav1.Condition{
			Type:    "ClusterCertificateRotated",
			Status:  metav1.ConditionFalse,
			Reason:  "ClientCertificateUpdateFailed",
			Message: fmt.Sprintf("Failed to create CSR %v", err),
		}, err
	}

	c.keyData = keyData
	c.csrName = createdCSRName
	return nil, nil, nil
}

func (c *GRPCDriver) BuildKubeConfigFromTemplate(template *clientcmdapi.Config) *clientcmdapi.Config {
	return nil
}

func (c *GRPCDriver) InformerHandler(option any) (cache.SharedIndexInformer, factory.EventFilterFunc) {
	grpcOption, ok := option.(*GRPCOption)
	if !ok {
		utilruntime.Must(fmt.Errorf("option type is not correct"))
	}
	return grpcOption.control.Informer(), nil
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
