# Deployment Guide for Stateful Migration Operator

This guide explains how to deploy both CheckpointBackup and MigrationBackup controllers using the automated `deploy.sh` script.

## Overview

The deployment script (`deploy.sh`) handles:
- **CheckpointBackup Controller**: Deployed as DaemonSet to member clusters via Karmada PropagationPolicies
- **MigrationBackup Controller**: Deployed to management/control plane cluster
- **Automatic RBAC**: Creates necessary service accounts, roles, and bindings
- **Namespace Management**: Creates and propagates namespaces
- **CRD Propagation**: Ensures CRDs are available on member clusters

## Prerequisites

### 1. System Requirements
- `kubectl` installed and configured
- Access to Karmada control plane
- Access to management cluster
- Docker images built and pushed to registry

### 2. Karmada Setup
- Karmada control plane running and accessible
- Member clusters registered with Karmada
- Kubeconfig file for Karmada control plane

### 3. Management Cluster
- Kubernetes cluster for running MigrationBackup controller
- Kubeconfig file for management cluster

### 4. Registry Credentials (for CheckpointBackup)
- Container registry access
- Registry credentials secret configured

## Usage

### Basic Syntax
```bash
./deploy.sh [options]
```

### Options
| Option | Description | Required |
|--------|-------------|----------|
| `-c, --checkpoint` | Deploy CheckpointBackup controller | Choice |
| `-m, --migration` | Deploy MigrationBackup controller | Choice |
| `-a, --all` | Deploy all controllers | Choice |
| `-v, --version VERSION` | Version tag for images (default: v1.16) | No |
| `-k, --karmada-config PATH` | Path to Karmada kubeconfig | For checkpoint |
| `-g, --mgmt-config PATH` | Path to management cluster kubeconfig | For migration |
| `-l, --clusters LIST` | Comma-separated member cluster names | For checkpoint |
| `-d, --dry-run` | Show what would be deployed | No |
| `-h, --help` | Show help message | No |

## Deployment Scenarios

### 1. Deploy All Controllers
```bash
./deploy.sh --all \
  --karmada-config ~/.kube/karmada \
  --mgmt-config ~/.kube/config \
  --clusters cluster1,cluster2,cluster3 \
  --version v1.17
```

### 2. Deploy Only CheckpointBackup Controller
```bash
./deploy.sh --checkpoint \
  --karmada-config ~/.kube/karmada \
  --clusters cluster1,cluster2 \
  --version v1.17
```

### 3. Deploy Only MigrationBackup Controller
```bash
./deploy.sh --migration \
  --mgmt-config ~/.kube/config \
  --version v1.17
```

### 4. Dry Run (Preview Changes)
```bash
./deploy.sh --all \
  --karmada-config ~/.kube/karmada \
  --mgmt-config ~/.kube/config \
  --clusters cluster1,cluster2 \
  --dry-run
```

## What Gets Deployed

### CheckpointBackup Controller (Member Clusters)

#### 🏗️ **Resources Created on Karmada**
1. **Namespace**: `stateful-migration`
2. **CRD**: `checkpointbackups.migration.dcnlab.com`
3. **RBAC**: Service account, ClusterRole, ClusterRoleBinding
4. **DaemonSet**: CheckpointBackup controller with buildah
5. **PropagationPolicies**: For namespace, CRD, RBAC, DaemonSet

#### 📦 **Container Image**
- `lehuannhatrang/stateful-migration-operator:checkpointBackup_<VERSION>`
- Includes buildah and container tools
- Size: ~120MB

#### 🔧 **Configuration**
- Privileged container with `SYS_ADMIN`, `SYS_PTRACE` capabilities
- Host network and PID access
- Volume mounts for kubelet checkpoints and buildah storage

### MigrationBackup Controller (Management Cluster)

#### 🏗️ **Resources Created**
1. **Namespace**: `stateful-migration`
2. **CRDs**: All migration-related CRDs
3. **RBAC**: Service account and permissions (follows deploy/multi-cluster-karmada/all-in-one.yaml pattern)
4. **Deployment**: MigrationBackup controller
5. **Service**: Metrics and health endpoints

#### 📦 **Container Image**
- `lehuannhatrang/stateful-migration-operator:migrationBackup_<VERSION>`
- Minimal distroless image
- Size: ~15MB

#### 🔧 **Configuration**
- Non-privileged container
- Leader election enabled
- Metrics and health endpoints

## Post-Deployment Steps

### 1. Verify CheckpointBackup Controller
```bash
# Check PropagationPolicies
kubectl --kubeconfig ~/.kube/karmada get propagationpolicy -n stateful-migration

# Check DaemonSet on member clusters
kubectl get daemonset checkpoint-backup-controller -n stateful-migration

# Check pods on member clusters
kubectl get pods -n stateful-migration -l app.kubernetes.io/name=checkpoint-backup-controller
```

### 2. Registry Credentials (Automatic)
The deployment script automatically prompts for and configures registry credentials:

```bash
# Registry credentials are configured automatically during deployment
# The script will prompt for:
#   - Registry username
#   - Registry password  
#   - Registry URL (optional, defaults to Docker Hub)

# Verify registry credentials were created and propagated
kubectl --kubeconfig ~/.kube/karmada get secret registry-credentials -n stateful-migration
kubectl get secret registry-credentials -n stateful-migration  # On member clusters
```

**Manual Registry Configuration (if needed):**
```bash
# Only if you need to update credentials manually
kubectl --kubeconfig ~/.kube/karmada apply -f config/checkpoint-backup/registry-credentials-secret.yaml

# Create PropagationPolicy manually  
kubectl --kubeconfig ~/.kube/karmada apply -f - <<EOF
apiVersion: policy.karmada.io/v1alpha1
kind: PropagationPolicy
metadata:
  name: registry-credentials-propagation
  namespace: stateful-migration
spec:
  resourceSelectors:
  - apiVersion: v1
    kind: Secret
    name: registry-credentials
  placement:
    clusterAffinity:
      clusterNames:
      - cluster1
      - cluster2
EOF
```

### 3. Verify MigrationBackup Controller
```bash
# Check deployment
kubectl --kubeconfig ~/.kube/config get deployment migration-backup-controller -n stateful-migration

# Check pods  
kubectl --kubeconfig ~/.kube/config get pods -n stateful-migration -l app.kubernetes.io/name=migration-backup-controller

# Check logs
kubectl --kubeconfig ~/.kube/config logs -n stateful-migration deployment/migration-backup-controller -f

# Check service
kubectl --kubeconfig ~/.kube/config get svc -n stateful-migration migration-backup-controller-metrics
```

### 4. Test the Setup
```bash
# Create a test StatefulMigration resource
kubectl --kubeconfig ~/.kube/config apply -f - <<EOF
apiVersion: migration.dcnlab.com/v1
kind: StatefulMigration
metadata:
  name: test-migration
  namespace: default
spec:
  resourceRef:
    kind: StatefulSet
    name: my-statefulset
    namespace: default
  schedule: "0 2 * * *"
  sourceClusters:
  - cluster1
  registry:
    server: "your-registry.com"
    repository: "your-repo/checkpoints"
EOF
```

## Troubleshooting

### Common Issues

#### 1. **PropagationPolicy Not Working**
```bash
# Check PropagationPolicy status
kubectl --kubeconfig ~/.kube/karmada get propagationpolicy -n stateful-migration -o wide

# Check cluster registration
kubectl --kubeconfig ~/.kube/karmada get clusters
```

#### 2. **DaemonSet Not Scheduling**
```bash
# Check node selectors and tolerations
kubectl describe daemonset checkpoint-backup-controller -n stateful-migration

# Check node conditions
kubectl get nodes -o wide
```

#### 3. **Controller Not Starting**
```bash
# Check pod logs
kubectl logs -n stateful-migration -l app.kubernetes.io/name=checkpoint-backup-controller

# Check events
kubectl get events -n stateful-migration --sort-by='.lastTimestamp'
```

#### 4. **Registry Issues**
```bash
# Test registry connectivity from pod
kubectl exec -n stateful-migration <pod-name> -- buildah login your-registry.com

# Check secret propagation
kubectl get secret registry-credentials -n stateful-migration
```

#### 5. **RBAC Permission Issues**
```bash
# Check if RBAC resources are propagated to member clusters
kubectl --kubeconfig ~/.kube/karmada get clusterpropagationpolicy checkpoint-backup-cluster-rbac

# Verify ClusterRole exists on member cluster
kubectl get clusterrole checkpoint-backup-role

# Verify ClusterRoleBinding exists on member cluster  
kubectl get clusterrolebinding checkpoint-backup-rolebinding

# Verify ServiceAccount exists on member cluster
kubectl get serviceaccount checkpoint-backup-sa -n stateful-migration

# Check if controller can access CheckpointBackup CRD
kubectl auth can-i list checkpointbackups.migration.dcnlab.com --as=system:serviceaccount:stateful-migration:checkpoint-backup-sa

# Check if controller can access kubelet checkpoint API
kubectl auth can-i create nodes/checkpoint --as=system:serviceaccount:stateful-migration:checkpoint-backup-sa

# If RBAC is missing, manually apply and propagate
kubectl --kubeconfig ~/.kube/karmada apply -f config/rbac/checkpoint_backup_rbac.yaml
```

#### 6. **Kubelet Checkpoint API Issues**
```bash
# Common error: "kubelet checkpoint API returned status 403: Forbidden"
# This indicates missing permissions for nodes/checkpoint

# Check if controller has node checkpoint permissions
kubectl auth can-i create nodes/checkpoint --as=system:serviceaccount:stateful-migration:checkpoint-backup-sa
kubectl auth can-i get nodes --as=system:serviceaccount:stateful-migration:checkpoint-backup-sa

# Test kubelet checkpoint API directly from controller pod
kubectl exec -n stateful-migration <checkpoint-backup-pod> -- curl -k -X POST \
  -H "Authorization: Bearer $(kubectl exec -n stateful-migration <checkpoint-backup-pod> -- cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
  https://localhost:10250/checkpoint/test-namespace/test-pod/test-container

# Check if kubelet checkpoint feature is enabled on nodes
kubectl get nodes -o jsonpath='{.items[*].status.features.checkpointContainer}'
```

#### 7. **Checkpoint API Response Format Issues**
```bash
# Common error: "json: cannot unmarshal string into Go struct field CheckpointResponse.items"
# This indicates the kubelet checkpoint API returns a different format than expected

# Check controller logs for DEBUG messages showing actual API responses
kubectl logs -n stateful-migration -l app.kubernetes.io/name=checkpoint-backup-controller | grep "DEBUG:"

# Check what checkpoint files are actually created
kubectl exec -n stateful-migration <checkpoint-backup-pod> -- ls -la /var/lib/kubelet/checkpoints/

# Test the actual kubelet response format
kubectl exec -n stateful-migration <checkpoint-backup-pod> -- curl -v -k -X POST \
  -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
  "https://localhost:10250/checkpoint/test-namespace/test-pod/test-container?timeout=60"

# The controller now includes fallback handling for different response formats
# Check KUBELET_CHECKPOINT_API_DEBUG.md for detailed troubleshooting
```

### Debug Commands

```bash
# Check buildah functionality
kubectl exec -n stateful-migration <pod-name> -- buildah version

# Check storage configuration
kubectl exec -n stateful-migration <pod-name> -- buildah info

# Check kubelet checkpoint API
kubectl exec -n stateful-migration <pod-name> -- curl -k https://localhost:10250/healthz
```

## Uninstallation

### Remove CheckpointBackup Controller
```bash
# Delete PropagationPolicies
kubectl --kubeconfig ~/.kube/karmada delete propagationpolicy -n stateful-migration --all
kubectl --kubeconfig ~/.kube/karmada delete clusterpropagationpolicy checkpoint-backup-cluster-rbac

# Delete DaemonSet
kubectl --kubeconfig ~/.kube/karmada delete daemonset checkpoint-backup-controller -n stateful-migration

# Delete RBAC from Karmada
kubectl --kubeconfig ~/.kube/karmada delete -f config/rbac/checkpoint_backup_rbac.yaml

# Delete namespace
kubectl --kubeconfig ~/.kube/karmada delete namespace stateful-migration
```

### Remove MigrationBackup Controller
```bash
# Delete all resources from all-in-one manifest (recommended)
kubectl --kubeconfig ~/.kube/config delete -f deploy/multi-cluster-karmada/all-in-one.yaml

# Or delete individual components:
# Delete deployment and service
kubectl --kubeconfig ~/.kube/config delete deployment migration-backup-controller -n stateful-migration
kubectl --kubeconfig ~/.kube/config delete svc migration-backup-controller-metrics -n stateful-migration

# Delete RBAC
kubectl --kubeconfig ~/.kube/config delete clusterrole migration-backup-controller-role
kubectl --kubeconfig ~/.kube/config delete clusterrolebinding migration-backup-controller-rolebinding
kubectl --kubeconfig ~/.kube/config delete role migration-backup-leader-election-role -n stateful-migration
kubectl --kubeconfig ~/.kube/config delete rolebinding migration-backup-leader-election-rolebinding -n stateful-migration
kubectl --kubeconfig ~/.kube/config delete serviceaccount migration-backup-controller -n stateful-migration

# Delete CRDs (only if not used by other controllers)
kubectl --kubeconfig ~/.kube/config delete -f config/crd/bases/

# Delete namespace (only if not used by CheckpointBackup controllers)
kubectl --kubeconfig ~/.kube/config delete namespace stateful-migration
```

## Advanced Configuration

### Custom Namespaces
Edit the script variables:
```bash
NAMESPACE="custom-migration-namespace"
OPERATOR_NAMESPACE="custom-operator-namespace"
```

### Custom Image Registry
Edit the script variables:
```bash
DOCKERHUB_USERNAME="your-registry.com/your-org"
REPOSITORY_NAME="custom-operator"
```

### Additional Member Clusters
Add clusters to the list:
```bash
./deploy.sh --checkpoint \
  --clusters cluster1,cluster2,cluster3,cluster4 \
  --karmada-config ~/.kube/karmada
```

## Performance Considerations

### Resource Requirements
- **CheckpointBackup Controller**: 100m CPU, 128Mi memory (requests), 500m CPU, 512Mi memory (limits)
- **MigrationBackup Controller**: 10m CPU, 64Mi memory (requests), 500m CPU, 128Mi memory (limits)

### Storage Requirements
- **Kubelet checkpoints**: 50MB-500MB per container
- **Buildah storage**: 1GB-10GB per node
- **Registry bandwidth**: Consider checkpoint image sizes

### Scaling
- CheckpointBackup controllers scale with cluster nodes (DaemonSet)
- MigrationBackup controller typically runs as single replica with leader election

This deployment script provides a complete, production-ready solution for deploying the Stateful Migration Operator across your Karmada-managed clusters! 🚀 