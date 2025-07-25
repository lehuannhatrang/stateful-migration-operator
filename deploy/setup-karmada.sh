#!/bin/bash

# Setup script for creating Karmada kubeconfig secret
# This script helps you create the secret with your karmada-apiserver kubeconfig

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
KARMADA_KUBECONFIG_PATH=${1:-""}
NAMESPACE="stateful-migration"
SECRET_NAME="karmada-kubeconfig"

# Function to show usage
show_usage() {
    echo "Usage: $0 <karmada-kubeconfig-path>"
    echo
    echo "Examples:"
    echo "  $0 ~/.kube/karmada-apiserver-config"
    echo "  $0 /path/to/karmada-apiserver.kubeconfig"
    echo
    echo "This script creates a secret containing your Karmada API server kubeconfig"
    echo "for the migration backup controller to use."
}

# Check if kubeconfig path is provided
if [[ -z "$KARMADA_KUBECONFIG_PATH" ]]; then
    print_error "Karmada kubeconfig path is required!"
    echo
    show_usage
    exit 1
fi

# Check if kubeconfig file exists
if [[ ! -f "$KARMADA_KUBECONFIG_PATH" ]]; then
    print_error "Karmada kubeconfig file not found: $KARMADA_KUBECONFIG_PATH"
    exit 1
fi

echo "🔧 Setting up Karmada kubeconfig secret"
echo "======================================="
echo "Kubeconfig file: $KARMADA_KUBECONFIG_PATH"
echo "Namespace: $NAMESPACE"
echo "Secret name: $SECRET_NAME"
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

# Create namespace if it doesn't exist
print_status "Ensuring namespace exists..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
print_success "Namespace '$NAMESPACE' ready"

# Delete existing secret if it exists
if kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    print_warning "Secret '$SECRET_NAME' already exists, deleting it..."
    kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE"
fi

# Create the secret
print_status "Creating Karmada kubeconfig secret..."
kubectl create secret generic "$SECRET_NAME" \
    --from-file=kubeconfig="$KARMADA_KUBECONFIG_PATH" \
    --namespace="$NAMESPACE"

if [[ $? -eq 0 ]]; then
    print_success "Secret created successfully"
else
    print_error "Failed to create secret"
    exit 1
fi

# Add labels to the secret
print_status "Adding labels to secret..."
kubectl label secret "$SECRET_NAME" -n "$NAMESPACE" \
    app.kubernetes.io/name=migration-backup-controller \
    app.kubernetes.io/component=karmada-config

# Verify the secret
print_status "Verifying secret..."
kubectl describe secret "$SECRET_NAME" -n "$NAMESPACE"

# Test the kubeconfig
print_status "Testing Karmada connectivity..."
if kubectl --kubeconfig="$KARMADA_KUBECONFIG_PATH" get nodes >/dev/null 2>&1; then
    print_success "Karmada kubeconfig is valid and accessible"
else
    print_warning "Could not verify Karmada connectivity (this might be normal)"
fi

# Success summary
echo
echo "🎉 Karmada Setup Complete!"
echo "========================="
echo "Secret: $SECRET_NAME"
echo "Namespace: $NAMESPACE"
echo "Mount path: /etc/karmada/kubeconfig"
echo
echo "The migration backup controller will now be able to:"
echo "✅ Connect to your Karmada API server"
echo "✅ Create PropagationPolicy resources"
echo "✅ Distribute CheckpointBackup resources to target clusters"
echo
echo "Next steps:"
echo "1. Build and push your updated controller image:"
echo "   ./build-and-push.sh lehuannhatrang latest"
echo
echo "2. Deploy the controller:"
echo "   cd deploy && ./deploy.sh lehuannhatrang/stateful-migration-operator:latest"
echo
echo "3. Verify the controller can connect to Karmada:"
echo "   kubectl logs -n $NAMESPACE deployment/migration-backup-controller -f"

print_success "Karmada kubeconfig secret setup completed successfully!" 