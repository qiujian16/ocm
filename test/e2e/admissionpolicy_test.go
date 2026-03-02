package e2e

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	workclientset "open-cluster-management.io/api/client/work/clientset/versioned"
	clusterv1 "open-cluster-management.io/api/cluster/v1"
	workapiv1 "open-cluster-management.io/api/work/v1"

	"open-cluster-management.io/ocm/test/integration/util"
)

// Test cases for AdmissionPolicy (VAP + MAP) mode
// These tests verify that the same validations work when using ValidatingAdmissionPolicy
// and MutatingAdmissionPolicy instead of traditional webhooks
var _ = ginkgo.Describe("AdmissionPolicy mode validation", ginkgo.Label("admission-policy"), func() {
	var nameSuffix string
	var workName string

	ginkgo.BeforeEach(func() {
		// Skip if AdmissionPolicy feature not enabled
		if !isAdmissionPolicyEnabled() {
			ginkgo.Skip("AdmissionPolicy feature gate is not enabled")
		}

		nameSuffix = rand.String(5)
		workName = fmt.Sprintf("ap-w1-%s", nameSuffix)
	})

	ginkgo.Context("Verification of AdmissionPolicy deployment", func() {
		ginkgo.It("Should have ValidatingAdmissionPolicy resources deployed", func() {
			ginkgo.By("checking ValidatingAdmissionPolicy for ManifestWork exists")
			vap, err := getValidatingAdmissionPolicy("manifestworks.admission.work.open-cluster-management.io")
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(vap).ToNot(gomega.BeNil())

			ginkgo.By("checking ValidatingAdmissionPolicy for ManagedCluster exists")
			vap, err = getValidatingAdmissionPolicy("managedclusters.admission.cluster.open-cluster-management.io")
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(vap).ToNot(gomega.BeNil())

			ginkgo.By("checking ValidatingAdmissionPolicy for ManagedClusterSetBinding exists")
			vap, err = getValidatingAdmissionPolicy("managedclustersetbindings.admission.cluster.open-cluster-management.io")
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(vap).ToNot(gomega.BeNil())
		})

		// Note: MutatingAdmissionPolicy verification is skipped because the API
		// may not be available in client-go yet (still in beta in K8s 1.31)
		// ginkgo.It("Should have MutatingAdmissionPolicy resources deployed", func() {
		// 	ginkgo.By("checking MutatingAdmissionPolicy for ManagedCluster exists")
		// 	map_, err := getMutatingAdmissionPolicy("managedclusters.admission.cluster.open-cluster-management.io")
		// 	gomega.Expect(err).ToNot(gomega.HaveOccurred())
		// 	gomega.Expect(map_).ToNot(gomega.BeNil())
		// })

		ginkgo.It("Should NOT have webhook deployments", func() {
			ginkgo.By("checking registration webhook deployment is absent")
			_, err := hub.KubeClient.AppsV1().Deployments("open-cluster-management-hub").Get(
				context.Background(), "cluster-manager-registration-webhook", metav1.GetOptions{})
			gomega.Expect(errors.IsNotFound(err)).Should(gomega.BeTrue(),
				"registration webhook deployment should not exist when AdmissionPolicy is enabled")

			ginkgo.By("checking work webhook deployment is absent")
			_, err = hub.KubeClient.AppsV1().Deployments("open-cluster-management-hub").Get(
				context.Background(), "cluster-manager-work-webhook", metav1.GetOptions{})
			gomega.Expect(errors.IsNotFound(err)).Should(gomega.BeTrue(),
				"work webhook deployment should not exist when AdmissionPolicy is enabled")
		})

		ginkgo.It("Should NOT have webhook configurations", func() {
			ginkgo.By("checking ValidatingWebhookConfiguration for ManifestWork is absent")
			_, err := hub.KubeClient.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(
				context.Background(), "manifestworkvalidators.admission.work.open-cluster-management.io", metav1.GetOptions{})
			gomega.Expect(errors.IsNotFound(err)).Should(gomega.BeTrue(),
				"ValidatingWebhookConfiguration should not exist when AdmissionPolicy is enabled")

			ginkgo.By("checking MutatingWebhookConfiguration for ManagedCluster is absent")
			_, err = hub.KubeClient.AdmissionregistrationV1().MutatingWebhookConfigurations().Get(
				context.Background(), "managedclustermutators.admission.cluster.open-cluster-management.io", metav1.GetOptions{})
			gomega.Expect(errors.IsNotFound(err)).Should(gomega.BeTrue(),
				"MutatingWebhookConfiguration should not exist when AdmissionPolicy is enabled")
		})
	})

	ginkgo.Context("ManifestWork validation via ValidatingAdmissionPolicy", func() {
		ginkgo.AfterEach(func() {
			gomega.Expect(hub.CleanManifestWorks(universalClusterName, workName)).To(gomega.BeNil())
		})

		ginkgo.It("Should reject ManifestWork with empty manifests", func() {
			work := newManifestWork(universalClusterName, workName)
			_, err := hub.WorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
				context.Background(), work, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject empty manifests")
		})

		ginkgo.It("Should reject ManifestWork with manifest missing name", func() {
			work := newManifestWork(universalClusterName, workName,
				[]runtime.Object{util.NewConfigmap("default", "", nil, nil)}...)
			_, err := hub.WorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
				context.Background(), work, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject manifest without name")
		})

		ginkgo.It("Should reject ManifestWork with duplicate manifests", func() {
			work := newManifestWork(universalClusterName, workName, []runtime.Object{
				util.NewConfigmap("default", "cm1", nil, nil),
				util.NewConfigmap("default", "cm1", nil, nil), // duplicate
			}...)
			_, err := hub.WorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
				context.Background(), work, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject duplicate manifests")
		})

		ginkgo.It("Should reject ManifestWork exceeding size limit", func() {
			manifests := []workapiv1.Manifest{
				{RawExtension: runtime.RawExtension{Object: newSecretBySize("default", "test1", 100*1024)}},
				{RawExtension: runtime.RawExtension{Object: newSecretBySize("default", "test2", 100*1024)}},
				{RawExtension: runtime.RawExtension{Object: newSecretBySize("default", "test3", 100*1024)}},
				{RawExtension: runtime.RawExtension{Object: newSecretBySize("default", "test4", 100*1024)}},
				{RawExtension: runtime.RawExtension{Object: newSecretBySize("default", "test5", 100*1024)}},
			}

			work := newManifestWork(universalClusterName, workName)
			work.Spec.Workload.Manifests = manifests

			_, err := hub.WorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
				context.Background(), work, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject manifests exceeding 500KB limit")
		})

		ginkgo.It("Should accept valid ManifestWork", func() {
			work := newManifestWork(universalClusterName, workName,
				[]runtime.Object{util.NewConfigmap("default", "cm1", nil, nil)}...)
			_, err := hub.WorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
				context.Background(), work, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		ginkgo.Context("executor RBAC validation", func() {
			var hubUser string
			var roleName string

			ginkgo.BeforeEach(func() {
				hubUser = fmt.Sprintf("ap-sa-%s", nameSuffix)
				roleName = fmt.Sprintf("ap-role-%s", nameSuffix)

				// create a temporary role without execute-as permission
				_, err := hub.KubeClient.RbacV1().Roles(universalClusterName).Create(
					context.TODO(), &rbacv1.Role{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: universalClusterName,
							Name:      roleName,
						},
						Rules: []rbacv1.PolicyRule{
							{
								Verbs:     []string{"create", "update", "patch", "get", "list", "delete"},
								APIGroups: []string{"work.open-cluster-management.io"},
								Resources: []string{"manifestworks"},
							},
						},
					}, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				// create a temporary rolebinding
				_, err = hub.KubeClient.RbacV1().RoleBindings(universalClusterName).Create(
					context.TODO(), &rbacv1.RoleBinding{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: universalClusterName,
							Name:      roleName,
						},
						Subjects: []rbacv1.Subject{
							{
								Kind:      "ServiceAccount",
								Namespace: universalClusterName,
								Name:      hubUser,
							},
						},
						RoleRef: rbacv1.RoleRef{
							APIGroup: "rbac.authorization.k8s.io",
							Kind:     "Role",
							Name:     roleName,
						},
					}, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			})

			ginkgo.AfterEach(func() {
				hub.KubeClient.RbacV1().Roles(universalClusterName).Delete(context.TODO(), roleName, metav1.DeleteOptions{})
				hub.KubeClient.RbacV1().RoleBindings(universalClusterName).Delete(context.TODO(), roleName, metav1.DeleteOptions{})
			})

			ginkgo.It("Should reject ManifestWork without execute-as permission", func() {
				work := newManifestWork(universalClusterName, workName,
					[]runtime.Object{util.NewConfigmap("default", "cm1", nil, nil)}...)

				// Impersonate as user without execute-as permission
				hubClusterCfg, err := clientcmd.BuildConfigFromFlags("", hubKubeconfig)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				impersonatedConfig := *hubClusterCfg
				impersonatedConfig.Impersonate.UserName = fmt.Sprintf("system:serviceaccount:%s:%s",
					universalClusterName, hubUser)
				impersonatedHubWorkClient, err := workclientset.NewForConfig(&impersonatedConfig)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				_, err = impersonatedHubWorkClient.WorkV1().ManifestWorks(universalClusterName).Create(
					context.Background(), work, metav1.CreateOptions{})
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(),
					"VAP should reject without execute-as permission")
			})
		})
	})

	ginkgo.Context("ManagedCluster validation via ValidatingAdmissionPolicy", func() {
		var clusterName string

		ginkgo.BeforeEach(func() {
			clusterName = fmt.Sprintf("ap-cluster-%s", nameSuffix)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(hub.DeleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
		})

		ginkgo.It("Should reject ManagedCluster with invalid name format", func() {
			// Names with uppercase are invalid
			invalidCluster := newManagedCluster("Invalid-Cluster-Name", false, "https://127.0.0.1:8443")
			_, err := hub.ClusterClient.ClusterV1().ManagedClusters().Create(
				context.TODO(), invalidCluster, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject invalid DNS subdomain names")
		})

		ginkgo.It("Should reject ManagedCluster with non-HTTPS URL", func() {
			cluster := newManagedCluster(clusterName, false, "http://127.0.0.1:8080")
			_, err := hub.ClusterClient.ClusterV1().ManagedClusters().Create(
				context.TODO(), cluster, metav1.CreateOptions{})
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(errors.IsBadRequest(err) || errors.IsForbidden(err)).Should(gomega.BeTrue(),
				"VAP should reject non-HTTPS URLs")
		})

		ginkgo.It("Should accept valid ManagedCluster", func() {
			cluster := newManagedCluster(clusterName, false, "https://127.0.0.1:8443")
			_, err := hub.ClusterClient.ClusterV1().ManagedClusters().Create(
				context.TODO(), cluster, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})
	})

	ginkgo.Context("ManagedCluster mutation via MutatingAdmissionPolicy", func() {
		var clusterName string

		ginkgo.BeforeEach(func() {
			clusterName = fmt.Sprintf("ap-mut-cluster-%s", nameSuffix)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(hub.DeleteManageClusterAndRelatedNamespace(clusterName)).ToNot(gomega.HaveOccurred())
		})

		ginkgo.It("Should auto-populate taint timeAdded on create", func() {
			cluster := newManagedCluster(clusterName, false, "https://127.0.0.1:8443")
			cluster.Spec.Taints = []clusterv1.Taint{
				{
					Key:    "test-key",
					Value:  "test-value",
					Effect: clusterv1.TaintEffectNoSelect,
					// Don't set TimeAdded - should be auto-populated
				},
			}

			created, err := hub.ClusterClient.ClusterV1().ManagedClusters().Create(
				context.TODO(), cluster, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			gomega.Expect(created.Spec.Taints).To(gomega.HaveLen(1))
			gomega.Expect(created.Spec.Taints[0].TimeAdded).ToNot(gomega.BeNil(),
				"MAP should auto-populate taint timeAdded")
		})

		ginkgo.It("Should preserve existing taint timeAdded on update", func() {
			cluster := newManagedCluster(clusterName, false, "https://127.0.0.1:8443")
			cluster.Spec.Taints = []clusterv1.Taint{
				{
					Key:    "original-taint",
					Value:  "value",
					Effect: clusterv1.TaintEffectNoSelect,
				},
			}

			created, err := hub.ClusterClient.ClusterV1().ManagedClusters().Create(
				context.TODO(), cluster, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			originalTime := created.Spec.Taints[0].TimeAdded

			// Update with a new taint
			err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
				cluster, err := hub.ClusterClient.ClusterV1().ManagedClusters().Get(
					context.TODO(), clusterName, metav1.GetOptions{})
				if err != nil {
					return err
				}

				cluster.Spec.Taints = append(cluster.Spec.Taints, clusterv1.Taint{
					Key:    "new-taint",
					Value:  "new-value",
					Effect: clusterv1.TaintEffectNoSelect,
				})

				_, err = hub.ClusterClient.ClusterV1().ManagedClusters().Update(
					context.TODO(), cluster, metav1.UpdateOptions{})
				return err
			})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			updated, err := hub.ClusterClient.ClusterV1().ManagedClusters().Get(
				context.TODO(), clusterName, metav1.GetOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			// Original taint should preserve timeAdded
			var originalTaint *clusterv1.Taint
			for i := range updated.Spec.Taints {
				if updated.Spec.Taints[i].Key == "original-taint" {
					originalTaint = &updated.Spec.Taints[i]
					break
				}
			}
			gomega.Expect(originalTaint).ToNot(gomega.BeNil())
			gomega.Expect(originalTaint.TimeAdded.Equal(&originalTime)).To(gomega.BeTrue(),
				"MAP should preserve existing taint timeAdded")

			// New taint should have timeAdded set
			var newTaint *clusterv1.Taint
			for i := range updated.Spec.Taints {
				if updated.Spec.Taints[i].Key == "new-taint" {
					newTaint = &updated.Spec.Taints[i]
					break
				}
			}
			gomega.Expect(newTaint).ToNot(gomega.BeNil())
			gomega.Expect(newTaint.TimeAdded).ToNot(gomega.BeNil(),
				"MAP should set timeAdded for new taints")
		})
	})
})

// Helper functions

func isAdmissionPolicyEnabled() bool {
	// Check if AdmissionPolicy feature gate is enabled by looking for VAP resources
	_, err := getValidatingAdmissionPolicy("manifestworks.admission.work.open-cluster-management.io")
	return err == nil
}

func getValidatingAdmissionPolicy(name string) (interface{}, error) {
	// Try v1 first (K8s 1.32+)
	vap, err := hub.KubeClient.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(
		context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return vap, nil
	}

	// Fall back to v1beta1 (K8s 1.30-1.31)
	vapBeta, err := hub.KubeClient.AdmissionregistrationV1beta1().ValidatingAdmissionPolicies().Get(
		context.Background(), name, metav1.GetOptions{})
	if err == nil {
		return vapBeta, nil
	}

	return nil, err
}

// Note: getMutatingAdmissionPolicy is commented out because MutatingAdmissionPolicy
// API is not yet available in standard client-go (still in beta in K8s 1.31)
// func getMutatingAdmissionPolicy(name string) (interface{}, error) {
// 	// This would be used once MAP API is available in client-go
// 	return nil, fmt.Errorf("MutatingAdmissionPolicy API not yet available")
// }
