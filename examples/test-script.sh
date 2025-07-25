#!/bin/bash

# Test script for Migration Backup Controller
# This script helps you test the migration backup controller with various scenarios

set -e

echo "🚀 Migration Backup Controller Test Suite"
echo "=========================================="

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

# Function to wait for resources to be ready
wait_for_resource() {
    local resource_type=$1
    local resource_name=$2
    local namespace=$3
    local timeout=${4:-60}
    
    print_status "Waiting for $resource_type/$resource_name to be ready..."
    kubectl wait --for=condition=Ready $resource_type/$resource_name -n $namespace --timeout=${timeout}s
}

# Function to check if CRDs exist
check_crds() {
    print_status "Checking if required CRDs are installed..."
    
    if kubectl get crd statefulmigrations.migration.dcnlab.com &>/dev/null; then
        print_success "StatefulMigration CRD found"
    else
        print_error "StatefulMigration CRD not found. Please install CRDs first."
        exit 1
    fi
    
    if kubectl get crd checkpointbackups.migration.dcnlab.com &>/dev/null; then
        print_success "CheckpointBackup CRD found"
    else
        print_error "CheckpointBackup CRD not found. Please install CRDs first."
        exit 1
    fi
}

# Function to apply test resources
apply_test_resources() {
    print_status "Applying test resources..."
    kubectl apply -f examples/test-migration-backup.yaml
    print_success "Test resources applied"
}

# Function to check controller deployment
check_controller() {
    print_status "Checking if migration backup controller is running..."
    
    # Check if the controller pod is running
    if kubectl get pods -n stateful-migration-operator-system | grep -q "Running"; then
        print_success "Controller is running"
    else
        print_warning "Controller may not be running. Check deployment status."
    fi
}

# Function to monitor StatefulMigration resources
monitor_statefulmigrations() {
    print_status "Monitoring StatefulMigration resources..."
    echo
    kubectl get statefulmigrations -n stateful-migration-test -o wide
    echo
}

# Function to monitor CheckpointBackup resources
monitor_checkpointbackups() {
    print_status "Monitoring CheckpointBackup resources..."
    echo
    kubectl get checkpointbackups -n stateful-migration-test -o wide
    echo
}

# Function to check labels on target resources
check_labels() {
    print_status "Checking labels on target resources..."
    
    echo "StatefulSet my-app labels:"
    kubectl get statefulset my-app -n stateful-migration-test -o jsonpath='{.metadata.labels}' | jq .
    echo
    
    echo "Deployment web-app labels:"
    kubectl get deployment web-app -n stateful-migration-test -o jsonpath='{.metadata.labels}' | jq .
    echo
}

# Function to show controller logs
show_controller_logs() {
    print_status "Showing controller logs (last 50 lines)..."
    kubectl logs -n stateful-migration-operator-system -l control-plane=controller-manager --tail=50
}

# Function to cleanup test resources
cleanup() {
    print_status "Cleaning up test resources..."
    kubectl delete -f examples/test-migration-backup.yaml --ignore-not-found=true
    print_success "Cleanup completed"
}

# Function to describe all StatefulMigration resources
describe_statefulmigrations() {
    print_status "Describing StatefulMigration resources..."
    kubectl get statefulmigrations -n stateful-migration-test -o name | while read sm; do
        echo "--- $sm ---"
        kubectl describe $sm -n stateful-migration-test
        echo
    done
}

# Function to test specific scenario
test_scenario() {
    local scenario=$1
    case $scenario in
        "statefulset")
            print_status "Testing StatefulSet scenario..."
            kubectl apply -f - <<EOF
apiVersion: migration.dcnlab.com/v1
kind: StatefulMigration
metadata:
  name: test-statefulset
  namespace: stateful-migration-test
spec:
  resourceRef:
    apiVersion: apps/v1
    kind: StatefulSet
    namespace: stateful-migration-test
    name: my-app
  sourceClusters:
    - test-cluster
  registry:
    url: registry.example.com
    repository: test/checkpoints
  schedule: "*/5 * * * *"
EOF
            ;;
        "deployment")
            print_status "Testing Deployment scenario..."
            kubectl apply -f - <<EOF
apiVersion: migration.dcnlab.com/v1
kind: StatefulMigration
metadata:
  name: test-deployment
  namespace: stateful-migration-test
spec:
  resourceRef:
    apiVersion: apps/v1
    kind: Deployment
    namespace: stateful-migration-test
    name: web-app
  sourceClusters:
    - test-cluster
  registry:
    url: registry.example.com
    repository: test/checkpoints
  schedule: "*/10 * * * *"
EOF
            ;;
        "pod")
            print_status "Testing Pod scenario..."
            kubectl apply -f - <<EOF
apiVersion: migration.dcnlab.com/v1
kind: StatefulMigration
metadata:
  name: test-pod
  namespace: stateful-migration-test
spec:
  resourceRef:
    apiVersion: v1
    kind: Pod
    namespace: stateful-migration-test
    name: standalone-pod
  sourceClusters:
    - test-cluster
  registry:
    url: registry.example.com
    repository: test/checkpoints
  schedule: "*/15 * * * *"
EOF
            ;;
        *)
            print_error "Unknown scenario: $scenario"
            print_status "Available scenarios: statefulset, deployment, pod"
            exit 1
            ;;
    esac
}

# Main menu
show_menu() {
    echo
    echo "Select an option:"
    echo "1) Check CRDs"
    echo "2) Apply test resources"
    echo "3) Check controller status"
    echo "4) Monitor StatefulMigrations"
    echo "5) Monitor CheckpointBackups"
    echo "6) Check target resource labels"
    echo "7) Show controller logs"
    echo "8) Describe StatefulMigrations"
    echo "9) Test specific scenario"
    echo "10) Cleanup test resources"
    echo "11) Full test run"
    echo "0) Exit"
    echo
    read -p "Enter your choice: " choice
}

# Full test run
full_test_run() {
    print_status "Running full test suite..."
    check_crds
    apply_test_resources
    sleep 10
    check_controller
    monitor_statefulmigrations
    sleep 5
    monitor_checkpointbackups
    check_labels
    print_success "Full test run completed!"
}

# Main execution
case "${1:-menu}" in
    "check-crds")
        check_crds
        ;;
    "apply")
        apply_test_resources
        ;;
    "monitor")
        monitor_statefulmigrations
        monitor_checkpointbackups
        ;;
    "logs")
        show_controller_logs
        ;;
    "cleanup")
        cleanup
        ;;
    "full-test")
        full_test_run
        ;;
    "scenario")
        test_scenario $2
        ;;
    "menu"|*)
        while true; do
            show_menu
            case $choice in
                1) check_crds ;;
                2) apply_test_resources ;;
                3) check_controller ;;
                4) monitor_statefulmigrations ;;
                5) monitor_checkpointbackups ;;
                6) check_labels ;;
                7) show_controller_logs ;;
                8) describe_statefulmigrations ;;
                9) 
                    echo "Available scenarios: statefulset, deployment, pod"
                    read -p "Enter scenario: " scenario
                    test_scenario $scenario
                    ;;
                10) cleanup ;;
                11) full_test_run ;;
                0) print_success "Goodbye!"; exit 0 ;;
                *) print_error "Invalid option" ;;
            esac
            echo
            read -p "Press Enter to continue..."
        done
        ;;
esac 