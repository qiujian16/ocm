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

	operatorapiv1 "open-cluster-management.io/api/operator/v1"
	"open-cluster-management.io/sdk-go/pkg/basecontroller/events"

	"open-cluster-management.io/ocm/manifests"
	"open-cluster-management.io/ocm/pkg/operator/helpers"
)

var (
	namespaceResource = "cluster-manager/cluster-manager-namespace.yaml"

	// Core RBAC resources (always deployed)
	coreRbacResourceFiles = []string{
		// registration
		"cluster-manager/hub/registration/clusterrole.yaml",
		"cluster-manager/hub/registration/clusterrolebinding.yaml",
		"cluster-manager/hub/registration/serviceaccount.yaml",
		// work executor admin
		"cluster-manager/hub/work/executor-admin-clusterrole.yaml",
		// placement
		"cluster-manager/hub/placement/clusterrole.yaml",
		"cluster-manager/hub/placement/clusterrolebinding.yaml",
		"cluster-manager/hub/placement/serviceaccount.yaml",
	}

	// Webhook-specific RBAC resources (only deployed when VAP is disabled)
	registrationWebhookRbacFiles = []string{
		"cluster-manager/hub/registration/webhook-clusterrole.yaml",
		"cluster-manager/hub/registration/webhook-clusterrolebinding.yaml",
		"cluster-manager/hub/registration/webhook-serviceaccount.yaml",
	}
	workWebhookRbacFiles = []string{
		"cluster-manager/hub/work/webhook-clusterrole.yaml",
		"cluster-manager/hub/work/webhook-clusterrolebinding.yaml",
		"cluster-manager/hub/work/webhook-serviceaccount.yaml",
	}
	addonWebhookRbacFiles = []string{
		"cluster-manager/hub/addon-manager/webhook-serviceaccount.yaml",
	}

	workControllerResourceFiles = []string{
		// manifestworkreplicaset
		"cluster-manager/hub/work/clusterrole.yaml",
		"cluster-manager/hub/work/clusterrolebinding.yaml",
		"cluster-manager/hub/work/serviceaccount.yaml",
	}

	hubAddOnManagerRbacResourceFiles = []string{
		// addon-manager
		"cluster-manager/hub/addon-manager/clusterrole.yaml",
		"cluster-manager/hub/addon-manager/clusterrolebinding.yaml",
		"cluster-manager/hub/addon-manager/work-executor-admin-clusterrolebinding.yaml",
		"cluster-manager/hub/addon-manager/serviceaccount.yaml",
	}

	// The hubHostedWebhookServiceFiles should only be deployed on the hub cluster when the deploy mode is hosted.
	hubDefaultWebhookServiceFiles = []string{
		"cluster-manager/hub/registration/webhook-service.yaml",
		"cluster-manager/hub/work/webhook-service.yaml",
		"cluster-manager/hub/addon-manager/webhook-service.yaml",
	}
	// Note: addon conversion webhook is not supported in hosted mode
	hubHostedWebhookServiceFiles = []string{
		"cluster-manager/hub/registration/webhook-service-hosted.yaml",
		"cluster-manager/hub/work/webhook-service-hosted.yaml",
	}

	// hubHostedWebhookEndpointFiles only apply when the deploy mode is hosted and address is IPFormat.
	// Note: addon conversion webhook is not supported in hosted mode
	hubHostedWebhookEndpointRegistration = "cluster-manager/hub/registration/webhook-endpoint-hosted.yaml"
	hubHostedWebhookEndpointWork         = "cluster-manager/hub/work/webhook-endpoint-hosted.yaml"

	grpcServerResourceFiles = []string{
		"cluster-manager/hub/grpc-server/clusterrole.yaml",
		"cluster-manager/hub/grpc-server/clusterrolebinding.yaml",
		"cluster-manager/hub/grpc-server/serviceaccount.yaml",
		"cluster-manager/hub/grpc-server/service.yaml",
	}
)

type hubReconcile struct {
	hubKubeClient kubernetes.Interface
	cache         resourceapply.ResourceCache
	recorder      events.Recorder
}

func (c *hubReconcile) reconcile(ctx context.Context, cm *operatorapiv1.ClusterManager,
	config manifests.HubConfig) (*operatorapiv1.ClusterManager, reconcileState, error) {
	// If AddOnManager is not enabled, remove related resources
	if !config.AddOnManagerEnabled {
		_, _, err := cleanResources(ctx, c.hubKubeClient, cm, config, hubAddOnManagerRbacResourceFiles...)
		if err != nil {
			return cm, reconcileStop, err
		}
	}

	// Remove work-controller deployment if feature not enabled
	if !config.WorkControllerEnabled {
		_, _, err := cleanResources(ctx, c.hubKubeClient, cm, config, workControllerResourceFiles...)
		if err != nil {
			return cm, reconcileStop, err
		}
	}

	// Remove grpc server related resources if grpc auth is disabled
	if !config.GRPCAuthEnabled {
		_, _, err := cleanResources(ctx, c.hubKubeClient, cm, config, grpcServerResourceFiles...)
		if err != nil {
			return cm, reconcileStop, err
		}
	}

	// Clean up webhook RBAC/services when AdmissionPolicy is enabled
	if config.RegistrationAPEnabled {
		_, _, err := cleanResources(ctx, c.hubKubeClient, cm, config, registrationWebhookRbacFiles...)
		if err != nil {
			return cm, reconcileStop, err
		}
		// Clean up webhook service files
		if helpers.IsHosted(cm.Spec.DeployOption.Mode) {
			cleanResources(ctx, c.hubKubeClient, cm, config, "cluster-manager/hub/registration/webhook-service-hosted.yaml")
			if config.RegistrationWebhook.HostedIsIPFormat {
				cleanResources(ctx, c.hubKubeClient, cm, config, hubHostedWebhookEndpointRegistration)
			}
		} else {
			cleanResources(ctx, c.hubKubeClient, cm, config, "cluster-manager/hub/registration/webhook-service.yaml")
		}
	}
	if config.WorkAPEnabled {
		_, _, err := cleanResources(ctx, c.hubKubeClient, cm, config, workWebhookRbacFiles...)
		if err != nil {
			return cm, reconcileStop, err
		}
		// Clean up webhook service files
		if helpers.IsHosted(cm.Spec.DeployOption.Mode) {
			cleanResources(ctx, c.hubKubeClient, cm, config, "cluster-manager/hub/work/webhook-service-hosted.yaml")
			if config.WorkWebhook.HostedIsIPFormat {
				cleanResources(ctx, c.hubKubeClient, cm, config, hubHostedWebhookEndpointWork)
			}
		} else {
			cleanResources(ctx, c.hubKubeClient, cm, config, "cluster-manager/hub/work/webhook-service.yaml")
		}
	}

	hubResources := getHubResources(cm.Spec.DeployOption.Mode, config)
	var appliedErrs []error

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
		hubResources...,
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
			Reason:  "HubResourceApplyFailed",
			Message: fmt.Sprintf("Failed to apply hub resources: %v", utilerrors.NewAggregate(appliedErrs)),
		})
		return cm, reconcileStop, utilerrors.NewAggregate(appliedErrs)
	}

	return cm, reconcileContinue, nil
}

func (c *hubReconcile) clean(ctx context.Context, cm *operatorapiv1.ClusterManager,
	config manifests.HubConfig) (*operatorapiv1.ClusterManager, reconcileState, error) {
	hubResources := getHubResources(cm.Spec.DeployOption.Mode, config)
	return cleanResources(ctx, c.hubKubeClient, cm, config, hubResources...)
}

func getHubResources(mode operatorapiv1.InstallMode, config manifests.HubConfig) []string {
	hubResources := []string{namespaceResource}
	// Add core RBAC resources
	hubResources = append(hubResources, coreRbacResourceFiles...)

	// All-or-nothing: Add webhook RBAC ONLY if AdmissionPolicy disabled
	if !config.RegistrationAPEnabled {
		hubResources = append(hubResources, registrationWebhookRbacFiles...)
	}
	if !config.WorkAPEnabled {
		hubResources = append(hubResources, workWebhookRbacFiles...)
	}

	if config.AddOnManagerEnabled {
		hubResources = append(hubResources, hubAddOnManagerRbacResourceFiles...)
		// Addon webhook (no VAP equivalent yet)
		hubResources = append(hubResources, addonWebhookRbacFiles...)
	}

	if config.WorkControllerEnabled {
		hubResources = append(hubResources, workControllerResourceFiles...)
	}

	if config.GRPCAuthEnabled {
		hubResources = append(hubResources, grpcServerResourceFiles...)
	}

	// Add webhook services
	// the hubHostedWebhookServiceFiles are only used in hosted mode
	// Note: addon conversion webhook is not supported in hosted mode

	// All-or-nothing: Services only if AdmissionPolicy disabled
	needRegistrationWebhook := !config.RegistrationAPEnabled
	needWorkWebhook := !config.WorkAPEnabled
	needAddonWebhook := config.AddOnManagerEnabled

	if helpers.IsHosted(mode) {
		if needRegistrationWebhook && config.RegistrationWebhook.HostedIsIPFormat {
			hubResources = append(hubResources, hubHostedWebhookEndpointRegistration)
		}
		if needWorkWebhook && config.WorkWebhook.HostedIsIPFormat {
			hubResources = append(hubResources, hubHostedWebhookEndpointWork)
		}
		// Add hosted webhook services
		if needRegistrationWebhook {
			hubResources = append(hubResources, "cluster-manager/hub/registration/webhook-service-hosted.yaml")
		}
		if needWorkWebhook {
			hubResources = append(hubResources, "cluster-manager/hub/work/webhook-service-hosted.yaml")
		}
	} else {
		// Default mode - add services for components that need webhooks
		if needRegistrationWebhook {
			hubResources = append(hubResources, "cluster-manager/hub/registration/webhook-service.yaml")
		}
		if needWorkWebhook {
			hubResources = append(hubResources, "cluster-manager/hub/work/webhook-service.yaml")
		}
		if needAddonWebhook {
			hubResources = append(hubResources, "cluster-manager/hub/addon-manager/webhook-service.yaml")
		}
	}

	return hubResources
}
