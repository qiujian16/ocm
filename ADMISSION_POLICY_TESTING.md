# AdmissionPolicy Feature Testing Guide

## Overview

This document describes the test coverage for the AdmissionPolicy feature, which migrates OCM's admission control from traditional webhooks to Kubernetes AdmissionPolicy (ValidatingAdmissionPolicy + MutatingAdmissionPolicy).

## Test Files Created

### 1. Integration Tests

**File**: `pkg/operator/operators/clustermanager/controllers/clustermanagercontroller/clustermanager_admissionpolicy_test.go`

**Purpose**: Test the cluster manager controller's behavior when the AdmissionPolicy feature gate is toggled.

**Test Cases**:

#### TestAdmissionPolicyFeatureGate
Tests that the controller correctly deploys resources based on the feature gate setting.

**Scenarios**:
- **AP disabled**: Should deploy webhook deployments, services, RBAC, ValidatingWebhookConfiguration, MutatingWebhookConfiguration
- **AP enabled**: Should deploy ValidatingAdmissionPolicy, MutatingAdmissionPolicy, NO webhook resources

**Verification**:
- Webhook deployment creation/absence
- ValidatingWebhookConfiguration creation/absence
- ValidatingAdmissionPolicy creation/absence
- Note: MutatingAdmissionPolicy verification is commented out because the API is not yet available in standard k8s.io/api (still beta in K8s 1.31)

#### TestAdmissionPolicyToggle
Tests the dynamic behavior when the feature gate is toggled at runtime.

**Flow**:
1. Start with AP disabled
2. Reconcile → Verify webhooks deployed
3. Enable AP feature gate
4. Reconcile → Verify webhooks removed, policies deployed
5. Verify clean transition

**Purpose**: Ensures operators can safely switch between modes without manual intervention.

#### TestAdmissionPolicyResourceCleanup
Tests that webhook resources are properly cleaned up when switching to AP mode.

**Setup**:
- Manually create webhook deployment, service, service account
- Enable AP feature gate
- Reconcile

**Verification**:
- All webhook resources are deleted
- No leftover webhook components

### 2. E2E Tests

**File**: `test/e2e/admissionpolicy_test.go`

**Purpose**: End-to-end validation that admission control works correctly using AdmissionPolicy instead of webhooks.

**Test Cases**:

#### Verification of AdmissionPolicy deployment
- Checks ValidatingAdmissionPolicy resources exist for:
  - ManifestWork
  - ManagedCluster
  - ManagedClusterSetBinding
- Checks webhook deployments are NOT present
- Checks webhook configurations are NOT present

#### ManifestWork validation via ValidatingAdmissionPolicy
Verifies all ManifestWork validations work through VAP:

**Rejection tests**:
- Empty manifests
- Manifest missing name
- Duplicate manifests
- Manifests exceeding 500KB size limit
- Missing execute-as RBAC permission

**Acceptance tests**:
- Valid ManifestWork with proper manifests

**RBAC test**:
- Creates temporary role without execute-as permission
- Impersonates user
- Verifies ManifestWork creation is rejected by VAP
- Tests CEL authorizer integration

#### ManagedCluster validation via ValidatingAdmissionPolicy
**Rejection tests**:
- Invalid name format (uppercase characters)
- Non-HTTPS URLs

**Acceptance tests**:
- Valid ManagedCluster with HTTPS URL

#### ManagedCluster mutation via MutatingAdmissionPolicy
**Auto-population of taint timeAdded**:
- Creates ManagedCluster with taint (no timeAdded set)
- Verifies timeAdded is auto-populated
- Tests MAP mutation on CREATE

**Preservation of existing timeAdded**:
- Creates ManagedCluster with taint
- Captures original timeAdded
- Updates cluster to add new taint
- Verifies original taint's timeAdded is preserved
- Verifies new taint has timeAdded set
- Tests MAP mutation on UPDATE

## Test Coverage

### Validation Rules Tested

| Resource | Validation | Test File | Status |
|----------|-----------|-----------|--------|
| ManifestWork | Non-empty manifests | E2E | ✅ |
| ManifestWork | Size limit (500KB) | E2E | ✅ |
| ManifestWork | Manifest has name | E2E | ✅ |
| ManifestWork | No duplicates | E2E | ✅ |
| ManifestWork | Executor RBAC | E2E | ✅ |
| ManagedCluster | Name format | E2E | ✅ |
| ManagedCluster | HTTPS URLs | E2E | ✅ |
| ManagedCluster | HubAcceptsClient RBAC | Manual | ⏳ |
| ManagedCluster | ClusterSet label RBAC | Manual | ⏳ |
| ManagedClusterSetBinding | Name matching | Manual | ⏳ |
| ManagedClusterSetBinding | Bind permission | Manual | ⏳ |

### Mutation Rules Tested

| Resource | Mutation | Test File | Status |
|----------|---------|-----------|--------|
| ManagedCluster | Taint timeAdded (CREATE) | E2E | ✅ |
| ManagedCluster | Taint timeAdded (UPDATE) | E2E | ✅ |
| ManagedCluster | Default clusterset label | Manual | ⏳ |

### Controller Behavior Tested

| Behavior | Test File | Status |
|----------|-----------|--------|
| AP disabled → Deploy webhooks | Integration | ✅ |
| AP enabled → Deploy policies | Integration | ✅ |
| Toggle AP → Clean transition | Integration | ✅ |
| Resource cleanup on mode switch | Integration | ✅ |
| Feature gate detection | E2E | ✅ |

## Running the Tests

### Prerequisites

1. **Kubernetes cluster**: Version 1.31+ (for MutatingAdmissionPolicy Beta support)
2. **Feature gate**: AdmissionPolicy feature gate must be defined in `open-cluster-management.io/api` repository
3. **Test environment**: OCM hub cluster with ClusterManager installed

### Run Integration Tests

```bash
# Run all cluster manager controller tests
make test-integration

# Run only AdmissionPolicy tests
go test -v ./pkg/operator/operators/clustermanager/controllers/clustermanagercontroller/... \
  -run TestAdmissionPolicy
```

### Run E2E Tests

```bash
# Setup test environment
export KUBECONFIG=/path/to/hub/kubeconfig

# Enable AdmissionPolicy feature gate
kubectl patch clustermanager cluster-manager --type=merge -p '
spec:
  workConfiguration:
    featureGates:
    - feature: AdmissionPolicy
      mode: Enable
  registrationConfiguration:
    featureGates:
    - feature: AdmissionPolicy
      mode: Enable
'

# Wait for policies to be deployed
kubectl wait --for=condition=available --timeout=60s \
  validatingadmissionpolicy/manifestworks.admission.work.open-cluster-management.io

# Run E2E tests with admission-policy label
ginkgo -v --label-filter="admission-policy" test/e2e/
```

## Known Limitations

### 1. MutatingAdmissionPolicy API Not in Client-Go

**Issue**: MutatingAdmissionPolicy API is not yet available in the standard `k8s.io/api` and `k8s.io/client-go` packages (still beta in K8s 1.31).

**Impact**:
- Cannot verify MutatingAdmissionPolicy creation in integration tests
- Cannot query MutatingAdmissionPolicy resources via standard client in E2E tests

**Workaround**:
- Integration tests skip MutatingAdmissionPolicy verification (commented out with explanation)
- E2E tests verify mutations work functionally (taint timeAdded auto-population)
- Direct kubectl commands can be used to verify MAP resources:
  ```bash
  kubectl get mutatingadmissionpolicies
  ```

**Future**: Once MAP graduates to GA and is included in client-go, uncomment the verification code.

### 2. Test Requires Feature Gate in API Repository

**Issue**: Tests depend on `ocmfeature.AdmissionPolicy` constant which must be defined in `open-cluster-management.io/api` repository.

**Status**: ⏳ Pending addition to API repository

**Current Workaround**: Tests are complete but will fail to compile until feature gate is added.

## Testing Checklist

Use this checklist when validating the AdmissionPolicy feature:

- [ ] **Integration Tests**
  - [ ] AP disabled: Webhooks deployed, policies not deployed
  - [ ] AP enabled: Policies deployed, webhooks not deployed
  - [ ] Toggle from disabled to enabled: Clean transition
  - [ ] Toggle from enabled to disabled: Restore webhooks
  - [ ] Resource cleanup works correctly

- [ ] **E2E Tests - ManifestWork**
  - [ ] Empty manifests rejected
  - [ ] Missing manifest name rejected
  - [ ] Duplicate manifests rejected
  - [ ] Size limit (500KB) enforced
  - [ ] Executor RBAC validated
  - [ ] Valid ManifestWork accepted

- [ ] **E2E Tests - ManagedCluster Validation**
  - [ ] Invalid name format rejected
  - [ ] Non-HTTPS URL rejected
  - [ ] Valid cluster accepted
  - [ ] HubAcceptsClient RBAC validated (manual)
  - [ ] ClusterSet label RBAC validated (manual)

- [ ] **E2E Tests - ManagedCluster Mutation**
  - [ ] Taint timeAdded auto-populated on create
  - [ ] Taint timeAdded preserved on update
  - [ ] New taints get fresh timeAdded
  - [ ] Default clusterset label added (manual, if feature enabled)

- [ ] **Deployment Verification**
  - [ ] No webhook deployments when AP enabled
  - [ ] No webhook services when AP enabled
  - [ ] No ValidatingWebhookConfiguration when AP enabled
  - [ ] No MutatingWebhookConfiguration when AP enabled
  - [ ] ValidatingAdmissionPolicy resources exist
  - [ ] MutatingAdmissionPolicy resources exist
  - [ ] PolicyBindings exist and are active

- [ ] **Performance**
  - [ ] Validation latency < 5ms (vs 10-50ms for webhooks)
  - [ ] Large ManifestWork (50 manifests) validates successfully
  - [ ] No webhook pod resource usage

## Troubleshooting Tests

### Integration Tests Fail to Compile

**Error**: `undefined: ocmfeature.AdmissionPolicy`

**Solution**: Feature gate must be added to `open-cluster-management.io/api` repository:
```bash
cd $GOPATH/src/open-cluster-management.io/api
# Add feature gate to feature/feature.go
# Run make vendor in ocm repository
cd $GOPATH/src/open-cluster-management.io/ocm
make vendor
```

### E2E Tests Skip All Cases

**Symptom**: All tests report "AdmissionPolicy feature gate is not enabled"

**Solution**:
1. Verify feature gate is enabled in ClusterManager CR
2. Check ValidatingAdmissionPolicy resources are deployed:
   ```bash
   kubectl get validatingadmissionpolicies
   ```
3. Check cluster manager controller logs:
   ```bash
   kubectl logs -n open-cluster-management-hub deployment/cluster-manager
   ```

### Validation Tests Pass but Should Fail

**Symptom**: Invalid ManifestWork is accepted

**Possible Causes**:
1. ValidatingAdmissionPolicy not active
2. PolicyBinding not configured
3. CEL expression error

**Debug**:
```bash
# Check policy status
kubectl get validatingadmissionpolicy manifestworks.admission.work.open-cluster-management.io -o yaml

# Check binding
kubectl get validatingadmissionpolicybinding -o yaml

# Check for CEL errors in events
kubectl get events -A | grep -i validation
```

### Mutation Tests Fail

**Symptom**: Taint timeAdded not auto-populated

**Possible Causes**:
1. MutatingAdmissionPolicy not deployed
2. PolicyBinding not configured
3. CEL expression error in MAP

**Debug**:
```bash
# Check MAP exists (may need kubectl 1.31+)
kubectl get mutatingadmissionpolicies

# Check MAP binding
kubectl get mutatingadmissionpolicybinding

# Check API server supports MAP
kubectl api-resources | grep mutatingadmissionpolicies
```

## Future Test Enhancements

### Planned Additions

1. **Performance Benchmarks**
   - Measure validation latency (VAP vs webhook)
   - Test with large ManifestWork lists (100+ manifests)
   - Concurrent request handling

2. **RBAC Test Expansion**
   - HubAcceptsClient accept permission
   - ClusterSet join permission
   - ClusterSetBinding bind permission
   - Comprehensive impersonation scenarios

3. **Error Message Validation**
   - Verify user-friendly error messages from VAP
   - Test CEL expression failure messages
   - Validation failure details

4. **Upgrade/Downgrade Tests**
   - Test upgrade from webhook mode to AP mode
   - Test downgrade from AP mode to webhook mode
   - Multi-version compatibility

5. **Stress Tests**
   - Rapid create/delete cycles
   - Large-scale concurrent operations
   - Resource limit boundary testing

### Test Automation

**CI/CD Integration**:
- Add AdmissionPolicy tests to GitHub Actions
- Run tests on K8s 1.31, 1.32, 1.33+
- Nightly E2E test runs
- Performance regression detection

**Test Matrix**:
```yaml
kubernetes_versions:
  - 1.31  # MAP Beta
  - 1.32  # Future
  - 1.33  # Future (MAP GA expected)

feature_combinations:
  - AdmissionPolicy: enabled, ManifestWorkReplicaSet: enabled
  - AdmissionPolicy: enabled, ManifestWorkReplicaSet: disabled
  - AdmissionPolicy: disabled  # Baseline webhook behavior
```

## Summary

The AdmissionPolicy feature has comprehensive test coverage across:
- ✅ Integration tests for controller behavior
- ✅ E2E tests for validation rules
- ✅ E2E tests for mutation rules
- ✅ Feature gate toggle behavior
- ✅ Resource cleanup verification
- ⏳ Performance benchmarks (planned)
- ⏳ Extended RBAC tests (planned)

**Test Status**: Ready to run once `AdmissionPolicy` feature gate is added to API repository.

**Next Steps**:
1. Add feature gate to `open-cluster-management.io/api`
2. Run `make vendor` to update dependencies
3. Execute integration tests: `make test-integration`
4. Deploy to K8s 1.31+ cluster
5. Enable feature gate in ClusterManager
6. Execute E2E tests: `ginkgo --label-filter="admission-policy" test/e2e/`
7. Verify all tests pass ✅
