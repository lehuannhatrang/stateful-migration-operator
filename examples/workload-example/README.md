# inmem-go Workload Example with Karmada Propagation

This example demonstrates how to deploy a workload (`inmem-go` application) and propagate it to a specific cluster (`cluster-criu`) using Karmada PropagationPolicy.

## 📁 Files Overview

| File | Description |
|------|-------------|
| `inmem-go-workload.yaml` | Pod and Service definitions for the inmem-go application |
| `propagation-policy.yaml` | Karmada PropagationPolicy to propagate to cluster-criu |
| `cluster-propagation-policy.yaml` | Alternative ClusterPropagationPolicy (cluster-wide) |
| `statefulmigration.yaml` | StatefulMigration CR + Registry Secret for checkpoint backups |
| `complete-example.yaml` | All-in-one file with workload + propagation + StatefulMigration |
| `deploy-workload.sh` | Automated deployment script (workload + propagation only) |
| `deploy-complete.sh` ✅ | Complete deployment script (includes StatefulMigration) |
| `README.md` | This documentation |

## 🚀 Quick Deployment

### **Option 1: Complete StatefulMigration Workflow (Recommended)**

```bash
# Navigate to the example directory
cd examples/workload-example

# Complete deployment with StatefulMigration
./deploy-complete.sh ~/.kube/karmada-config ~/.kube/mgmt-config your-docker-username

# This deploys: workload + propagation + StatefulMigration CR
# The script will prompt for your Docker password securely
```

### **Option 2: Workload + Propagation Only**

```bash
# Deploy just workload and propagation (no StatefulMigration)
./deploy-workload.sh ~/.kube/karmada-apiserver-config

# Or use ClusterPropagationPolicy
./deploy-workload.sh ~/.kube/karmada-apiserver-config cluster
```

### **Option 3: Manual Deployment**

#### **Workload + Propagation Only**
```bash
# Set Karmada kubeconfig (if different from current context)
export KUBECONFIG=~/.kube/karmada-apiserver-config

# Deploy workload resources
kubectl apply -f inmem-go-workload.yaml

# Deploy propagation policy
kubectl apply -f propagation-policy.yaml

# Check propagation status
kubectl get propagationpolicy inmem-go-propagation -n inmem-go-app
kubectl get resourcebinding -A | grep inmem-go
```

#### **Complete Workflow (Workload + Propagation + StatefulMigration)**
```bash
# Set Karmada kubeconfig
export KUBECONFIG=~/.kube/karmada-apiserver-config

# Deploy everything at once
kubectl apply -f complete-example.yaml

# OR deploy step by step
kubectl apply -f inmem-go-workload.yaml
kubectl apply -f propagation-policy.yaml
kubectl apply -f statefulmigration.yaml  # includes registry secret

# Check StatefulMigration status
kubectl get statefulmigration inmem-go-migration -n inmem-go-app
```

## 📋 What Gets Deployed

### **1. Workload Resources**
- **Namespace**: `inmem-go-app`
- **Pod**: `inmem-go-app` running `lehuannhatrang/inmem-go-server:v1`
- **Service**: `inmem-go-service` exposing port 30180 (NodePort)

### **2. Karmada PropagationPolicy**
- **Target Cluster**: `cluster-criu`
- **Resources**: Propagates namespace, pod, and service
- **Policy Name**: `inmem-go-propagation`

### **3. StatefulMigration CR (Optional)**
- **Migration Name**: `inmem-go-migration`
- **Target Resource**: The `inmem-go-app` pod
- **Source Cluster**: `cluster-criu` (where pod runs after propagation)
- **Registry**: `docker.io/your-username/checkpoints`
- **Schedule**: Every 5 minutes (`*/5 * * * *`)
- **Function**: Enables checkpoint backup monitoring by migration controller

## 🔍 Verification Commands

### **Check Deployment Status**
```bash
# Check if cluster-criu is available
kubectl get clusters

# Check workload resources on Karmada control plane
kubectl get pods -n inmem-go-app
kubectl get services -n inmem-go-app

# Check propagation policy
kubectl describe propagationpolicy inmem-go-propagation -n inmem-go-app
```

### **Check Propagation Status**
```bash
# Check resource bindings (shows which resources are bound to which clusters)
kubectl get resourcebinding -A

# Check work objects (actual propagated resources)
kubectl get work -A

# Get detailed propagation status
kubectl describe resourcebinding -A | grep inmem-go
```

### **Verify on Target Cluster**
```bash
# Switch to cluster-criu context (if you have direct access)
kubectl config use-context cluster-criu

# Check if resources were propagated
kubectl get namespace inmem-go-app
kubectl get pods -n inmem-go-app
kubectl get services -n inmem-go-app

# Test the application
curl http://<cluster-criu-node-ip>:30180
```

## 📊 PropagationPolicy Explained

### **Resource Selectors**
```yaml
resourceSelectors:
  - apiVersion: v1
    kind: Namespace
    name: inmem-go-app
  - apiVersion: v1
    kind: Pod
    namespace: inmem-go-app
    name: inmem-go-app
  - apiVersion: v1
    kind: Service
    namespace: inmem-go-app
    name: inmem-go-service
```
Specifies exactly which resources to propagate.

### **Placement Configuration**
```yaml
placement:
  clusterAffinity:
    clusterNames:
      - cluster-criu
```
Targets the `cluster-criu` cluster specifically.

## 🔧 Customization Options

### **1. Target Different Clusters**
Edit the `clusterNames` in the PropagationPolicy:
```yaml
clusterAffinity:
  clusterNames:
    - cluster-criu
    - cluster-other
    - cluster-backup
```

### **2. Add Cluster-Specific Overrides**
Uncomment and modify the override section:
```yaml
overridePolicy: 
  - clusterName: cluster-criu
    overriders:
      plaintextOverriders:
        - path: "/spec/containers/0/env"
          operator: add
          value:
            - name: CLUSTER_NAME
              value: cluster-criu
```

### **3. Use Label-Based Selection**
Switch to `cluster-propagation-policy.yaml` for label-based resource selection:
```yaml
resourceSelectors:
  - apiVersion: v1
    kind: Pod
    labelSelector:
      matchLabels:
        app: inmem-go
```

## 🐛 Troubleshooting

### **Common Issues**

1. **Cluster not found**:
   ```bash
   # Check available clusters
   kubectl get clusters
   
   # Verify cluster-criu is joined to Karmada
   kubectl describe cluster cluster-criu
   ```

2. **Resources not propagating**:
   ```bash
   # Check PropagationPolicy status
   kubectl describe propagationpolicy inmem-go-propagation -n inmem-go-app
   
   # Check for events
   kubectl get events -n inmem-go-app --sort-by='.lastTimestamp'
   ```

3. **ResourceBinding issues**:
   ```bash
   # Check resource bindings
   kubectl get resourcebinding -A -o wide
   
   # Describe specific binding
   kubectl describe resourcebinding <binding-name> -n <namespace>
   ```

### **Debug Commands**

```bash
# Check Karmada scheduler status
kubectl get pods -n karmada-system | grep scheduler

# Check PropagationPolicy validation
kubectl get propagationpolicy inmem-go-propagation -n inmem-go-app -o yaml

# Check work status
kubectl get work -A -o wide

# Force re-evaluation (delete and recreate policy)
kubectl delete propagationpolicy inmem-go-propagation -n inmem-go-app
kubectl apply -f propagation-policy.yaml
```

## 📝 Expected Results

After successful deployment and propagation:

1. **On Karmada Control Plane**:
   ```bash
   $ kubectl get pods -n inmem-go-app
   NAME           READY   STATUS    RESTARTS   AGE
   inmem-go-app   1/1     Running   0          2m
   
   $ kubectl get resourcebinding -A
   NAMESPACE      NAME                            AGE
   inmem-go-app   inmem-go-app-pod-binding       2m
   inmem-go-app   inmem-go-service-binding       2m
   ```

2. **On cluster-criu**:
   ```bash
   $ kubectl get pods -n inmem-go-app
   NAME           READY   STATUS    RESTARTS   AGE
   inmem-go-app   1/1     Running   0          1m
   
   $ curl http://<node-ip>:30180
   Response from inmem-go-server
   ```

## 🎯 Application Details

- **Image**: `lehuannhatrang/inmem-go-server:v1`
- **Port**: 8080 (internal), 30180 (NodePort)
- **Health Endpoints**: `/health` (liveness), `/ready` (readiness)
- **Resource Limits**: 100m CPU, 128Mi memory
- **Resource Requests**: 50m CPU, 64Mi memory

The application will be accessible on `cluster-criu` at `http://<any-node-ip>:30180` once propagation is complete.

## 🔄 **StatefulMigration Workflow**

When you deploy the `StatefulMigration` CR, here's what happens:

1. **Migration Controller Detection**: The migration backup controller (deployed on mgmt-cluster) detects the new `StatefulMigration` CR
2. **Label Addition**: Controller adds `checkpoint-migration.dcn.io: "True"` label to the target pod
3. **CheckpointBackup Creation**: Controller creates `CheckpointBackup` resources for each source cluster (cluster-criu)
4. **Karmada Propagation**: CheckpointBackup resources are propagated to cluster-criu via Karmada
5. **Checkpoint Execution**: On cluster-criu, checkpoint backups are created every 5 minutes
6. **Registry Storage**: Checkpoint images are pushed to `docker.io/your-username/checkpoints`

### **StatefulMigration CR Structure**
```yaml
apiVersion: migration.dcn.io/v1
kind: StatefulMigration
metadata:
  name: inmem-go-migration
  namespace: inmem-go-app
spec:
  resourceRef:          # Target workload to backup
    apiVersion: v1
    kind: Pod
    namespace: inmem-go-app
    name: inmem-go-app
  sourceClusters:       # Where the workload runs
    - cluster-criu
  registry:             # Where to store checkpoints
    url: docker.io
    repository: your-username/checkpoints
    secretRef:
      name: registry-secret
  schedule: "*/5 * * * *"  # Backup frequency
```

### **Verification Commands for StatefulMigration**
```bash
# Check StatefulMigration resource
kubectl get statefulmigration inmem-go-migration -n inmem-go-app

# Check if migration label was added to pod
kubectl get pod inmem-go-app -n inmem-go-app --show-labels

# Check CheckpointBackup resources (on mgmt cluster)
kubectl get checkpointbackup -A

# Check migration controller logs
kubectl logs -n stateful-migration deployment/migration-backup-controller -f

# Check propagated CheckpointBackup on cluster-criu
kubectl --context=cluster-criu get checkpointbackup -A
``` 