#!/bin/bash

# Deploy script for inmem-go workload with Karmada propagation
# This script deploys the workload to Karmada control plane and propagates it to cluster-criu

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
KARMADA_KUBECONFIG=${1:-""}
POLICY_TYPE=${2:-"namespaced"}  # "namespaced" or "cluster"

# Function to show usage
show_usage() {
    echo "Usage: $0 [karmada-kubeconfig] [policy-type]"
    echo
    echo "Examples:"
    echo "  $0 ~/.kube/karmada-apiserver-config namespaced"
    echo "  $0 ~/.kube/karmada-apiserver-config cluster"
    echo "  $0  # Uses current kubeconfig, namespaced policy"
    echo
    echo "Parameters:"
    echo "  karmada-kubeconfig  - Path to Karmada API server kubeconfig (optional)"
    echo "  policy-type         - 'namespaced' or 'cluster' (default: namespaced)"
}

echo "🚀 Deploying inmem-go workload with Karmada propagation"
echo "======================================================"
echo "Target cluster: cluster-criu"
echo "Policy type: $POLICY_TYPE"
if [[ -n "$KARMADA_KUBECONFIG" ]]; then
    echo "Karmada kubeconfig: $KARMADA_KUBECONFIG"
    export KUBECONFIG="$KARMADA_KUBECONFIG"
fi
echo

# Check if kubectl is available
if ! command -v kubectl &> /dev/null; then
    print_error "kubectl is not installed or not in PATH"
    exit 1
fi

# Check cluster connectivity
print_status "Checking Karmada connectivity..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    print_error "Cannot connect to Karmada cluster"
    print_status "Please check your kubeconfig and cluster connectivity"
    exit 1
fi
print_success "Connected to Karmada: $(kubectl config current-context)"

# Check if cluster-criu exists
print_status "Checking if cluster-criu is joined to Karmada..."
if kubectl get cluster cluster-criu >/dev/null 2>&1; then
    print_success "cluster-criu is available in Karmada"
else
    print_warning "cluster-criu not found in Karmada clusters"
    print_status "Available clusters:"
    kubectl get clusters --no-headers -o custom-columns=NAME:.metadata.name 2>/dev/null || echo "  No clusters found"
    echo
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Deploy workload resources
print_status "Deploying workload resources..."
kubectl apply -f inmem-go-workload.yaml
print_success "Workload resources deployed"

# Wait a moment for resources to be created
sleep 2

# Deploy propagation policy
if [[ "$POLICY_TYPE" == "cluster" ]]; then
    print_status "Deploying ClusterPropagationPolicy..."
    kubectl apply -f cluster-propagation-policy.yaml
    print_success "ClusterPropagationPolicy deployed"
else
    print_status "Deploying PropagationPolicy..."
    kubectl apply -f propagation-policy.yaml
    print_success "PropagationPolicy deployed"
fi

# Wait for propagation
print_status "Waiting for propagation to complete..."
sleep 5

# Check propagation status
print_status "Checking propagation status..."

echo
echo "Namespace status:"
kubectl get namespace inmem-go-app -o wide 2>/dev/null || echo "Namespace not found"

echo
echo "Pod status:"
kubectl get pod inmem-go-app -n inmem-go-app -o wide 2>/dev/null || echo "Pod not found"

echo
echo "Service status:"
kubectl get service inmem-go-service -n inmem-go-app -o wide 2>/dev/null || echo "Service not found"

echo
echo "PropagationPolicy status:"
if [[ "$POLICY_TYPE" == "cluster" ]]; then
    kubectl get clusterpropagationpolicy inmem-go-cluster-propagation -o wide 2>/dev/null || echo "ClusterPropagationPolicy not found"
else
    kubectl get propagationpolicy inmem-go-propagation -n inmem-go-app -o wide 2>/dev/null || echo "PropagationPolicy not found"
fi

# Check ResourceBinding
echo
echo "ResourceBinding status:"
kubectl get resourcebinding -A 2>/dev/null | grep inmem-go || echo "No ResourceBindings found for inmem-go"

# Success summary
echo
echo "🎉 Deployment Complete!"
echo "======================"
echo "Workload: inmem-go-app"
echo "Target cluster: cluster-criu"
echo "Service NodePort: 30180"
echo
echo "Useful commands:"
echo "  # Check cluster status"
echo "  kubectl get clusters"
echo
echo "  # Check propagation status"
if [[ "$POLICY_TYPE" == "cluster" ]]; then
echo "  kubectl describe clusterpropagationpolicy inmem-go-cluster-propagation"
else
echo "  kubectl describe propagationpolicy inmem-go-propagation -n inmem-go-app"
fi
echo
echo "  # Check resource bindings"
echo "  kubectl get resourcebinding -A"
echo
echo "  # Check work objects (actual propagated resources)"
echo "  kubectl get work -A"
echo
echo "  # Access the application (once propagated to cluster-criu)"
echo "  curl http://<cluster-criu-node-ip>:30180"

print_success "inmem-go workload deployed and propagated to cluster-criu!" 