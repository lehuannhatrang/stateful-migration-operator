#!/bin/bash

# Build and Push Script for Stateful Migration Operator Controllers
# This script builds different controller variants and pushes them to Docker Hub

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

print_header() {
    echo -e "${PURPLE}[BUILD]${NC} $1"
}

# Configuration
DOCKERHUB_USERNAME="lehuannhatrang"
VERSION=${2:-"latest"}
REPOSITORY_NAME="stateful-migration-operator"

# Controller configurations
declare -A CONTROLLERS=(
    ["checkpoint"]="checkpointBackup_${VERSION}"
    ["migration"]="migrationBackup_${VERSION}"
    ["restore"]="migrationRestore_${VERSION}"
    ["apiserver"]="checkpointApiserver_${VERSION}"
)

declare -A CONTROLLER_FLAGS=(
    ["checkpoint"]="--enable-checkpoint-backup-controller=true --enable-migration-backup-controller=false --enable-migration-restore-controller=false"
    ["migration"]="--enable-checkpoint-backup-controller=false --enable-migration-backup-controller=true --enable-migration-restore-controller=false"
    ["restore"]="--enable-checkpoint-backup-controller=false --enable-migration-backup-controller=false --enable-migration-restore-controller=true"
    ["apiserver"]=""
)

declare -A CONTROLLER_DESCRIPTIONS=(
    ["checkpoint"]="CheckpointBackup Controller (DaemonSet for member clusters)"
    ["migration"]="MigrationBackup Controller (Karmada control plane)"
    ["restore"]="MigrationRestore Controller (Karmada control plane)"
    ["apiserver"]="Checkpoint API Server (REST API for checkpoint management)"
)

# Function to show usage
show_usage() {
    echo "Build and Push Script for Stateful Migration Operator Controllers"
    echo "================================================================="
    echo
    echo "Usage: $0 [controller-type] [version]"
    echo
    echo "Parameters:"
    echo "  controller-type   - Type of controller to build (default: all)"
    echo "  version          - Version tag for images (default: v1.16)"
    echo
    echo "Controller types:"
    echo "  checkpoint    - Build CheckpointBackup controller only (for member clusters)"
    echo "  migration     - Build MigrationBackup controller only (for Karmada control plane)"
    echo "  restore       - Build MigrationRestore controller only (for Karmada control plane)"
    echo "  apiserver     - Build Checkpoint API Server only (REST API)"
    echo "  all           - Build all controllers and API server (default)"
    echo
    echo "Examples:"
    echo "  $0                        # Build all with default version"
    echo "  $0 all                    # Build all with default version"
    echo "  $0 all v1.17              # Build all with version v1.17"
    echo "  $0 checkpoint             # Build only CheckpointBackup controller"
    echo "  $0 checkpoint v2.0        # Build CheckpointBackup with version v2.0"
    echo "  $0 migration v1.18        # Build MigrationBackup with version v1.18"
    echo "  $0 restore v1.18          # Build MigrationRestore with version v1.18"
    echo "  $0 apiserver v1.0         # Build Checkpoint API Server with version v1.0"
    echo
    echo "Built images will be:"
    echo "  ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointBackup_${VERSION}"
    echo "  ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationBackup_${VERSION}"
    echo "  ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationRestore_${VERSION}"
    echo "  ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointApiserver_${VERSION}"
}

# Function to check prerequisites
check_prerequisites() {
    print_status "Checking prerequisites..."
    
    # Check if Docker is running
    if ! docker info >/dev/null 2>&1; then
        print_error "Docker is not running or not accessible!"
        exit 1
    fi
    
    # Check if logged in to Docker Hub
    if ! docker info | grep -q "Username:"; then
        print_warning "You may not be logged in to Docker Hub"
        print_status "Please run: docker login"
        read -p "Continue anyway? (y/N): " -n 1 -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            exit 1
        fi
    fi
    
    # Check if make is available
    if ! command -v make &> /dev/null; then
        print_error "make command not found!"
        exit 1
    fi
    
    print_success "Prerequisites check passed"
}

# Function to prepare build environment
prepare_build() {
    print_status "Preparing build environment..."
    
    # Generate latest manifests and CRDs
    print_status "Generating CRDs and manifests..."
    if make manifests generate; then
        print_success "Manifests and CRDs generated"
    else
        print_error "Failed to generate manifests and CRDs"
        exit 1
    fi
    
    # Run tests (optional)
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
}

# Function to create controller-specific Dockerfile
create_controller_dockerfile() {
    local controller_type=$1
    local dockerfile_name="Dockerfile.${controller_type}"
    local flags="${CONTROLLER_FLAGS[$controller_type]}"
    
    print_status "Creating ${dockerfile_name}..."
    
    # Convert flags to JSON array format
    local flag_array=""
    IFS=' ' read -ra FLAG_PARTS <<< "$flags"
    for flag in "${FLAG_PARTS[@]}"; do
        if [[ -n "$flag_array" ]]; then
            flag_array+=", "
        fi
        flag_array+="\"$flag\""
    done
    
    if [[ "$controller_type" == "apiserver" ]]; then
        # Checkpoint API Server builds a separate binary
        cat > ${dockerfile_name} <<EOF
# Build the apiserver binary
FROM golang:1.24-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/apiserver/ cmd/apiserver/
COPY api/ api/
COPY internal/apiserver/ internal/apiserver/

# Build
RUN CGO_ENABLED=0 GOOS=\${TARGETOS:-linux} GOARCH=\${TARGETARCH} go build -a -o apiserver cmd/apiserver/main.go

# Use distroless as minimal base image
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/apiserver .
USER 65532:65532

ENTRYPOINT ["/apiserver"]
EOF
    elif [[ "$controller_type" == "checkpoint" ]]; then
        # CheckpointBackup controller needs buildah and other tools
        cat > ${dockerfile_name} <<EOF
# Build the manager binary
FROM golang:1.24-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build with controller-specific configuration
RUN CGO_ENABLED=0 GOOS=\${TARGETOS:-linux} GOARCH=\${TARGETARCH} go build -a -o manager cmd/main.go

# Use Alpine as base image for CheckpointBackup controller to support buildah
FROM alpine:3.19
ARG TARGETOS
ARG TARGETARCH

# Install buildah and required dependencies
RUN apk add --no-cache \\
    buildah \\
    fuse-overlayfs \\
    shadow \\
    ca-certificates \\
    curl

# Create a non-root user for buildah
RUN addgroup -g 65532 -S nonroot && \\
    adduser -u 65532 -S nonroot -G nonroot -h /home/nonroot

# Configure buildah storage
RUN mkdir -p /home/nonroot/.config/containers && \\
    echo '[storage]' > /home/nonroot/.config/containers/storage.conf && \\
    echo 'driver = "overlay"' >> /home/nonroot/.config/containers/storage.conf && \\
    echo 'runroot = "/home/nonroot/.local/share/containers/storage"' >> /home/nonroot/.config/containers/storage.conf && \\
    echo 'graphroot = "/home/nonroot/.local/share/containers/storage"' >> /home/nonroot/.config/containers/storage.conf && \\
    echo '[storage.options.overlay]' >> /home/nonroot/.config/containers/storage.conf && \\
    echo 'mount_program = "/usr/bin/fuse-overlayfs"' >> /home/nonroot/.config/containers/storage.conf && \\
    chown -R nonroot:nonroot /home/nonroot/.config

# Create directories for container storage
RUN mkdir -p /home/nonroot/.local/share/containers/storage && \\
    chown -R nonroot:nonroot /home/nonroot/.local/share

WORKDIR /
COPY --from=builder /workspace/manager .

# Set permissions
RUN chmod +x /manager

USER nonroot

# Set default controller flags for this variant
ENTRYPOINT ["/manager", ${flag_array}]
EOF
    else
        # MigrationBackup and MigrationRestore controllers use distroless (minimal)
        cat > ${dockerfile_name} <<EOF
# Build the manager binary
FROM golang:1.24-alpine AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace

# Install git (needed for go mod download)
RUN apk add --no-cache git

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build with controller-specific configuration
RUN CGO_ENABLED=0 GOOS=\${TARGETOS:-linux} GOARCH=\${TARGETARCH} go build -a -o manager cmd/main.go

# Use distroless as minimal base image for MigrationBackup and MigrationRestore controllers
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

# Set default controller flags for this variant
ENTRYPOINT ["/manager", ${flag_array}]
EOF
    fi

    print_success "Created ${dockerfile_name}"
}

# Function to build and push a controller
build_and_push_controller() {
    local controller_type=$1
    local tag="${CONTROLLERS[$controller_type]}"
    local full_image_name="${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:${tag}"
    local dockerfile_name="Dockerfile.${controller_type}"
    local description="${CONTROLLER_DESCRIPTIONS[$controller_type]}"
    
    print_header "Building ${description}"
    echo "Image: ${full_image_name}"
    echo
    
    # Create controller-specific Dockerfile
    create_controller_dockerfile "$controller_type"
    
    # Build the Docker image
    print_status "Building Docker image: ${full_image_name}"
    if docker build -f "${dockerfile_name}" -t "${full_image_name}" .; then
        print_success "Docker image built successfully"
    else
        print_error "Failed to build Docker image"
        exit 1
    fi
    
    # Test the image locally
    print_status "Testing the image locally..."
    local test_binary="/manager"
    if [[ "$controller_type" == "apiserver" ]]; then
        test_binary="/apiserver"
    fi
    if docker run --rm --entrypoint="" "${full_image_name}" ${test_binary} --help >/dev/null 2>&1; then
        print_success "Image test passed"
    else
        print_warning "Image test failed, but continuing with push"
    fi
    
    # Push to Docker Hub
    print_status "Pushing image to Docker Hub: ${full_image_name}"
    if docker push "${full_image_name}"; then
        print_success "Image pushed successfully to Docker Hub!"
    else
        print_error "Failed to push image to Docker Hub"
        exit 1
    fi
    
    # Clean up temporary Dockerfile
    rm -f "${dockerfile_name}"
    
    # Store image info for summary
    echo "${full_image_name}" >> /tmp/built_images.txt
    
    echo
}

# Function to display build summary
show_summary() {
    echo
    echo "🎉 Build and Push Complete!"
    echo "=========================="
    
    if [[ -f /tmp/built_images.txt ]]; then
        echo "Built and pushed images:"
        while IFS= read -r image; do
            local size=$(docker images --format "table {{.Repository}}:{{.Tag}}\t{{.Size}}" | grep "$image" | awk '{print $2}' || echo "Unknown")
            echo "  ✅ $image (Size: $size)"
        done < /tmp/built_images.txt
        rm -f /tmp/built_images.txt
    fi
    
    echo
    echo "Deployment examples:"
    echo
    echo "For CheckpointBackup controller (DaemonSet):"
    echo "  # Update config/checkpoint-backup/daemonset.yaml with:"
    echo "  image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointBackup_${VERSION}"
    echo
    echo "For MigrationBackup controller (Karmada):"
    echo "  make deploy IMG=${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationBackup_${VERSION}"
    echo
    echo "For MigrationRestore controller (Karmada):"
    echo "  make deploy IMG=${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationRestore_${VERSION}"
    echo
    echo "For Checkpoint API Server:"
    echo "  # Update config/apiserver/deployment.yaml with:"
    echo "  image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointApiserver_${VERSION}"
    echo
}

# Function to create deployment examples
create_deployment_examples() {
    print_status "Creating deployment examples..."
    
    # CheckpointBackup DaemonSet example
    cat > checkpoint-deploy-example.yaml <<EOF
# Example DaemonSet deployment for CheckpointBackup controller
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: checkpoint-backup-controller
  namespace: stateful-migration
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: checkpoint-backup-controller
  template:
    metadata:
      labels:
        app.kubernetes.io/name: checkpoint-backup-controller
    spec:
      serviceAccountName: checkpoint-backup-sa
      hostNetwork: true
      containers:
      - name: controller
        image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointBackup_${VERSION}
        env:
        - name: NODE_NAME
          valueFrom:
            fieldRef:
              fieldPath: spec.nodeName
        # ... other configuration from config/checkpoint-backup/daemonset.yaml
EOF

    # MigrationBackup deployment example
    cat > migration-deploy-example.yaml <<EOF
# Example deployment for MigrationBackup controller
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
        image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationBackup_${VERSION}
        command:
        - /manager
        args:
        - --leader-elect
        - --metrics-bind-address=0.0.0.0:8080
        ports:
        - containerPort: 8080
          name: metrics
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
EOF

    # MigrationRestore deployment example
    cat > restore-deploy-example.yaml <<EOF
# Example deployment for MigrationRestore controller
apiVersion: apps/v1
kind: Deployment
metadata:
  name: migration-restore-controller
  namespace: stateful-migration-operator-system
spec:
  replicas: 1
  selector:
    matchLabels:
      control-plane: restore-controller-manager
  template:
    metadata:
      labels:
        control-plane: restore-controller-manager
    spec:
      containers:
      - name: manager
        image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:migrationRestore_${VERSION}
        command:
        - /manager
        args:
        - --leader-elect
        - --metrics-bind-address=0.0.0.0:8080
        ports:
        - containerPort: 8080
          name: metrics
        resources:
          limits:
            cpu: 500m
            memory: 128Mi
          requests:
            cpu: 10m
            memory: 64Mi
EOF

    # Checkpoint API Server deployment example
    cat > apiserver-deploy-example.yaml <<EOF
# Example deployment for Checkpoint API Server
apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkpoint-apiserver
  namespace: stateful-migration
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: checkpoint-apiserver
  template:
    metadata:
      labels:
        app.kubernetes.io/name: checkpoint-apiserver
    spec:
      serviceAccountName: checkpoint-apiserver-sa
      containers:
      - name: apiserver
        image: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}:checkpointApiserver_${VERSION}
        command:
        - /apiserver
        args:
        - --bind-address=:8090
        ports:
        - containerPort: 8090
          name: http
        livenessProbe:
          httpGet:
            path: /healthz
            port: 8090
          initialDelaySeconds: 10
          periodSeconds: 15
        readinessProbe:
          httpGet:
            path: /readyz
            port: 8090
          initialDelaySeconds: 5
          periodSeconds: 10
        resources:
          limits:
            cpu: 250m
            memory: 256Mi
          requests:
            cpu: 50m
            memory: 64Mi
EOF

    print_success "Created deployment examples: checkpoint-deploy-example.yaml, migration-deploy-example.yaml, restore-deploy-example.yaml, apiserver-deploy-example.yaml"
}

# Main execution
main() {
    local controller_type=${1:-"all"}
    local version=${2:-"v1.16"}
    
    # Update global VERSION variable
    VERSION="$version"
    
    # Update controller tags with the provided version
    CONTROLLERS["checkpoint"]="checkpointBackup_${VERSION}"
    CONTROLLERS["migration"]="migrationBackup_${VERSION}"
    CONTROLLERS["restore"]="migrationRestore_${VERSION}"
    CONTROLLERS["apiserver"]="checkpointApiserver_${VERSION}"
    
    # Initialize build summary
    rm -f /tmp/built_images.txt
    
    # Show header
    echo "🚀 Stateful Migration Operator Build Script"
    echo "============================================="
    echo "Version: ${VERSION}"
    echo "Repository: ${DOCKERHUB_USERNAME}/${REPOSITORY_NAME}"
    echo "Controller Type: ${controller_type}"
    echo
    
    # Validate input
    if [[ "$controller_type" != "all" && "$controller_type" != "checkpoint" && "$controller_type" != "migration" && "$controller_type" != "restore" && "$controller_type" != "apiserver" ]]; then
        print_error "Invalid controller type: $controller_type"
        echo
        show_usage
        exit 1
    fi
    
    # Check prerequisites
    check_prerequisites
    
    # Prepare build environment
    prepare_build
    
    # Build controllers based on selection
    case "$controller_type" in
        "all")
            print_status "Building all controllers and API server..."
            for controller in "checkpoint" "migration" "restore" "apiserver"; do
                build_and_push_controller "$controller"
            done
            ;;
        "checkpoint"|"migration"|"restore"|"apiserver")
            print_status "Building ${controller_type}..."
            build_and_push_controller "$controller_type"
            ;;
    esac
    
    # Create deployment examples
    create_deployment_examples
    
    # Show summary
    show_summary
}

# Show usage if help is requested
if [[ "$1" == "-h" || "$1" == "--help" ]]; then
    show_usage
    exit 0
fi

# Run main function
main "$1" "$2" 