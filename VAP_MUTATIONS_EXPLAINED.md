# Why Registration Webhook is Still Needed with VAP

## TL;DR

When ValidatingAdmissionPolicy is enabled:
- ✅ **Work webhook**: Completely eliminated (validation → VAP)
- ⚠️ **Registration webhook**: Still deployed (mutations → webhook, validation → VAP)
- 🎯 **Result**: 50% webhook pod reduction + in-process validation

## The Problem: Mutations

ValidatingAdmissionPolicy (VAP) **only handles validation**. It cannot modify objects.

The registration webhook performs **mutations** on ManagedCluster that are essential for OCM functionality:

### 1. Taint TimeAdded Auto-Population

**What it does**: Automatically sets `spec.taints[].timeAdded` timestamp

```yaml
# User creates ManagedCluster with taint
apiVersion: cluster.open-cluster-management.io/v1
kind: ManagedCluster
metadata:
  name: cluster1
spec:
  taints:
  - key: "maintenance"
    value: "true"
    effect: "NoSelect"
    # timeAdded: NOT set by user

# Webhook mutates to add timeAdded
spec:
  taints:
  - key: "maintenance"
    value: "true"
    effect: "NoSelect"
    timeAdded: "2024-01-15T10:30:00Z"  # ← Auto-set by webhook
```

**Why it's needed**:
- Tracks when taints were added for audit/debugging
- Prevents users from manually setting timeAdded (security)
- Used by scheduling/placement logic

**On CREATE**: Sets timeAdded to current time for all taints
**On UPDATE**:
- New taints → set current time
- Unchanged taints → preserve original timeAdded
- Modified taints → set new time
- Reject if user tries to manually set timeAdded

### 2. Default ClusterSet Label

**What it does**: Adds default clusterset label when missing

```yaml
# User creates ManagedCluster without clusterset label
apiVersion: cluster.open-cluster-management.io/v1
kind: ManagedCluster
metadata:
  name: cluster1
  labels: {}

# Webhook mutates to add default clusterset
metadata:
  name: cluster1
  labels:
    cluster.open-cluster-management.io/clusterset: default  # ← Auto-added
```

**Why it's needed**:
- Ensures all clusters belong to a clusterset
- Enables RBAC and placement to work correctly
- Backwards compatibility for clusters created without clusterset

**Only when**:
- `DefaultClusterSet` feature gate is enabled
- No clusterset label exists or label is empty

## Why Not MutatingAdmissionPolicy?

MutatingAdmissionPolicy (MAP) **could** handle these mutations, but:

| Kubernetes Version | MAP Status | Stability |
|-------------------|------------|-----------|
| 1.30 | **Alpha** | Unstable, requires feature gate |
| 1.31 | **Beta** | More stable, but still evolving |
| 1.32+ | **GA** (expected) | Production ready |

**Our approach**:
1. **Now (K8s 1.30+)**: Use ValidatingAdmissionPolicy (GA) for validation, keep webhook for mutations
2. **Future (K8s 1.32+)**: Add MutatingAdmissionPolicy to eliminate webhook entirely

## Implementation Details

### Webhook Registration Logic

**File**: `pkg/registration/webhook/start.go`

```go
func SetupWebhookServer(opts *commonoptions.WebhookOptions) error {
    if features.HubMutableFeatureGate.Enabled(ocmfeature.ValidatingAdmissionPolicy) {
        klog.Info("VAP enabled, webhook will handle mutations only")
        // Register ManagedCluster webhook (implements both mutation and validation)
        // Only MutatingWebhookConfiguration will be deployed
        // ValidatingWebhookConfiguration is NOT deployed (VAP replaces it)
        opts.InstallWebhook(&internalv1.ManagedClusterWebhook{})
        // ManagedClusterSetBinding: Skip entirely (VAP handles it, no mutations)
    } else {
        // Traditional mode: register all webhooks
        opts.InstallWebhook(
            &internalv1.ManagedClusterWebhook{},
            &internalv1beta2.ManagedClusterSetBindingWebhook{})
    }
    return nil
}
```

### Webhook Configuration Deployment

**File**: `clustermanager_webhook_reconcile.go`

When VAP is enabled:
- ✅ Deploy: `webhook-mutatingconfiguration.yaml` (for ManagedCluster mutations)
- ✅ Deploy: VAP policies (for ManagedCluster validation)
- ❌ Skip: `webhook-validatingconfiguration.yaml` (replaced by VAP)
- ❌ Skip: `webhook-clustersetbinding-validatingconfiguration.yaml` (replaced by VAP)

### Resource Deployment

**Files**: `clustermanager_runtime_reconcile.go`, `clustermanager_hub_reconcile.go`

When VAP is enabled:

**Always deployed**:
- `cluster-manager-registration-webhook` deployment
- Registration webhook service
- Registration webhook RBAC

**Never deployed**:
- `cluster-manager-work-webhook` deployment (work has no mutations)
- Work webhook service
- Work webhook RBAC

## Benefits of Hybrid Approach

### Resource Savings

```
Before (VAP disabled):
├── registration-webhook deployment (validation + mutation)
└── work-webhook deployment (validation)
Total: 2 pods

After (VAP enabled):
└── registration-webhook deployment (mutation only)
Total: 1 pod (50% reduction)

Future (VAP + MAP):
Total: 0 pods (100% reduction)
```

### Performance Improvement

**Validation requests**:
- Before: Network call to webhook pod → latency ~10-50ms
- After: In-process CEL evaluation → latency ~1-2ms
- **Improvement**: 5-50x faster validation

**Mutation requests** (unchanged):
- Network call to webhook pod → latency ~10-50ms

### Overall Request Latency

For ManifestWork CREATE:
- Before: 10-50ms (validation webhook)
- After: 1-2ms (VAP in-process)
- **Improvement**: 5-50x faster

For ManagedCluster CREATE:
- Before: ~20-100ms (validation webhook + mutation webhook)
- After: ~11-52ms (VAP validation ~1-2ms + mutation webhook ~10-50ms)
- **Improvement**: ~2x faster

## Future: Complete Migration

Once MutatingAdmissionPolicy is GA in K8s 1.32+:

### Create MutatingAdmissionPolicy for Taints

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicy
metadata:
  name: managedcluster-taints
spec:
  matchConstraints:
    resourceRules:
    - apiGroups: ["cluster.open-cluster-management.io"]
      apiVersions: ["v1"]
      operations: ["CREATE", "UPDATE"]
      resources: ["managedclusters"]

  mutations:
  - patchType: "JSONPatch"
    condition: "has(object.spec.taints)"
    jsonPatch: |
      object.spec.taints.map(taint,
        has(oldObject.spec.taints.filter(t, t.key == taint.key)[0]) ?
          taint :  // existing taint, keep timeAdded
          taint.setField('timeAdded', string(time.now()))  // new taint
      )
```

### Create MutatingAdmissionPolicy for Default ClusterSet

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingAdmissionPolicy
metadata:
  name: managedcluster-default-clusterset
spec:
  matchConstraints:
    resourceRules:
    - apiGroups: ["cluster.open-cluster-management.io"]
      apiVersions: ["v1"]
      operations: ["CREATE", "UPDATE"]
      resources: ["managedclusters"]

  mutations:
  - patchType: "JSONPatch"
    condition: |
      !has(object.metadata.labels) ||
      !has(object.metadata.labels['cluster.open-cluster-management.io/clusterset']) ||
      object.metadata.labels['cluster.open-cluster-management.io/clusterset'] == ''
    jsonPatch: |
      [
        {
          "op": "add",
          "path": "/metadata/labels/cluster.open-cluster-management.io~1clusterset",
          "value": "default"
        }
      ]
```

### Then Eliminate Registration Webhook

Once MAP is deployed:
- ❌ Remove `cluster-manager-registration-webhook` deployment
- ❌ Remove registration webhook service/RBAC
- ❌ Remove `webhook-mutatingconfiguration.yaml`
- ✅ Keep VAP for validation
- ✅ Add MAP for mutations

**Result**: 100% in-process admission control, zero webhook pods!

## Summary

| Aspect | Current (Webhooks) | Hybrid (VAP) | Future (VAP + MAP) |
|--------|-------------------|--------------|---------------------|
| Webhook Pods | 2 | 1 | 0 |
| Validation | Webhooks | VAP (in-process) | VAP (in-process) |
| Mutations | Webhooks | Webhooks | MAP (in-process) |
| K8s Version | Any | 1.30+ | 1.32+ |
| Latency | Baseline | 2-50x faster validation | 2-50x faster overall |
| Resource Usage | Baseline | 50% reduction | 100% reduction |

The hybrid approach provides **immediate benefits** while setting up for **complete migration** when the ecosystem is ready.
