package aws_irsa

import (
	"github.com/openshift/library-go/pkg/controller/factory"
	"k8s.io/apimachinery/pkg/api/meta"

	addonv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	"open-cluster-management.io/ocm/pkg/registration/register"
)

// AWSOption includes options that is used to monitor ManagedClusters
type AWSOption struct {
	EventFilterFunc factory.EventFilterFunc
}

func NewAWSOption(
	secretOption register.SecretOption) (*AWSOption, error) {
	return &AWSOption{
		EventFilterFunc: func(obj interface{}) bool {
			accessor, err := meta.Accessor(obj)
			if err != nil {
				return false
			}
			labels := accessor.GetLabels()

			// should not contain addon key
			_, ok := labels[addonv1alpha1.AddonLabelKey]
			if ok {
				return false
			}

			// only enqueue csr whose name starts with the cluster name
			return accessor.GetName() == secretOption.ClusterName
		},
	}, nil
}
