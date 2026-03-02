# ValidatingAdmissionPolicy Migration Guide

## Overview

This document describes the migration from traditional ValidatingWebhookConfiguration to ValidatingAdmissionPolicy (VAP) for OCM admission control.

## Feature Gate Requirement

**IMPORTANT**: Before this implementation can be used, the following feature gate must be added to the API repository (`open-cluster-management.io/api`):

### Add to `feature/feature.go`:

```go
// AdmissionPolicy enables the use of Kubernetes AdmissionPolicy (VAP + MAP)
// instead of traditional webhooks for admission control. This provides better performance,
// lower latency, and improved scalability.
//
// When enabled on the hub:
// - Webhook deployments are NOT created
// - ValidatingAdmissionPolicy handles all validation (ManifestWork, ManagedCluster, etc.)
// - MutatingAdmissionPolicy handles all mutations (ManagedCluster taints, labels)
// - All admission control happens in-process in the API server
//
// Requirements:
// - Kubernetes 1.31+ (where MutatingAdmissionPolicy is Beta)
// - CEL authorizer support for RBAC checks
//
// Note: This is disabled by default to ensure backward compatibility
AdmissionPolicy featuregate.Feature = "AdmissionPolicy"
```

### Add to `DefaultHubWorkFeatureGates`:

```go
var DefaultHubWorkFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
    // ... existing gates ...
    AdmissionPolicy: {Default: false, PreRelease: featuregate.Alpha},
}
```

### Add to `DefaultHubRegistrationFeatureGates`:

```go
var DefaultHubRegistrationFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
    // ... existing gates ...
    AdmissionPolicy: {Default: false, PreRelease: featuregate.Alpha},
}
```

## Prerequisites

1. **Kubernetes Version**: 1.30 or later (where ValidatingAdmissionPolicy is GA)
2. **API Repository**: Feature gate added to `open-cluster-management.io/api`
3. **Vendor Update**: Run `go mod vendor` after API repository update

## Implementation Status

### Completed

- ✅ ValidatingAdmissionPolicy manifests created
  - ManifestWork VAP with CEL validations
  - ManifestWorkReplicaSet VAP
  - ManagedCluster VAP with RBAC checks
  - ManagedClusterSetBinding VAP with RBAC checks
- ✅ Webhook start.go files modified to skip registration when feature enabled
- ✅ Cluster manager controller updated for conditional VAP deployment
- ✅ Automatic cleanup of old resources when switching modes

### Pending

- ⏳ Feature gate definition in API repository
- ⏳ Unit tests for CEL expressions
- ⏳ Integration tests in VAP mode
- ⏳ E2E tests for feature gate toggle
- ⏳ Performance benchmarks

## How It Works

### Architecture

```
┌─────────────────────────────────────────┐
│     ClusterManager CR                   │
│  spec:                                  │
│    workConfiguration:                   │
│      featureGates:                      │
│        - feature: ValidatingAdmissionPolicy │
│          mode: Enable                   │
│    registrationConfiguration:           │
│      featureGates:                      │
│        - feature: ValidatingAdmissionPolicy │
│          mode: Enable                   │
└─────────────────────────────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────┐
│  Cluster Manager Controller             │
│  - Checks feature gates                 │
│  - Deploys VAP or Webhooks conditionally│
│  - Cleans up old resources              │
└─────────────────────────────────────────┘
                  │
         ┌────────┴────────┐
         ▼                 ▼
┌──────────────────┐ ┌──────────────────┐
│ VAP Mode         │ │ Webhook Mode     │
│ (Feature ON)     │ │ (Feature OFF)    │
├──────────────────┤ ├──────────────────┤
│ • Deploy VAPs    │ │ • Deploy Webhooks│
│ • NO webhook     │ │ • Deploy webhook │
│   deployments    │ │   deployments    │
│ • NO webhook     │ │ • Deploy webhook │
│   services/RBAC  │ │   services/RBAC  │
│ • In-process     │ │ • Webhook pods   │
│   validation     │ │   handle requests│
└──────────────────┘ └──────────────────┘
```

### Key Differences

| Aspect | Webhook Mode | VAP Mode |
|--------|--------------|----------|
| **Work Webhook** | | |
| - Deployment | ✅ Created | ❌ Not created |
| - Service/RBAC | ✅ Created | ❌ Not created |
| - ValidatingWebhookConfiguration | ✅ Created | ❌ Not created |
| - ValidatingAdmissionPolicy | ❌ Not created | ✅ Created |
| **Registration Webhook** | | |
| - Deployment | ✅ Created | ✅ **Still created** (for mutations) |
| - Service/RBAC | ✅ Created | ✅ **Still created** (for mutations) |
| - ValidatingWebhookConfiguration | ✅ Created | ❌ Not created (VAP instead) |
| - MutatingWebhookConfiguration | ✅ Created | ✅ **Still created** (for mutations) |
| - ValidatingAdmissionPolicy | ❌ Not created | ✅ Created |
| **Validation Location** | Webhook pods | API server (in-process) |
| **Mutation Location** | Webhook pods | Webhook pods (mutations still needed) |
| **Network Overhead** | Yes (all requests) | Partial (only mutations) |
| **Resource Usage** | Higher (2 webhook pods) | Lower (1 webhook pod) |

### Validation Mapping

| Webhook Validation | VAP CEL Expression |
|-------------------|-------------------|
| Empty manifests check | `size(object.spec.workload.manifests) > 0` |
| Size limit (500KB) | `variables.totalSize <= 512000` |
| Manifest name required | `object.spec.workload.manifests.all(m, has(m.metadata.name))` |
| No generateName | `!has(m.metadata.generateName)` |
| No duplicates | `size(keys) == size(keys.unique())` |
| Executor RBAC | `authorizer.group().resource().check().allowed()` |
| HubAcceptsClient RBAC | `authorizer.group().resource().subresource('accept').check('update').allowed()` |
| ClusterSet label RBAC | `authorizer.group().resource().subresource('join').check('create').allowed()` |

## Usage

### Enable VAP

```yaml
apiVersion: operator.open-cluster-management.io/v1
kind: ClusterManager
metadata:
  name: cluster-manager
spec:
  workConfiguration:
    featureGates:
    - feature: ValidatingAdmissionPolicy
      mode: Enable
  registrationConfiguration:
    featureGates:
    - feature: ValidatingAdmissionPolicy
      mode: Enable
```

### Verify Deployment

```bash
# Check VAP policies are deployed
kubectl get validatingadmissionpolicies
kubectl get validatingadmissionpolicybindings

# Check webhook deployments
kubectl get deployments -n open-cluster-management-hub | grep webhook
# Expected output:
# - cluster-manager-registration-webhook (PRESENT - needed for mutations)
# - cluster-manager-work-webhook (ABSENT - replaced by VAP)

# Verify webhook configurations
kubectl get validatingwebhookconfiguration | grep cluster-manager
# Expected:
# - managedclustervalidators (ABSENT - replaced by VAP)
# - managedclustersetbindingvalidators (ABSENT - replaced by VAP)
# - manifestworkvalidators (ABSENT - replaced by VAP)

kubectl get mutatingwebhookconfiguration | grep cluster-manager
# Expected:
# - managedclustermutators (PRESENT - still needed for mutations)

# Check registration webhook logs (should mention VAP mode)
kubectl logs -n open-cluster-management-hub deployment/cluster-manager-registration-webhook | \
  grep "ValidatingAdmissionPolicy"
# Expected: "ValidatingAdmissionPolicy feature enabled, registration webhook will handle mutations only"
```

### Test Validations

```bash
# Should fail - empty manifests
cat <<EOF | kubectl apply -f -
apiVersion: work.open-cluster-management.io/v1
kind: ManifestWork
metadata:
  name: test-empty
  namespace: cluster1
spec:
  workload:
    manifests: []
EOF

# Should succeed
cat <<EOF | kubectl apply -f -
apiVersion: work.open-cluster-management.io/v1
kind: ManifestWork
metadata:
  name: test-valid
  namespace: cluster1
spec:
  workload:
    manifests:
    - apiVersion: v1
      kind: ConfigMap
      metadata:
        name: test
        namespace: default
      data:
        key: value
EOF
```

### Rollback to Webhooks

```yaml
apiVersion: operator.open-cluster-management.io/v1
kind: ClusterManager
metadata:
  name: cluster-manager
spec:
  workConfiguration:
    featureGates:
    - feature: ValidatingAdmissionPolicy
      mode: Disable
  registrationConfiguration:
    featureGates:
    - feature: ValidatingAdmissionPolicy
      mode: Disable
```

The controller will automatically:
1. Remove VAP resources
2. Deploy webhook configurations
3. Webhook pods will register hooks on restart

## Important: Mutations Still Use Webhooks

**ValidatingAdmissionPolicy only handles validation.** Mutations still require webhooks.

### Why Registration Webhook Deployment is Still Needed

The registration webhook performs these mutations on ManagedCluster:

1. **Taint TimeAdded**: Automatically sets `spec.taints[].timeAdded` timestamp
   - On CREATE: Sets current time for all taints
   - On UPDATE: Sets current time for new/changed taints, preserves existing timestamps
   - Prevents users from manually setting timeAdded (security)

2. **Default ClusterSet Label**: Adds `cluster.open-cluster-management.io/clusterset: default` label
   - Only when `DefaultClusterSet` feature is enabled
   - Only if no clusterset label exists

### MutatingAdmissionPolicy Status

**Not yet used** in this implementation because:
- K8s 1.30: MutatingAdmissionPolicy is **Alpha** (feature gate required)
- K8s 1.31: MutatingAdmissionPolicy is **Beta**
- K8s 1.32+: MutatingAdmissionPolicy expected to be **GA**

**Future enhancement**: Once MutatingAdmissionPolicy is GA and widely available, we can:
- Create MutatingAdmissionPolicy for taint/label mutations
- Fully eliminate registration webhook deployment
- Achieve complete in-process admission control

### Resource Usage with VAP Enabled

```
Traditional (VAP disabled):
- Registration webhook deployment: validation + mutation
- Work webhook deployment: validation
Total: 2 webhook pods

Hybrid (VAP enabled):
- Registration webhook deployment: mutation only
- Work webhook deployment: NOT deployed
Total: 1 webhook pod (50% reduction)

Future (VAP + MAP):
- No webhook deployments
Total: 0 webhook pods (100% reduction)
```

## Known Limitations and Behavioral Changes

### 1. Fixed Size Limit

**Current (Webhooks)**: Configurable via `--manifestLimit` flag
**VAP**: Fixed at 500KB (512000 bytes)

**Impact**: Low - the flag is rarely changed in practice
**Workaround**: None - this is a limitation of how VAP works with static policies

### 2. Namespace Terminating Check Removed

**Current (Webhooks)**: ManagedCluster validation checks if cluster namespace is terminating before accepting HubAcceptsClient
**VAP**: Cannot read namespace status in CEL (no cross-resource reads allowed)

**Impact**: Low - extremely rare edge case
**Mitigation**: Controllers will catch this during reconciliation

### 3. NilExecutorValidating Behavior

**Current (Webhooks)**: When `NilExecutorValidating=false` (default), nil executors skip RBAC validation
**VAP**: Always validates executors (equivalent to `NilExecutorValidating=true`)

**Impact**: More secure behavior - VAP always validates using the default executor if none is specified
**Migration Note**: If you rely on nil executors bypassing validation, you must set `NilExecutorValidating=true` before enabling VAP

### 4. Kubernetes Version Requirement

**Requirement**: Kubernetes 1.30+

**Why**: ValidatingAdmissionPolicy became GA in 1.30 with full CEL authorizer support

**Impact**: Older clusters cannot use this feature
**Mitigation**: Feature gate disabled by default - webhooks continue to work

### 5. Webhook Deployments Not Created

**Current (Webhooks)**: Webhook deployments, services, and RBAC resources are always deployed
**VAP**: When enabled, webhook deployments and related resources are NOT created at all

**Impact**: Reduced resource usage and improved performance
**Migration Note**: When switching between modes, resources are automatically cleaned up

## Migration Timeline (Proposed)

### Release N (Current)
- Introduce VAP feature gate (disabled by default)
- Full backward compatibility with webhooks
- Alpha quality

### Release N+1
- Promote to Beta
- Enable VAP by default for K8s 1.30+
- Keep webhooks for older K8s versions

### Release N+2
- Promote to GA
- Recommend VAP for all new deployments
- Mark webhook code as deprecated

### Release N+3
- Remove webhook code
- VAP-only for K8s 1.30+
- Webhooks removed from codebase

## Benefits of VAP

### Performance
- **Lower latency**: In-process validation (no network call)
- **Better scalability**: No webhook pod overhead
- **Reduced resource usage**: No separate webhook deployment

### Operational
- **Simpler debugging**: CEL expressions visible in policy
- **Better observability**: Validation logic in K8s resources
- **Easier testing**: Can test CEL expressions directly

### Development
- **Declarative**: No Go code for simple validations
- **Portable**: Standard Kubernetes feature
- **Maintainable**: Validation logic co-located with API

## Troubleshooting

### VAP Not Deployed

**Symptom**: Feature gate enabled but VAPs not created

**Check**:
```bash
# Check K8s version
kubectl version --short

# Check feature gate in ClusterManager
kubectl get clustermanager cluster-manager -o yaml | grep -A5 featureGates

# Check controller logs
kubectl logs -n open-cluster-management-hub deployment/cluster-manager -c cluster-manager
```

**Solutions**:
- Ensure K8s >= 1.30
- Verify feature gate is correctly set
- Check controller logs for errors

### Validation Failures

**Symptom**: Resources rejected unexpectedly

**Check**:
```bash
# Check VAP status
kubectl get validatingadmissionpolicy -A -o yaml

# View CEL expression errors
kubectl get validatingadmissionpolicy <policy-name> -o jsonpath='{.status}'
```

**Solutions**:
- Review CEL expressions in VAP manifests
- Check RBAC permissions for authorizer checks
- Verify resource format matches expectations

### Performance Issues

**Symptom**: Slow admission response

**Check**:
```bash
# Monitor API server metrics
kubectl top pod -n kube-system | grep apiserver

# Check for resource constraints
kubectl describe nodes
```

**Solutions**:
- VAP should be faster than webhooks
- If slower, check API server resource allocation
- Review CEL expression complexity

## Files Modified

### Created
- `manifests/cluster-manager/hub/work/vap-manifestwork.yaml`
- `manifests/cluster-manager/hub/work/vap-manifestwork-binding.yaml`
- `manifests/cluster-manager/hub/work/vap-manifestworkreplicaset.yaml`
- `manifests/cluster-manager/hub/work/vap-manifestworkreplicaset-binding.yaml`
- `manifests/cluster-manager/hub/registration/vap-managedcluster.yaml`
- `manifests/cluster-manager/hub/registration/vap-managedcluster-binding.yaml`
- `manifests/cluster-manager/hub/registration/vap-managedclustersetbinding.yaml`
- `manifests/cluster-manager/hub/registration/vap-managedclustersetbinding-binding.yaml`

### Modified
- `pkg/work/webhook/start.go` - Skip webhook registration when VAP enabled
- `pkg/registration/webhook/start.go` - Skip webhook registration when VAP enabled
- `pkg/operator/operators/clustermanager/controllers/clustermanagercontroller/clustermanager_webhook_reconcile.go` - Conditional VAP/webhook deployment

## References

- [KEP-3488: CEL for Admission Control](https://github.com/kubernetes/enhancements/tree/master/keps/sig-api-machinery/3488-cel-admission-control)
- [ValidatingAdmissionPolicy Documentation](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/)
- [CEL Language Specification](https://github.com/google/cel-spec)
- [OCM Enhancement Proposal](TBD)
