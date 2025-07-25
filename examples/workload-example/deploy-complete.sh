#!/bin/bash

# Complete deployment script for inmem-go workload with Karmada propagation and StatefulMigration
# This script demonstrates the full migration backup workflow

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
PURPLE='\033[0;35m'
NC='\033[0m' # No Color

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_step() {
    echo -e "${PURPLE}[STEP]${NC} $1"
}

# Configuration
KARMADA_KUBECONFIG=${1:-""}
MGMT_KUBECONFIG=${2:-""}
DOCKER_USERNAME=${3:-"lehuannhatrang"}
DOCKER_PASSWORD=${4:-""}

# Function to show usage
show_usage() {
    echo "Complete StatefulMigration Deployment Script"
    echo "============================================="
    echo
    echo "Usage: $0 [karmada-kubeconfig] [mgmt-kubeconfig] [docker-username] [docker-password]"
    echo
    echo "Examples:"
    echo "  $0 ~/.kube/karmada-config ~/.kube/mgmt-config myuser mypass"
    echo "  $0 ~/.kube/karmada-config ~/.kube/mgmt-config  # Will prompt for Docker credentials"
    echo "  $0  # Uses current kubeconfig for both, prompts for credentials"
    echo
    echo "Parameters:"
    echo "  karmada-kubeconfig  - Path to Karmada API server kubeconfig (optional)"
    echo "  mgmt-kubeconfig     - Path to management cluster kubeconfig (optional)"
    echo "  docker-username     - Docker Hub username (default: lehuannhatrang)"
    echo "  docker-password     - Docker Hub password (will prompt if not provided)"
    echo
    echo "This script will:"
    echo "  1. Deploy workload to Karmada (Pod + Service)"
    echo "  2. Apply PropagationPolicy to send workload to cluster-criu"
    echo "  3. Create registry secret for checkpoint storage"
    echo "  4. Deploy StatefulMigration CR to enable backup monitoring"
    echo "  5. Verify the complete setup"
}

if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    show_usage
    exit 0
fi

echo "🚀 Complete StatefulMigration Deployment"
echo "========================================"
echo "Workload: inmem-go-app"
echo "Target cluster: cluster-criu"
echo "Docker registry: docker.io/$DOCKER_USERNAME/checkpoints"
echo

# Prompt for Docker password if not provided
if [[ -z "$DOCKER_PASSWORD" ]]; then
    echo -n "Enter Docker Hub password for $DOCKER_USERNAME: "
    read -s DOCKER_PASSWORD
    echo
    if [[ -z "$DOCKER_PASSWORD" ]]; then
        print_error "Docker password is required for registry secret"
        exit 1
    fi
fi

# Check kubectl
if ! command -v kubectl &> /dev/null; then
    print_error "kubectl is not installed or not in PATH"
    exit 1
fi

# =====================================================
# STEP 1: Deploy to Karmada Control Plane
# =====================================================

print_step "1. Deploying workload to Karmada control plane"

if [[ -n "$KARMADA_KUBECONFIG" ]]; then
    print_status "Using Karmada kubeconfig: $KARMADA_KUBECONFIG"
    export KUBECONFIG="$KARMADA_KUBECONFIG"
fi

# Check Karmada connectivity
print_status "Checking Karmada connectivity..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    print_error "Cannot connect to Karmada cluster"
    exit 1
fi
print_success "Connected to Karmada: $(kubectl config current-context)"

# Check if cluster-criu exists
print_status "Checking if cluster-criu is available..."
if kubectl get cluster cluster-criu >/dev/null 2>&1; then
    print_success "cluster-criu is available in Karmada"
else
    print_warning "cluster-criu not found in Karmada clusters"
    kubectl get clusters --no-headers -o custom-columns=NAME:.metadata.name 2>/dev/null || echo "  No clusters found"
fi

# Deploy workload and propagation policy
print_status "Deploying workload resources..."
kubectl apply -f inmem-go-workload.yaml
print_success "Workload deployed"

print_status "Applying PropagationPolicy..."
kubectl apply -f propagation-policy.yaml
print_success "PropagationPolicy applied"

# Wait for propagation
print_status "Waiting for propagation to complete..."
sleep 5

# =====================================================
# STEP 2: Create Registry Secret with Real Credentials
# =====================================================

print_step "2. Creating registry secret with provided credentials"

# Encode credentials
DOCKER_USERNAME_B64=$(echo -n "$DOCKER_USERNAME" | base64 -w 0)
DOCKER_PASSWORD_B64=$(echo -n "$DOCKER_PASSWORD" | base64 -w 0)

# Create secret with real credentials
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: registry-secret
  namespace: inmem-go-app
  labels:
    app: inmem-go
    component: registry-auth
type: Opaque
data:
  username: $DOCKER_USERNAME_B64
  password: $DOCKER_PASSWORD_B64
EOF

print_success "Registry secret created with provided credentials"

# =====================================================
# STEP 3: Deploy StatefulMigration CR
# =====================================================

print_step "3. Deploying StatefulMigration CR"

# Create StatefulMigration with correct registry info
cat <<EOF | kubectl apply -f -
apiVersion: migration.dcnlab.com/v1
kind: StatefulMigration
metadata:
  name: inmem-go-migration
  namespace: inmem-go-app
  labels:
    app: inmem-go
    migration-type: pod-checkpoint
    target-cluster: cluster-criu
  annotations:
    description: "Enable checkpoint backups for inmem-go pod running on cluster-criu"
    created-by: "migration-backup-controller"
spec:
  resourceRef:
    apiVersion: v1
    kind: Pod
    namespace: inmem-go-app
    name: inmem-go-app
  sourceClusters:
    - cluster-criu
  registry:
    url: docker.io
    repository: $DOCKER_USERNAME/checkpoints
    secretRef:
      name: registry-secret
  schedule: "*/5 * * * *"
EOF

print_success "StatefulMigration CR deployed"

# =====================================================
# STEP 4: Verify on Management Cluster (if available)
# =====================================================

if [[ -n "$MGMT_KUBECONFIG" ]]; then
    print_step "4. Checking migration backup controller on management cluster"
    
    # Switch to management cluster
    export KUBECONFIG="$MGMT_KUBECONFIG"
    
    print_status "Checking controller deployment..."
    kubectl get deployment migration-backup-controller -n stateful-migration >/dev/null 2>&1 && \
        print_success "Migration backup controller is deployed" || \
        print_warning "Migration backup controller not found (deploy first with deploy/deploy.sh)"
    
    # Check if controller is processing our StatefulMigration
    print_status "Checking for CheckpointBackup resources (may take a few minutes)..."
    sleep 10
    
    kubectl get checkpointbackup -A 2>/dev/null | grep inmem-go && \
        print_success "CheckpointBackup resources are being created!" || \
        print_status "CheckpointBackup resources not yet created (controller may still be processing)"
    
    # Switch back to Karmada
    if [[ -n "$KARMADA_KUBECONFIG" ]]; then
        export KUBECONFIG="$KARMADA_KUBECONFIG"
    fi
else
    print_step "4. Skipping management cluster check (kubeconfig not provided)"
fi

# =====================================================
# STEP 5: Show Status and Verification Commands
# =====================================================

print_step "5. Deployment status and verification"

echo
echo "Karmada Resources:"
echo "=================="
kubectl get namespace inmem-go-app -o wide 2>/dev/null || echo "Namespace not found"
kubectl get pod inmem-go-app -n inmem-go-app -o wide 2>/dev/null || echo "Pod not found"
kubectl get service inmem-go-service -n inmem-go-app -o wide 2>/dev/null || echo "Service not found"
kubectl get propagationpolicy inmem-go-propagation -n inmem-go-app -o wide 2>/dev/null || echo "PropagationPolicy not found"
kubectl get statefulmigration inmem-go-migration -n inmem-go-app -o wide 2>/dev/null || echo "StatefulMigration not found"

echo
echo "Propagation Status:"
echo "==================="
kubectl get resourcebinding -A 2>/dev/null | grep inmem-go || echo "No ResourceBindings found for inmem-go"
kubectl get work -A 2>/dev/null | grep inmem-go || echo "No Work objects found for inmem-go"

# Success summary
echo
echo "🎉 Complete Deployment Finished!"
echo "================================="
echo "✅ Workload deployed to Karmada"
echo "✅ PropagationPolicy applied (targets cluster-criu)"
echo "✅ Registry secret created with your credentials"
echo "✅ StatefulMigration CR deployed"
echo
echo "📋 What happens next:"
echo "  1. Karmada propagates Pod + Service to cluster-criu"
echo "  2. Migration backup controller detects StatefulMigration"
echo "  3. Controller adds label 'checkpoint-migration.dcn.io: True' to the pod"
echo "  4. Controller creates CheckpointBackup resources for cluster-criu"
echo "  5. Karmada propagates CheckpointBackup to cluster-criu"
echo "  6. Checkpoint creation happens every 5 minutes"
echo
echo "🔍 Useful verification commands:"
echo
echo "  # Check StatefulMigration status"
echo "  kubectl describe statefulmigration inmem-go-migration -n inmem-go-app"
echo
echo "  # Check if pod has migration label (added by controller)"
echo "  kubectl get pod inmem-go-app -n inmem-go-app --show-labels"
echo
echo "  # Check propagation status"
echo "  kubectl get resourcebinding -A | grep inmem-go"
echo "  kubectl get work -A | grep inmem-go"
echo
if [[ -n "$MGMT_KUBECONFIG" ]]; then
echo "  # Check CheckpointBackup resources (on mgmt cluster)"
echo "  export KUBECONFIG=\"$MGMT_KUBECONFIG\""
echo "  kubectl get checkpointbackup -A"
echo "  kubectl logs -n stateful-migration deployment/migration-backup-controller -f"
echo
fi
echo "  # Check on cluster-criu (if you have direct access)"
echo "  kubectl --context=cluster-criu get pods -n inmem-go-app"
echo "  kubectl --context=cluster-criu get checkpointbackup -A"
echo "  curl http://<cluster-criu-node-ip>:30180"

print_success "🚀 StatefulMigration setup complete! Your inmem-go workload is now monitored for checkpoint backups." 