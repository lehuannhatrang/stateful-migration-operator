#!/bin/bash

# Install migration CRDs on management cluster
# This script installs the StatefulMigration, CheckpointBackup, and CheckpointRestore CRDs

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Configuration
MGMT_KUBECONFIG=${1:-""}

echo "🔧 Installing Migration CRDs on Management Cluster"
echo "================================================="

if [[ -n "$MGMT_KUBECONFIG" ]]; then
    print_status "Using mgmt cluster kubeconfig: $MGMT_KUBECONFIG"
    export KUBECONFIG="$MGMT_KUBECONFIG"
else
    print_status "Using current kubeconfig context"
fi

# Check kubectl connectivity
print_status "Checking cluster connectivity..."
if ! kubectl cluster-info >/dev/null 2>&1; then
    print_error "Cannot connect to management cluster"
    exit 1
fi
print_success "Connected to cluster: $(kubectl config current-context)"

# Check if we're in the correct directory
if [[ ! -d "config/crd/bases" ]]; then
    print_error "config/crd/bases directory not found"
    print_status "Please run this script from the stateful-migration-operator root directory"
    exit 1
fi

# Install CRDs
print_status "Installing migration CRDs..."
kubectl apply -f config/crd/bases/migration.dcnlab.com_statefulmigrations.yaml
kubectl apply -f config/crd/bases/migration.dcnlab.com_checkpointbackups.yaml
kubectl apply -f config/crd/bases/migration.dcnlab.com_checkpointrestores.yaml

print_success "Migration CRDs installed successfully!"

# Verify installation
print_status "Verifying CRD installation..."
echo
echo "Installed CRDs:"
kubectl get crd | grep migration.dcnlab.com

echo
print_success "✅ Migration CRDs are ready!"
print_status "You can now deploy StatefulMigration resources to this cluster." 