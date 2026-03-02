/*
 * Copyright 2022 Contributors to the Open Cluster Management project
 */

package clustermanagercontroller

import (
	"context"
	"fmt"

	"github.com/openshift/library-go/pkg/assets"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	ocmfeature "open-cluster-management.io/api/feature"
	operatorapiv1 "open-cluster-management.io/api/operator/v1"
	"open-cluster-management.io/sdk-go/pkg/basecontroller/events"

	"open-cluster-management.io/ocm/manifests"
	commonhelpers "open-cluster-management.io/ocm/pkg/common/helpers"
	"open-cluster-management.io/ocm/pkg/operator/helpers"
)

var (
	// Webhook configurations (deployed when AdmissionPolicy is disabled)
	hubRegistrationWebhookFiles = []string{
		"cluster-manager/hub/registration/webhook-validatingconfiguration.yaml",
		"cluster-manager/hub/registration/webhook-mutatingconfiguration.yaml",
		"cluster-manager/hub/registration/webhook-clustersetbinding-validatingconfiguration.yaml",
	}
	hubWorkWebhookFiles = []string{
		"cluster-manager/hub/work/webhook-validatingconfiguration.yaml",
	}

	// AdmissionPolicy resources (K8s 1.31+): VAP + MAP
	// These replace webhooks when AdmissionPolicy feature is enabled
	hubRegistrationAPResourceFiles = []string{
		// ValidatingAdmissionPolicy
		"cluster-manager/hub/registration/vap-managedcluster.yaml",
		"cluster-manager/hub/registration/vap-managedcluster-binding.yaml",
		"cluster-manager/hub/registration/vap-managedclustersetbinding.yaml",
		"cluster-manager/hub/registration/vap-managedclustersetbinding-binding.yaml",
		// MutatingAdmissionPolicy
		"cluster-manager/hub/registration/map-managedcluster.yaml",
		"cluster-manager/hub/registration/map-managedcluster-binding.yaml",
	}
	hubWorkAPResourceFiles = []string{
		"cluster-manager/hub/work/vap-manifestwork.yaml",
		"cluster-manager/hub/work/vap-manifestwork-binding.yaml",
	}
	hubWorkAPReplicaSetResourceFiles = []string{
		"cluster-manager/hub/work/vap-manifestworkreplicaset.yaml",
		"cluster-manager/hub/work/vap-manifestworkreplicaset-binding.yaml",
	}
)

type webhookReconcile struct {
	kubeClient    kubernetes.Interface
	hubKubeClient kubernetes.Interface

	cache    resourceapply.ResourceCache
	recorder events.Recorder
}

func (c *webhookReconcile) reconcile(ctx context.Context, cm *operatorapiv1.ClusterManager,
	config manifests.HubConfig) (*operatorapiv1.ClusterManager, reconcileState, error) {
	var appliedErrs []error

	if !meta.IsStatusConditionFalse(cm.Status.Conditions, operatorapiv1.ConditionProgressing) {
		return cm, reconcileStop, commonhelpers.NewRequeueError("Deployment is not ready", clusterManagerReSyncTime)
	}

	// All-or-nothing AdmissionPolicy approach:
	// When enabled: Deploy VAP + MAP policies, NO webhooks
	// When disabled: Deploy webhooks, NO policies
	var workFeatureGates []operatorapiv1.FeatureGate
	if cm.Spec.WorkConfiguration != nil {
		workFeatureGates = cm.Spec.WorkConfiguration.FeatureGates
	}
	manifestWorkReplicaSetEnabled := helpers.FeatureGateEnabled(
		workFeatureGates,
		ocmfeature.DefaultHubWorkFeatureGates,
		ocmfeature.ManifestWorkReplicaSet)

	var resourcesToApply []string

	// Registration: Either AdmissionPolicy (VAP+MAP) OR Webhooks
	if config.RegistrationAPEnabled {
		klog.Info("AdmissionPolicy feature enabled for registration, deploying VAP+MAP resources")
		resourcesToApply = append(resourcesToApply, hubRegistrationAPResourceFiles...)
		// Clean up webhooks
		if _, _, err := c.cleanWebhookResources(ctx, cm, config, hubRegistrationWebhookFiles...); err != nil {
			klog.Warningf("Failed to clean up registration webhook resources: %v", err)
		}
	} else {
		resourcesToApply = append(resourcesToApply, hubRegistrationWebhookFiles...)
		// Clean up AdmissionPolicy resources
		if _, _, err := c.cleanWebhookResources(ctx, cm, config, hubRegistrationAPResourceFiles...); err != nil {
			klog.Warningf("Failed to clean up registration AdmissionPolicy resources: %v", err)
		}
	}

	// Work: Either AdmissionPolicy (VAP) OR Webhooks
	if config.WorkAPEnabled {
		klog.Info("AdmissionPolicy feature enabled for work, deploying VAP resources")
		resourcesToApply = append(resourcesToApply, hubWorkAPResourceFiles...)
		// Deploy ManifestWorkReplicaSet VAP only if both features enabled
		if manifestWorkReplicaSetEnabled {
			resourcesToApply = append(resourcesToApply, hubWorkAPReplicaSetResourceFiles...)
		}
		// Clean up webhooks
		if _, _, err := c.cleanWebhookResources(ctx, cm, config, hubWorkWebhookFiles...); err != nil {
			klog.Warningf("Failed to clean up work webhook resources: %v", err)
		}
	} else {
		resourcesToApply = append(resourcesToApply, hubWorkWebhookFiles...)
		// Clean up AdmissionPolicy resources
		if _, _, err := c.cleanWebhookResources(ctx, cm, config, hubWorkAPResourceFiles...); err != nil {
			klog.Warningf("Failed to clean up work AdmissionPolicy resources: %v", err)
		}
		if _, _, err := c.cleanWebhookResources(ctx, cm, config, hubWorkAPReplicaSetResourceFiles...); err != nil {
			klog.Warningf("Failed to clean up work AdmissionPolicy replicaset resources: %v", err)
		}
	}

	// Apply the selected resources (either webhooks or VAPs)
	resourceResults := helpers.ApplyDirectly(
		ctx,
		c.hubKubeClient,
		nil,
		c.recorder,
		c.cache,
		func(name string) ([]byte, error) {
			template, err := manifests.ClusterManagerManifestFiles.ReadFile(name)
			if err != nil {
				return nil, err
			}
			objData := assets.MustCreateAssetFromTemplate(name, template, config).Data
			helpers.SetRelatedResourcesStatusesWithObj(ctx, &cm.Status.RelatedResources, objData)
			return objData, nil
		},
		resourcesToApply...,
	)

	for _, result := range resourceResults {
		if result.Error != nil {
			appliedErrs = append(appliedErrs, fmt.Errorf("%q (%T): %v", result.File, result.Type, result.Error))
		}
	}

	if len(appliedErrs) > 0 {
		meta.SetStatusCondition(&cm.Status.Conditions, metav1.Condition{
			Type:    operatorapiv1.ConditionClusterManagerApplied,
			Status:  metav1.ConditionFalse,
			Reason:  operatorapiv1.ReasonWebhookApplyFailed,
			Message: fmt.Sprintf("Failed to apply webhook/VAP resources: %v", utilerrors.NewAggregate(appliedErrs)),
		})
		return cm, reconcileStop, utilerrors.NewAggregate(appliedErrs)
	}

	return cm, reconcileContinue, nil
}

func (c *webhookReconcile) clean(ctx context.Context, cm *operatorapiv1.ClusterManager,
	config manifests.HubConfig) (*operatorapiv1.ClusterManager, reconcileState, error) {
	// Remove all webhook and AdmissionPolicy files
	var resourcesToClean []string
	resourcesToClean = append(resourcesToClean, hubRegistrationWebhookFiles...)
	resourcesToClean = append(resourcesToClean, hubWorkWebhookFiles...)
	resourcesToClean = append(resourcesToClean, hubRegistrationAPResourceFiles...)
	resourcesToClean = append(resourcesToClean, hubWorkAPResourceFiles...)
	resourcesToClean = append(resourcesToClean, hubWorkAPReplicaSetResourceFiles...)
	return cleanResources(ctx, c.kubeClient, cm, config, resourcesToClean...)
}

// cleanWebhookResources is a helper function to clean up specific webhook or VAP resources
func (c *webhookReconcile) cleanWebhookResources(ctx context.Context, cm *operatorapiv1.ClusterManager,
	config manifests.HubConfig, resources ...string) (*operatorapiv1.ClusterManager, reconcileState, error) {
	return cleanResources(ctx, c.hubKubeClient, cm, config, resources...)
}
