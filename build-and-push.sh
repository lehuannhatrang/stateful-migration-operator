#!/bin/bash

# Build and Push Script for Migration Backup Controller
# This script builds the controller and pushes it to Docker Hub

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
DOCKERHUB_USERNAME=${1:-""}
IMAGE_TAG=${2:-"latest"}
REPOSITORY_NAME="stateful-migration-operator"

# Function to show usage
show_usage() {
    echo "Usage: $0 <dockerhub-username> [tag]"
    echo
    echo "Examples:"
    echo "  $0 myusername latest"
    echo "  $0 myusername v1.0.0"
    echo "  $0 myusername $(date +%Y%m%d)"
    echo
    echo "Environment variables:"
    echo "  DOCKERHUB_USERNAME - Your Docker Hub username"
    echo "  IMAGE_TAG - Tag for the image (default: latest)"
}

# Check if username is provided
if [[ -z "$DOCKERHUB_USERNAME" ]]; then
    print_error "Docker Hub username is required!"
    echo
    show_usage
    exit 1
fi

# Construct full image name
FULL_IMAGE_NAME="${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:${IMAGE_TAG}"

echo "🚀 Building and Pushing Migration Backup Controller"
echo "=================================================="
echo "Image: $FULL_IMAGE_NAME"
echo "Tag: $IMAGE_TAG"
echo

# Check if Docker is running
if ! docker info >/dev/null 2>&1; then
    print_error "Docker is not running or not accessible!"
    exit 1
fi

# Check if user is logged in to Docker Hub
if ! docker info | grep -q "Username:"; then
    print_warning "You may not be logged in to Docker Hub"
    print_status "Please run: docker login"
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

# Step 1: Generate latest manifests and CRDs
print_status "Generating CRDs and manifests..."
make manifests generate
print_success "Manifests and CRDs generated"

# Step 2: Run tests (optional, skip if they fail)
print_status "Running tests..."
if make test 2>/dev/null; then
    print_success "Tests passed"
else
    print_warning "Tests failed or skipped"
    read -p "Continue with build? (Y/n): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Nn]$ ]]; then
        exit 1
    fi
fi

# Step 3: Build the Docker image
print_status "Building Docker image: $FULL_IMAGE_NAME"
if make docker-build IMG="$FULL_IMAGE_NAME"; then
    print_success "Docker image built successfully"
else
    print_error "Failed to build Docker image"
    exit 1
fi

# Step 4: Test the image locally (quick verification)
print_status "Testing the image locally..."
if docker run --rm --entrypoint="" "$FULL_IMAGE_NAME" /manager --help >/dev/null 2>&1; then
    print_success "Image test passed"
else
    print_warning "Image test failed, but continuing with push"
fi

# Step 5: Push to Docker Hub
print_status "Pushing image to Docker Hub: $FULL_IMAGE_NAME"
if make docker-push IMG="$FULL_IMAGE_NAME"; then
    print_success "Image pushed successfully to Docker Hub!"
else
    print_error "Failed to push image to Docker Hub"
    exit 1
fi

# Step 6: Update deployment manifests (optional)
print_status "Updating deployment manifests with new image..."
cd config/manager && kustomize edit set image controller="$FULL_IMAGE_NAME"
print_success "Deployment manifest updated"

# Summary
echo
echo "🎉 Build and Push Complete!"
echo "=========================="
echo "Image: $FULL_IMAGE_NAME"
echo "Size: $(docker images --format "table {{.Repository}}:{{.Tag}}\t{{.Size}}" | grep "$FULL_IMAGE_NAME" | awk '{print $2}')"
echo
echo "To deploy this image:"
echo "  make deploy IMG=$FULL_IMAGE_NAME"
echo
echo "To pull this image:"
echo "  docker pull $FULL_IMAGE_NAME"
echo
echo "To use in Kubernetes manifests:"
echo "  image: $FULL_IMAGE_NAME"

# Optional: Create a deployment example
cat > deploy-example.yaml <<EOF
# Example deployment with your custom image
apiVersion: apps/v1
kind: Deployment
metadata:
  name: migration-backup-controller
  namespace: stateful-migration-operator-system
spec:
  replicas: 1
  selector:
    matchLabels:
      control-plane: controller-manager
  template:
    metadata:
      labels:
        control-plane: controller-manager
    spec:
      containers:
      - name: manager
        image: $FULL_IMAGE_NAME
        command:
        - /manager
        args:
        - --leader-elect
        - --metrics-bind-address=0.0.0.0:8080
        ports:
        - containerPort: 8080
          name: metrics
        - containerPort: 9443
          name: webhook-server
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
EOF

print_success "Created deploy-example.yaml with your image" 