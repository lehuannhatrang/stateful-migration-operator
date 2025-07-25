#!/bin/bash

# Deployment script for Migration Backup Controller
# Deploys to mgmt-cluster in stateful-migration namespace

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
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

# Configuration
DOCKER_IMAGE=${1:-""}
KUBECONFIG_PATH=${2:-""}
NAMESPACE="stateful-migration"

# Function to show usage
show_usage() {
    echo "Usage: $0 <docker-image> [kubeconfig-path]"
    echo
    echo "Examples:"
    echo "  $0 myusername/stateful-migration-operator:latest"
    echo "  $0 myusername/stateful-migration-operator:v1.0.0 ~/.kube/mgmt-cluster-config"
    echo
    echo "Parameters:"
    echo "  docker-image      - Your Docker Hub image (required)"
    echo "  kubeconfig-path   - Path to mgmt-cluster kubeconfig (optional)"
}

# Check if image is provided
if [[ -z "$DOCKER_IMAGE" ]]; then
    print_error "Docker image is required!"
    echo
    show_usage
    exit 1
fi

echo "🚀 Deploying Migration Backup Controller"
echo "========================================"
echo "Image: $DOCKER_IMAGE"
echo "Namespace: $NAMESPACE"
echo "Target: mgmt-cluster"
if [[ -n "$KUBECONFIG_PATH" ]]; then
    echo "Kubeconfig: $KUBECONFIG_PATH"
    export KUBECONFIG="$KUBECONFIG_PATH"
fi
echo

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    print_error "kubectl is not installed or not in PATH"
    exit 1
fi

# Check cluster connectivity
print_status "Checking cluster connectivity..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    print_error "Cannot connect to Kubernetes cluster"
    print_status "Please check your kubeconfig and cluster connectivity"
    exit 1
fi
print_success "Connected to cluster: $(kubectl config current-context)"

# Check if CRDs exist
print_status "Checking if required CRDs exist..."
MISSING_CRDS=()

if ! kubectl get crd statefulmigrations.migration.dcnlab.com >/dev/null 2>&1; then
    MISSING_CRDS+=("statefulmigrations.migration.dcnlab.com")
fi

if ! kubectl get crd checkpointbackups.migration.dcnlab.com >/dev/null 2>&1; then
    MISSING_CRDS+=("checkpointbackups.migration.dcnlab.com")
fi

if [[ ${#MISSING_CRDS[@]} -gt 0 ]]; then
    print_warning "Missing CRDs: ${MISSING_CRDS[*]}"
    print_status "Installing CRDs..."
    
    # Try to install CRDs from the project
    if [[ -f "../config/crd/bases/migration.dcnlab.com_statefulmigrations.yaml" ]]; then
        kubectl apply -f ../config/crd/bases/
        print_success "CRDs installed from project"
    else
        print_error "CRDs not found. Please install them manually:"
        print_status "  make install  # from project root"
        exit 1
    fi
else
    print_success "All required CRDs found"
fi

# Create temporary deployment file with correct image
print_status "Preparing deployment manifests..."
TEMP_FILE=$(mktemp)
cp all-in-one.yaml "$TEMP_FILE"

# Replace image placeholder with actual image
sed -i "s|YOUR_DOCKERHUB_USERNAME/stateful-migration-operator:latest|$DOCKER_IMAGE|g" "$TEMP_FILE"

print_success "Manifests prepared with image: $DOCKER_IMAGE"

# Apply the manifests
print_status "Applying deployment manifests..."
if kubectl apply -f "$TEMP_FILE"; then
    print_success "Manifests applied successfully"
else
    print_error "Failed to apply manifests"
    rm -f "$TEMP_FILE"
    exit 1
fi

# Clean up temp file
rm -f "$TEMP_FILE"

# Wait for deployment to be ready
print_status "Waiting for deployment to be ready..."
if kubectl wait --for=condition=Available deployment/migration-backup-controller -n "$NAMESPACE" --timeout=300s; then
    print_success "Deployment is ready"
else
    print_error "Deployment failed to become ready"
    print_status "Check logs with: kubectl logs -n $NAMESPACE deployment/migration-backup-controller"
    exit 1
fi

# Check pod status
print_status "Checking pod status..."
kubectl get pods -n "$NAMESPACE" -l app.kubernetes.io/name=migration-backup-controller

# Show service status
print_status "Checking service status..."
kubectl get svc -n "$NAMESPACE" migration-backup-controller-metrics

# Success summary
echo
echo "🎉 Deployment Complete!"
echo "======================"
echo "Namespace: $NAMESPACE"
echo "Image: $DOCKER_IMAGE"
echo "Status: $(kubectl get deployment migration-backup-controller -n $NAMESPACE -o jsonpath='{.status.conditions[?(@.type=="Available")].status}')"
echo
echo "Useful commands:"
echo "  # Check pods"
echo "  kubectl get pods -n $NAMESPACE"
echo
echo "  # Check logs"
echo "  kubectl logs -n $NAMESPACE deployment/migration-backup-controller -f"
echo
echo "  # Check StatefulMigrations"
echo "  kubectl get statefulmigrations -A"
echo
echo "  # Check CheckpointBackups"
echo "  kubectl get checkpointbackups -A"
echo
echo "  # Port forward metrics (optional)"
echo "  kubectl port-forward -n $NAMESPACE svc/migration-backup-controller-metrics 8080:8080"

print_success "Migration Backup Controller deployed successfully!" 