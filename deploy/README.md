# Migration Backup Controller Deployment

This directory contains deployment manifests for the Migration Backup Controller to be deployed on your **management cluster** in the `stateful-migration` namespace.

## 📁 File Overview

| File | Description |
|------|-------------|
| `namespace.yaml` | Namespace definition for `stateful-migration` |
| `rbac.yaml` | Complete RBAC configuration (ServiceAccount, ClusterRole, etc.) |
| `deployment.yaml` | Controller deployment manifest |
| `service.yaml` | Service for metrics and health endpoints |
| `all-in-one.yaml` | Combined manifest with all resources |
| `deploy.sh` | Automated deployment script |
| `README.md` | This documentation |

## 🚀 Quick Deployment

### **Option 1: Automated Script (Recommended)**

```bash
# Navigate to deploy directory
cd deploy

# Deploy with your Docker image
./deploy.sh YOUR_DOCKERHUB_USERNAME/stateful-migration-operator:latest

# With custom kubeconfig for mgmt-cluster
./deploy.sh YOUR_DOCKERHUB_USERNAME/stateful-migration-operator:latest ~/.kube/mgmt-cluster-config
```

### **Option 2: Manual Deployment**

```bash
# 1. First, install CRDs (from project root)
make install

# 2. Edit the image in all-in-one.yaml
sed -i 's|YOUR_DOCKERHUB_USERNAME/stateful-migration-operator:latest|yourusername/stateful-migration-operator:latest|g' deploy/all-in-one.yaml

# 3. Apply to mgmt-cluster
kubectl apply -f deploy/all-in-one.yaml

# 4. Verify deployment
kubectl get pods -n stateful-migration
```

### **Option 3: Individual Files**

```bash
# Apply in order
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/deployment.yaml  # Edit image first!
kubectl apply -f deploy/service.yaml
```

## 🔧 Configuration

### **Required: Update Docker Image**

Before deploying, you **must** update the Docker image reference in the deployment manifests:

1. **In `deployment.yaml`** or **`all-in-one.yaml`**:
   ```yaml
   # Replace this line
   image: YOUR_DOCKERHUB_USERNAME/stateful-migration-operator:latest
   
   # With your actual image
   image: myusername/stateful-migration-operator:latest
   ```

2. **Or use the script** which does this automatically:
   ```bash
   ./deploy.sh myusername/stateful-migration-operator:latest
   ```

### **Optional: Customize Resources**

You can modify resource limits in `deployment.yaml`:

```yaml
resources:
  limits:
    cpu: 500m      # Adjust based on your needs
    memory: 256Mi  # Adjust based on your needs
  requests:
    cpu: 100m
    memory: 128Mi
```

## 🎯 Deployment Target

This deployment is specifically designed for:
- **Target**: Management cluster (Karmada control plane)
- **Namespace**: `stateful-migration`
- **Component**: Migration Backup Controller
- **Function**: Watches `StatefulMigration` CRDs and manages `CheckpointBackup` resources

## 📋 Prerequisites

1. **CRDs Installed**: StatefulMigration and CheckpointBackup CRDs must be installed
2. **Karmada**: Karmada CRDs (PropagationPolicy) must be available
3. **Docker Image**: Your controller image must be pushed to Docker Hub
4. **RBAC**: Cluster admin access to install ClusterRole and ClusterRoleBinding

## 🔍 Verification

After deployment, verify everything is working:

```bash
# Check deployment status
kubectl get deployment migration-backup-controller -n stateful-migration

# Check pod logs
kubectl logs -n stateful-migration deployment/migration-backup-controller -f

# Check service
kubectl get svc -n stateful-migration migration-backup-controller-metrics

# Test with a StatefulMigration
kubectl apply -f ../examples/validate-crds.yaml
kubectl get checkpointbackups -n crd-validation-test  # Should appear automatically
```

## 🛠️ RBAC Permissions

The controller has the following permissions:

### **Cluster-wide permissions:**
- **StatefulMigration**: Full CRUD access
- **CheckpointBackup**: Full CRUD access  
- **PropagationPolicy**: Full CRUD access (Karmada)
- **StatefulSets/Deployments**: Read and update (for labels)
- **Pods**: Read access
- **Events**: Create for logging

### **Namespace permissions (stateful-migration):**
- **ConfigMaps/Leases**: For leader election
- **Events**: For logging

## 📊 Monitoring

The controller exposes metrics on port 8080:

```bash
# Port forward to access metrics
kubectl port-forward -n stateful-migration svc/migration-backup-controller-metrics 8080:8080

# Access metrics
curl http://localhost:8080/metrics

# Health check
curl http://localhost:8081/healthz
```

## 🐛 Troubleshooting

### **Common Issues:**

1. **CRDs not found**:
   ```bash
   # Install CRDs from project root
   make install
   ```

2. **Image pull errors**:
   ```bash
   # Verify image exists
   docker pull yourusername/stateful-migration-operator:latest
   
   # Check deployment
   kubectl describe deployment migration-backup-controller -n stateful-migration
   ```

3. **RBAC errors**:
   ```bash
   # Check if you have cluster admin permissions
   kubectl auth can-i create clusterroles
   
   # Check service account
   kubectl get serviceaccount migration-backup-controller -n stateful-migration
   ```

4. **Controller not starting**:
   ```bash
   # Check logs
   kubectl logs -n stateful-migration deployment/migration-backup-controller
   
   # Check events
   kubectl get events -n stateful-migration --sort-by='.lastTimestamp'
   ```

### **Debug Commands:**

```bash
# Check all resources
kubectl get all -n stateful-migration

# Describe deployment
kubectl describe deployment migration-backup-controller -n stateful-migration

# Check RBAC
kubectl auth can-i create statefulmigrations --as=system:serviceaccount:stateful-migration:migration-backup-controller

# Test controller
kubectl apply -f ../examples/validate-crds.yaml
kubectl get checkpointbackups -A
```

## 📝 Cleanup

To remove the controller:

```bash
# Remove all resources
kubectl delete -f deploy/all-in-one.yaml

# Or remove namespace (removes everything)
kubectl delete namespace stateful-migration

# Clean up cluster resources
kubectl delete clusterrole migration-backup-controller-role
kubectl delete clusterrolebinding migration-backup-controller-rolebinding
```

## 🎯 Expected Result

After successful deployment:

```bash
$ kubectl get pods -n stateful-migration
NAME                                          READY   STATUS    RESTARTS   AGE
migration-backup-controller-7b8f9d5c4-x7h2m   1/1     Running   0          2m

$ kubectl get svc -n stateful-migration
NAME                                     TYPE        CLUSTER-IP       PORT(S)           AGE
migration-backup-controller-metrics      ClusterIP   10.96.45.123     8080/TCP,8081/TCP 2m
```

The controller is now ready to watch for `StatefulMigration` resources and automatically create `CheckpointBackup` resources with Karmada propagation! 