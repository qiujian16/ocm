package webhook

import (
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"

	clusterv1 "open-cluster-management.io/api/cluster/v1"
	ocmfeature "open-cluster-management.io/api/feature"

	commonoptions "open-cluster-management.io/ocm/pkg/common/options"
	"open-cluster-management.io/ocm/pkg/features"
	internalv1 "open-cluster-management.io/ocm/pkg/registration/webhook/v1"
	internalv1beta2 "open-cluster-management.io/ocm/pkg/registration/webhook/v1beta2"
)

func SetupWebhookServer(opts *commonoptions.WebhookOptions) error {
	// When AdmissionPolicy feature is enabled, skip webhook registration entirely
	// The cluster manager controller will deploy ValidatingAdmissionPolicy + MutatingAdmissionPolicy instead
	if features.HubMutableFeatureGate.Enabled(ocmfeature.AdmissionPolicy) {
		klog.Info("AdmissionPolicy feature enabled, skipping registration webhook registration")
		return nil
	}

	if err := opts.InstallScheme(
		clientgoscheme.AddToScheme,
		clusterv1.Install,
		internalv1beta2.Install,
	); err != nil {
		return err
	}

	// Traditional mode: register all webhooks for both validation and mutation
	opts.InstallWebhook(
		&internalv1.ManagedClusterWebhook{},
		&internalv1beta2.ManagedClusterSetBindingWebhook{})

	return nil
}
