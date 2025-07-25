# Migration Backup Controller Test Examples

This directory contains comprehensive test examples for the Migration Backup Controller, which watches `StatefulMigration` CRDs and automatically manages `CheckpointBackup` resources with Karmada propagation.

## 📁 File Overview

| File | Description |
|------|-------------|
| `test-migration-backup.yaml` | Main test scenarios covering StatefulSet, Deployment, Pod, and multi-cluster cases |
| `edge-cases.yaml` | Edge case scenarios to test error handling and boundary conditions |
| `test-script.sh` | Interactive test script with monitoring and validation functions |
| `README.md` | This documentation file |

## 🚀 Quick Start

### Prerequisites

1. **Install CRDs** (if not already done):
   ```bash
   make install
   ```

2. **Deploy the controller**:
   ```bash
   make deploy
   ```

3. **Verify controller is running**:
   ```bash
   kubectl get pods -n stateful-migration-operator-system
   ```

### Running Tests

#### Option 1: Interactive Test Script
```bash
# Make script executable
chmod +x examples/test-script.sh

# Run interactive menu
./examples/test-script.sh

# Or run specific commands
./examples/test-script.sh full-test    # Run complete test suite
./examples/test-script.sh apply        # Apply test resources
./examples/test-script.sh monitor      # Monitor resources
./examples/test-script.sh logs         # Show controller logs
./examples/test-script.sh cleanup      # Clean up test resources
```

#### Option 2: Manual Testing
```bash
# Apply main test scenarios
kubectl apply -f examples/test-migration-backup.yaml

# Monitor StatefulMigrations
kubectl get statefulmigrations -n stateful-migration-test -w

# Monitor CheckpointBackups (should be created automatically)
kubectl get checkpointbackups -n stateful-migration-test -w

# Check if labels were added to target resources
kubectl get statefulset my-app -n stateful-migration-test -o jsonpath='{.metadata.labels}'

# Apply edge cases
kubectl apply -f examples/edge-cases.yaml

# Cleanup
kubectl delete -f examples/test-migration-backup.yaml
kubectl delete -f examples/edge-cases.yaml
```

## 📋 Test Scenarios

### Main Test Scenarios (`test-migration-backup.yaml`)

#### 1. **StatefulSet Migration**
- **Resource**: 3-replica StatefulSet with persistent storage
- **Containers**: nginx + busybox sidecar
- **Clusters**: cluster-1, cluster-2
- **Schedule**: Every 15 minutes
- **Expected**: 6 CheckpointBackup resources (3 pods × 2 clusters)

#### 2. **Deployment Migration**
- **Resource**: 2-replica Deployment
- **Containers**: httpd + fluentd sidecar
- **Clusters**: cluster-1
- **Schedule**: Every 2 hours
- **Expected**: 2 CheckpointBackup resources (2 pods × 1 cluster)

#### 3. **Standalone Pod Migration**
- **Resource**: Single Pod
- **Containers**: alpine + busybox
- **Clusters**: cluster-3
- **Schedule**: Every 30 minutes
- **Expected**: 1 CheckpointBackup resource

#### 4. **Multi-Cluster Database**
- **Resource**: PostgreSQL StatefulSet
- **Containers**: postgres with persistent storage
- **Clusters**: cluster-east, cluster-west, cluster-central
- **Schedule**: Every 6 hours
- **Expected**: 3 CheckpointBackup resources (1 pod × 3 clusters)

### Edge Case Scenarios (`edge-cases.yaml`)

#### 1. **No Registry Secret**
- Tests optional `secretRef` field
- Should work with public registries

#### 2. **Single Source Cluster**
- Tests with only one source cluster
- Validates minimal configuration

#### 3. **Zero Replicas**
- Tests StatefulSet with 0 replicas
- Should create no CheckpointBackup resources

#### 4. **Init Containers**
- Tests pod with init containers
- Should extract only main containers

#### 5. **Long Cluster Names**
- Tests with very long cluster names
- Validates resource name generation

#### 6. **Many Replicas**
- Tests with 5-replica Deployment
- Validates scaling behavior

#### 7. **Non-existent Resource**
- Tests reference to missing resource
- Should handle gracefully with appropriate errors

## 🔍 What to Observe

### Expected Controller Behavior

1. **Label Addition**: Target resources should get the label `checkpoint-migration.dcn.io: "true"`

2. **CheckpointBackup Creation**: For each pod and source cluster combination, a CheckpointBackup should be created with:
   - Name format: `{statefulmigration-name}-{pod-name}-{cluster-name}`
   - Owner reference to the StatefulMigration
   - Populated container information

3. **Karmada PropagationPolicy**: Each CheckpointBackup should have an associated PropagationPolicy to distribute it to the target cluster

4. **Lifecycle Management**: 
   - Adding pods should create new CheckpointBackups
   - Removing pods should delete corresponding CheckpointBackups
   - Deleting StatefulMigration should clean up all resources

### Monitoring Commands

```bash
# Watch StatefulMigrations
kubectl get statefulmigrations -A -w

# Watch CheckpointBackups being created
kubectl get checkpointbackups -A -w

# Watch PropagationPolicies (Karmada)
kubectl get propagationpolicies -A

# Check controller logs
kubectl logs -n stateful-migration-operator-system -l control-plane=controller-manager -f

# Check labels on target resources
kubectl get statefulset,deployment,pod -A --show-labels | grep checkpoint-migration
```

## 🧪 Testing Different Scenarios

### Test Pod Scaling
```bash
# Scale up StatefulSet
kubectl scale statefulset my-app -n stateful-migration-test --replicas=5

# Watch for new CheckpointBackups
kubectl get checkpointbackups -n stateful-migration-test -w

# Scale down
kubectl scale statefulset my-app -n stateful-migration-test --replicas=1

# Watch for CheckpointBackup cleanup
```

### Test Resource Deletion
```bash
# Delete a target resource
kubectl delete deployment web-app -n stateful-migration-test

# Observe controller handling missing resource
kubectl describe statefulmigration migrate-web-app -n stateful-migration-test
```

### Test Controller Restart
```bash
# Restart controller
kubectl rollout restart deployment controller-manager -n stateful-migration-operator-system

# Verify state consistency after restart
kubectl get checkpointbackups -n stateful-migration-test
```

## 🐛 Troubleshooting

### Common Issues

1. **No CheckpointBackups Created**
   - Check if target pods are running
   - Verify controller is running and has no errors
   - Check RBAC permissions

2. **Labels Not Added**
   - Verify controller has update permissions for target resources
   - Check controller logs for errors

3. **PropagationPolicies Not Created**
   - Ensure Karmada CRDs are installed
   - Verify controller has permissions for PropagationPolicy resources

### Debug Commands

```bash
# Check controller status
kubectl get deployment controller-manager -n stateful-migration-operator-system

# Check controller logs with different levels
kubectl logs -n stateful-migration-operator-system -l control-plane=controller-manager --tail=100

# Describe StatefulMigration for events
kubectl describe statefulmigration -n stateful-migration-test

# Check RBAC
kubectl auth can-i create checkpointbackups --as=system:serviceaccount:stateful-migration-operator-system:controller-manager

# Validate CRDs
kubectl get crd | grep migration.dcnlab.com
```

## 📊 Expected Results Summary

| Test Case | StatefulMigrations | CheckpointBackups | PropagationPolicies | Labels Added |
|-----------|-------------------|-------------------|-------------------|--------------|
| Main scenarios | 4 | 12 (3+2+1+3+3) | 12 | 4 resources |
| Edge cases | 7 | 11 (varies by case) | 11 | 5 resources |
| **Total** | **11** | **23** | **23** | **9 resources** |

## 🎯 Success Criteria

✅ **All StatefulMigrations are created successfully**
✅ **CheckpointBackups are created for each pod-cluster combination**
✅ **PropagationPolicies are created for each CheckpointBackup**
✅ **Target resources have the migration label**
✅ **Controller handles edge cases gracefully**
✅ **Resource cleanup works when StatefulMigrations are deleted**
✅ **Pod scaling triggers appropriate CheckpointBackup updates**

## 📝 Notes

- The test uses mock cluster names since this is testing the control plane logic
- In a real Karmada environment, ensure the specified clusters exist
- Registry credentials in the examples are base64-encoded dummy credentials
- Some edge cases are designed to test error handling and should show appropriate error messages in logs 