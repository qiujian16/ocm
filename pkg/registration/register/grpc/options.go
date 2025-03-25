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
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	"open-cluster-management.io/ocm/pkg/registration/hub/user"
	"open-cluster-management.io/ocm/pkg/registration/register"
	"open-cluster-management.io/ocm/pkg/registration/register/csr"
	cloudeventscsr "open-cluster-management.io/sdk-go/pkg/cloudevents/clients/csr"
	"open-cluster-management.io/sdk-go/pkg/cloudevents/generic/options/grpc"
	"os"
)

const signer = "open-cluster-management.io/grpc"

type GRPCOption struct {
	csrOption  *csr.CSROption
	grpcConfig []byte
}

func NewGRPCOption(ctx context.Context,
	csrExpirationSeconds int32, secretOption register.SecretOption, configFile string) (*GRPCOption, error) {
	var csrExpirationSecondsInCSROption *int32
	if csrExpirationSeconds != 0 {
		csrExpirationSecondsInCSROption = &csrExpirationSeconds
	}

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
		csrOption: &csr.CSROption{
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
			SignerName:        signer,
			ExpirationSeconds: csrExpirationSecondsInCSROption,
		},
		grpcConfig: configData,
	}, nil
}

type csrControl struct {
	csrClientHolder *cloudeventscsr.ClientHolder
}

func (v *csrControl) IsApproved(name string) (bool, error) {
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

func (v *csrControl) GetIssuedCertificate(name string) ([]byte, error) {
	csr, err := v.csrClientHolder.Clients().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return csr.Status.Certificate, nil
}

func (v *csrControl) Create(ctx context.Context, recorder events.Recorder, objMeta metav1.ObjectMeta, csrData []byte,
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
