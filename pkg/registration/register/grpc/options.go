package grpc

import (
	"context"
	"crypto/x509/pkix"
	"fmt"
	"github.com/ghodss/yaml"
	"github.com/openshift/library-go/pkg/operator/events"
	certificates "k8s.io/api/certificates/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/ocm/pkg/registration/hub/user"
	"open-cluster-management.io/ocm/pkg/registration/register"
	"open-cluster-management.io/ocm/pkg/registration/register/csr"
	cloudeventscsr "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr"
	csrstore "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr/store"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/options/grpc"
	"os"
)

type GRPCOption struct {
	// ObjectMeta is the ObjectMeta shared by all created csrs. It should use GenerateName instead of Name
	// to generate random csr names
	ObjectMeta metav1.ObjectMeta
	// Subject represents the subject of the client certificate used to create csrs
	Subject *pkix.Name
	// DNSNames represents DNS names used to create the client certificate
	DNSNames []string
	// SignerName is the name of the signer specified in the created csrs
	SignerName string

	// ExpirationSeconds is the requested duration of validity of the issued
	// certificate.
	// Certificate signers may not honor this field for various reasons:
	//
	//   1. Old signer that is unaware of the field (such as the in-tree
	//      implementations prior to v1.22)
	//   2. Signer whose configured maximum is shorter than the requested duration
	//   3. Signer whose configured minimum is longer than the requested duration
	//
	// The minimum valid value for expirationSeconds is 3600, i.e. 1 hour.
	ExpirationSeconds *int32

	control *csrControl

	// HaltCSRCreation halt the csr creation
	HaltCSRCreation func() bool

	grpcConfig []byte
}

func NewGRPCOption(ctx context.Context,
	csrExpirationSeconds int32, secretOption register.SecretOption, configFile string) (*GRPCOption, error) {
	var csrExpirationSecondsInCSROption *int32
	if csrExpirationSeconds != 0 {
		csrExpirationSecondsInCSROption = &csrExpirationSeconds
	}
	csrWatcherStore := csrstore.NewAgentInformerWatcherStore()
	_, grpcConfig, err := generic.NewConfigLoader("grpc", configFile).
		LoadConfig()
	if err != nil {
		return nil, fmt.Errorf(
			"failed to load hub registration config from file %q: %w",
			configFile, err)
	}
	csrClientHolder, err := cloudeventscsr.NewClientHolderBuilder(grpcConfig).
		WithClientID(secretOption.ClusterName).
		WithClusterName(secretOption.ClusterName).
		WithCodec(cloudeventscsr.NewCSRCodec()).
		WithCSRClientWatcherStore(csrWatcherStore).
		NewAgentClientHolder(ctx)
	if err != nil {
		return nil, err
	}
	klog.Infof("grpc configfile ready %s", configFile)

	configData, err := os.ReadFile(configFile)
	if err != nil {
		return nil, err
	}
	config := &grpc.GRPCConfig{}
	if err := yaml.Unmarshal(configData, config); err != nil {
		return nil, err
	}
	config.ClientKeyFile = csr.TLSKeyFile
	config.ClientCertFile = csr.TLSCertFile
	configData, err = yaml.Marshal(config)
	if err != nil {
		return nil, err
	}

	return &GRPCOption{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("%s-", secretOption.ClusterName),
			Labels: map[string]string{
				// the label is only an hint for cluster name. Anyone could set/modify it.
				clusterv1.ClusterNameLabelKey: secretOption.ClusterName,
			},
		},
		Subject: &pkix.Name{
			Organization: []string{
				fmt.Sprintf("%s%s", user.SubjectPrefix, secretOption.ClusterName),
				user.ManagedClustersGroup,
			},
			CommonName: fmt.Sprintf("%s%s:%s", user.SubjectPrefix, secretOption.ClusterName, secretOption.AgentName),
		},
		SignerName:        "open-cluster-management.io/grpc",
		control:           &csrControl{csrClientHolder: csrClientHolder},
		ExpirationSeconds: csrExpirationSecondsInCSROption,
		grpcConfig:        configData,
	}, nil
}

type csrControl struct {
	csrClientHolder *cloudeventscsr.ClientHolder
}

func (v *csrControl) isApproved(name string) (bool, error) {
	csr, err := v.csrClientHolder.Clients().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	approved := false
	for _, condition := range csr.Status.Conditions {
		if condition.Type == certificates.CertificateDenied {
			return false, nil
		} else if condition.Type == certificates.CertificateApproved {
			approved = true
		}
	}
	return approved, nil
}

func (v *csrControl) getIssuedCertificate(name string) ([]byte, error) {
	csr, err := v.csrClientHolder.Clients().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return csr.Status.Certificate, nil
}

func (v *csrControl) create(ctx context.Context, recorder events.Recorder, objMeta metav1.ObjectMeta, csrData []byte,
	signerName string, expirationSeconds *int32) (string, error) {
	csr := &certificates.CertificateSigningRequest{
		ObjectMeta: objMeta,
		Spec: certificates.CertificateSigningRequestSpec{
			Request: csrData,
			Usages: []certificates.KeyUsage{
				certificates.UsageDigitalSignature,
				certificates.UsageKeyEncipherment,
				certificates.UsageClientAuth,
			},
			SignerName:        signerName,
			ExpirationSeconds: expirationSeconds,
		},
	}

	req, err := v.csrClientHolder.Clients().Create(ctx, csr, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	recorder.Eventf("CSRCreated", "A csr %q is created", req.Name)
	return req.Name, nil
}

func (v *csrControl) Informer() cache.SharedIndexInformer {
	return v.csrClientHolder.Informer()
}
