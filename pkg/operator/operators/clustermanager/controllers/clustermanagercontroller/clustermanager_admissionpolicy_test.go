package clustermanagercontroller

import (
	"context"
	"testing"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clienttesting "k8s.io/client-go/testing"

	operatorapiv1 "open-cluster-management.io/api/operator/v1"

	testingcommon "open-cluster-management.io/ocm/pkg/common/testing"
)

// TestAdmissionPolicyFeatureGate tests the AdmissionPolicy feature gate behavior
func TestAdmissionPolicyFeatureGate(t *testing.T) {
	tests := []struct {
		name                string
		apEnabled           bool
		expectWebhooks      bool
		expectPolicies      bool
		expectDeployments   []string
		notExpectDeployments []string
	}{
		{
			name:       "AP disabled - should deploy webhooks",
			apEnabled:  false,
			expectWebhooks: true,
			expectPolicies: false,
			expectDeployments: []string{
				"cluster-manager-registration-webhook",
				"cluster-manager-work-webhook",
			},
			notExpectDeployments: nil,
		},
		{
			name:       "AP enabled - should deploy policies not webhooks",
			apEnabled:  true,
			expectWebhooks: false,
			expectPolicies: true,
			expectDeployments: nil,
			notExpectDeployments: []string{
				"cluster-manager-registration-webhook",
				"cluster-manager-work-webhook",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create cluster manager with AdmissionPolicy feature gate
			cm := newClusterManagerWithAdmissionPolicy("test-clustermanager", tt.apEnabled)

			// Create test controller
			controller := newTestController(t, cm)

			// Run reconcile
			err := controller.clusterManagerController.sync(context.TODO(), testingcommon.NewFakeSyncContext(t, "test"), "test-clustermanager")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify webhook deployments based on feature gate
			if tt.expectWebhooks {
				verifyWebhookResourcesExist(t, controller, tt.expectDeployments)
			} else {
				verifyWebhookResourcesNotExist(t, controller, tt.notExpectDeployments)
			}

			// Verify AdmissionPolicy resources based on feature gate
			if tt.expectPolicies {
				verifyPolicyResourcesExist(t, controller)
			} else {
				verifyPolicyResourcesNotExist(t, controller)
			}
		})
	}
}

// TestAdmissionPolicyToggle tests toggling the AdmissionPolicy feature gate
func TestAdmissionPolicyToggle(t *testing.T) {
	// Start with AP disabled
	cm := newClusterManagerWithAdmissionPolicy("test-clustermanager", false)
	controller := newTestController(t, cm)

	// Initial reconcile - should deploy webhooks
	err := controller.clusterManagerController.sync(context.TODO(), testingcommon.NewFakeSyncContext(t, "test"), "test-clustermanager")
	if err != nil {
		t.Fatalf("unexpected error on initial sync: %v", err)
	}

	// Verify webhooks exist
	deployment, err := controller.hubKubeClient.AppsV1().Deployments("open-cluster-management-hub").Get(
		context.TODO(), "cluster-manager-registration-webhook", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected registration webhook deployment to exist: %v", err)
	}
	if deployment == nil {
		t.Fatal("registration webhook deployment should not be nil")
	}

	// Enable AdmissionPolicy feature gate
	cm.Spec.RegistrationConfiguration = &operatorapiv1.RegistrationHubConfiguration{
		FeatureGates: []operatorapiv1.FeatureGate{
			{Feature: "AdmissionPolicy", Mode: operatorapiv1.FeatureGateModeTypeEnable},
		},
	}
	cm.Spec.WorkConfiguration.FeatureGates = append(cm.Spec.WorkConfiguration.FeatureGates,
		operatorapiv1.FeatureGate{Feature: "AdmissionPolicy", Mode: operatorapiv1.FeatureGateModeTypeEnable})

	// Update cluster manager
	_, err = controller.operatorClient.OperatorV1().ClusterManagers().Update(context.TODO(), cm, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("failed to update cluster manager: %v", err)
	}

	// Reconcile again - should switch to policies
	err = controller.clusterManagerController.sync(context.TODO(), testingcommon.NewFakeSyncContext(t, "test"), "test-clustermanager")
	if err != nil {
		t.Fatalf("unexpected error after enabling AP: %v", err)
	}

	// Verify webhooks are cleaned up
	_, err = controller.hubKubeClient.AppsV1().Deployments("open-cluster-management-hub").Get(
		context.TODO(), "cluster-manager-registration-webhook", metav1.GetOptions{})
	if !errors.IsNotFound(err) {
		t.Fatalf("expected registration webhook deployment to be deleted, got: %v", err)
	}

	// Verify ValidatingAdmissionPolicy resources exist
	created := false
	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "create" {
			createAction := action.(clienttesting.CreateAction)
			obj := createAction.GetObject()
			if vap, ok := obj.(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				if vap.Name == "managedclusters.admission.cluster.open-cluster-management.io" {
					created = true
					break
				}
			}
			if vap, ok := obj.(*admissionregistrationv1beta1.ValidatingAdmissionPolicy); ok {
				if vap.Name == "managedclusters.admission.cluster.open-cluster-management.io" {
					created = true
					break
				}
			}
		}
	}
	if !created {
		t.Error("expected ValidatingAdmissionPolicy to be created")
	}
}

// TestAdmissionPolicyResourceCleanup tests that webhook resources are properly cleaned up when AP is enabled
func TestAdmissionPolicyResourceCleanup(t *testing.T) {
	cm := newClusterManagerWithAdmissionPolicy("test-clustermanager", true)
	controller := newTestController(t, cm)

	// Create some webhook resources that should be cleaned up
	namespace := "open-cluster-management-hub"

	// Create webhook deployment
	webhookDeployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-manager-registration-webhook",
			Namespace: namespace,
		},
	}
	_, err := controller.hubKubeClient.AppsV1().Deployments(namespace).Create(
		context.TODO(), webhookDeployment, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create webhook deployment: %v", err)
	}

	// Create webhook service
	webhookService := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cluster-manager-registration-webhook",
			Namespace: namespace,
		},
	}
	_, err = controller.hubKubeClient.CoreV1().Services(namespace).Create(
		context.TODO(), webhookService, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create webhook service: %v", err)
	}

	// Create webhook RBAC
	webhookSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "registration-webhook-sa",
			Namespace: namespace,
		},
	}
	_, err = controller.hubKubeClient.CoreV1().ServiceAccounts(namespace).Create(
		context.TODO(), webhookSA, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("failed to create webhook service account: %v", err)
	}

	// Run reconcile with AP enabled - should clean up webhook resources
	err = controller.clusterManagerController.sync(context.TODO(), testingcommon.NewFakeSyncContext(t, "test"), "test-clustermanager")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify webhook deployment is deleted
	deleted := false
	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "delete" {
			deleteAction := action.(clienttesting.DeleteAction)
			if deleteAction.GetName() == "cluster-manager-registration-webhook" {
				deleted = true
				break
			}
		}
	}
	if !deleted {
		t.Error("expected webhook deployment to be deleted")
	}
}

// Helper functions

func newClusterManagerWithAdmissionPolicy(name string, apEnabled bool) *operatorapiv1.ClusterManager {
	cm := newClusterManager(name)

	if apEnabled {
		// Enable AdmissionPolicy feature gate for both registration and work
		cm.Spec.RegistrationConfiguration = &operatorapiv1.RegistrationHubConfiguration{
			FeatureGates: []operatorapiv1.FeatureGate{
				{Feature: "AdmissionPolicy", Mode: operatorapiv1.FeatureGateModeTypeEnable},
			},
		}
		cm.Spec.WorkConfiguration.FeatureGates = append(cm.Spec.WorkConfiguration.FeatureGates,
			operatorapiv1.FeatureGate{Feature: "AdmissionPolicy", Mode: operatorapiv1.FeatureGateModeTypeEnable})
	}

	return cm
}

func verifyWebhookResourcesExist(t *testing.T, controller *testController, deploymentNames []string) {
	// Check deployments
	for _, name := range deploymentNames {
		found := false
		for _, action := range controller.hubKubeClient.Actions() {
			if action.GetVerb() == "create" {
				createAction := action.(clienttesting.CreateAction)
				if deployment, ok := createAction.GetObject().(*appsv1.Deployment); ok {
					if deployment.Name == name {
						found = true
						break
					}
				}
			}
		}
		if !found {
			t.Errorf("expected deployment %s to be created", name)
		}
	}

	// Check ValidatingWebhookConfiguration
	found := false
	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "create" {
			createAction := action.(clienttesting.CreateAction)
			if _, ok := createAction.GetObject().(*admissionregistrationv1.ValidatingWebhookConfiguration); ok {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected ValidatingWebhookConfiguration to be created")
	}
}

func verifyWebhookResourcesNotExist(t *testing.T, controller *testController, deploymentNames []string) {
	// Verify deployments are NOT created
	for _, name := range deploymentNames {
		for _, action := range controller.hubKubeClient.Actions() {
			if action.GetVerb() == "create" {
				createAction := action.(clienttesting.CreateAction)
				if deployment, ok := createAction.GetObject().(*appsv1.Deployment); ok {
					if deployment.Name == name {
						t.Errorf("did not expect deployment %s to be created", name)
					}
				}
			}
		}
	}

	// Verify ValidatingWebhookConfiguration is NOT created
	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "create" {
			createAction := action.(clienttesting.CreateAction)
			if vwc, ok := createAction.GetObject().(*admissionregistrationv1.ValidatingWebhookConfiguration); ok {
				if contains(vwc.Name, "manifestwork") || contains(vwc.Name, "managedcluster") {
					t.Errorf("did not expect ValidatingWebhookConfiguration %s to be created", vwc.Name)
				}
			}
		}
	}
}

func verifyPolicyResourcesExist(t *testing.T, controller *testController) {
	// Check for ValidatingAdmissionPolicy
	vapFound := false
	// Note: MutatingAdmissionPolicy checking is skipped because it may not be available
	// in the standard k8s.io/api package yet (still in beta in K8s 1.31)

	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "create" {
			createAction := action.(clienttesting.CreateAction)

			// Check v1 ValidatingAdmissionPolicy
			if vap, ok := createAction.GetObject().(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				if contains(vap.Name, "manifestwork") || contains(vap.Name, "managedcluster") {
					vapFound = true
				}
			}

			// Check v1beta1 ValidatingAdmissionPolicy (for K8s 1.30-1.31 compatibility)
			if vap, ok := createAction.GetObject().(*admissionregistrationv1beta1.ValidatingAdmissionPolicy); ok {
				if contains(vap.Name, "manifestwork") || contains(vap.Name, "managedcluster") {
					vapFound = true
				}
			}
		}
	}

	if !vapFound {
		t.Error("expected ValidatingAdmissionPolicy to be created")
	}
}

func verifyPolicyResourcesNotExist(t *testing.T, controller *testController) {
	for _, action := range controller.hubKubeClient.Actions() {
		if action.GetVerb() == "create" {
			createAction := action.(clienttesting.CreateAction)

			// Check v1 ValidatingAdmissionPolicy
			if vap, ok := createAction.GetObject().(*admissionregistrationv1.ValidatingAdmissionPolicy); ok {
				if contains(vap.Name, "manifestwork") || contains(vap.Name, "managedcluster") {
					t.Errorf("did not expect ValidatingAdmissionPolicy %s to be created", vap.Name)
				}
			}

			// Check v1beta1 ValidatingAdmissionPolicy
			if vap, ok := createAction.GetObject().(*admissionregistrationv1beta1.ValidatingAdmissionPolicy); ok {
				if contains(vap.Name, "manifestwork") || contains(vap.Name, "managedcluster") {
					t.Errorf("did not expect ValidatingAdmissionPolicy %s to be created", vap.Name)
				}
			}

			// Note: MutatingAdmissionPolicy checking is skipped because it may not be available
			// in the standard k8s.io/api package yet (still in beta in K8s 1.31)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr ||
		   len(s) >= len(substr) && s[len(s)-len(substr):] == substr ||
		   findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
