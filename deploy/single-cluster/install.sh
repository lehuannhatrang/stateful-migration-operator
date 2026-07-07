#!/bin/bash

# Single-cluster installer for the CheckpointBackup Controller.
#
# Installs the CheckpointBackup controller onto a single Kubernetes cluster with
# no management cluster and no Karmada. It is a thin wrapper around the
# config/checkpoint-backup-single-cluster kustomize overlay: it renders the
# overlay, applies optional image / storage overrides, applies the manifests,
# and (optionally) creates the registry-credentials secret.

set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_status()  { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

# Resolve paths from the script location so the caller's CWD does not matter.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OVERLAY_DIR="$REPO_ROOT/config/checkpoint-backup-single-cluster"

# Defaults
NAMESPACE="stateful-migration"
DEFAULT_IMAGE_NAME="docker.io/lehuannhatrang/stateful-migration-operator"
CRD_NAME="checkpointbackups.migration.dcnlab.com"
DAEMONSET_NAME="checkpoint-backup-controller"

IMAGE=""
STORAGE_CLASS=""
STORAGE_SIZE=""
REGISTRY_USERNAME=""
REGISTRY_PASSWORD=""
REGISTRY_URL=""
KUBE_CONTEXT=""
KUBECONFIG_PATH=""
DRY_RUN=false

show_usage() {
    cat <<EOF
Single-cluster CheckpointBackup Controller installer
====================================================

Usage: $0 [options]

Options:
  -i, --image IMAGE           Controller image (repo:tag). Overrides the overlay default
                              ($DEFAULT_IMAGE_NAME:<tag>).
      --storage-class NAME    StorageClass for the checkpoint-storage PVC (must support
                              ReadWriteMany). Defaults to the cluster's default StorageClass.
      --storage-size SIZE     Size of the checkpoint-storage PVC (default from overlay: 50Gi).
  -u, --registry-user USER    Registry username. When set, a 'registry-credentials' secret
                              is created in the $NAMESPACE namespace.
  -w, --registry-pass PASS    Registry password (required if --registry-user is set).
  -r, --registry-url URL      Registry URL (optional; stored in the secret).
  -c, --context NAME          kubectl context to target.
  -k, --kubeconfig PATH       Path to a kubeconfig file.
  -d, --dry-run               Render and show what would be applied without changing the cluster.
  -h, --help                  Show this help message.

Examples:
  # Minimal install using the overlay's default image
  $0

  # Pin a specific image and target a context
  $0 --image myrepo/stateful-migration-operator:checkpointBackup_v2.16 --context my-cluster

  # Provide registry credentials and an RWX storage class
  $0 -u myuser -w 'mypassword' -r myregistry.com --storage-class rook-cephfs

  # Preview only
  $0 --dry-run
EOF
}

parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -i|--image)          IMAGE="$2"; shift 2 ;;
            --storage-class)     STORAGE_CLASS="$2"; shift 2 ;;
            --storage-size)      STORAGE_SIZE="$2"; shift 2 ;;
            -u|--registry-user)  REGISTRY_USERNAME="$2"; shift 2 ;;
            -w|--registry-pass)  REGISTRY_PASSWORD="$2"; shift 2 ;;
            -r|--registry-url)   REGISTRY_URL="$2"; shift 2 ;;
            -c|--context)        KUBE_CONTEXT="$2"; shift 2 ;;
            -k|--kubeconfig)     KUBECONFIG_PATH="$2"; shift 2 ;;
            -d|--dry-run)        DRY_RUN=true; shift ;;
            -h|--help)           show_usage; exit 0 ;;
            *) print_error "Unknown option: $1"; echo; show_usage; exit 1 ;;
        esac
    done
}

# Build the common kubectl argument array (context / kubeconfig).
KUBECTL_ARGS=()
build_kubectl_args() {
    if [[ -n "$KUBE_CONTEXT" ]]; then
        KUBECTL_ARGS+=(--context "$KUBE_CONTEXT")
    fi
    if [[ -n "$KUBECONFIG_PATH" ]]; then
        KUBECTL_ARGS+=(--kubeconfig "$KUBECONFIG_PATH")
    fi
}

validate_prerequisites() {
    if ! command -v kubectl &>/dev/null; then
        print_error "kubectl is not installed or not in PATH"
        exit 1
    fi
    if [[ ! -d "$OVERLAY_DIR" ]]; then
        print_error "Kustomize overlay not found: $OVERLAY_DIR"
        exit 1
    fi
    if [[ -n "$REGISTRY_USERNAME" && -z "$REGISTRY_PASSWORD" ]]; then
        print_error "--registry-pass is required when --registry-user is set"
        exit 1
    fi
    # A dry run only renders manifests locally, so it needs no cluster access.
    if [[ "$DRY_RUN" == true ]]; then
        return 0
    fi
    print_status "Checking cluster connectivity..."
    if ! kubectl "${KUBECTL_ARGS[@]}" cluster-info &>/dev/null; then
        print_error "Cannot connect to the target cluster. Check your kubeconfig/context."
        exit 1
    fi
    print_success "Connected to cluster: $(kubectl "${KUBECTL_ARGS[@]}" config current-context 2>/dev/null || echo "unknown")"
}

# Render the overlay, layering optional image and PVC overrides on top.
# Writes the final manifest to the file named by $1.
render_manifests() {
    local out_file="$1"
    local tmp_dir
    tmp_dir="$(mktemp -d)"

    print_status "Rendering kustomize overlay..."
    kubectl kustomize --load-restrictor LoadRestrictionsNone "$OVERLAY_DIR" > "$tmp_dir/manifest.yaml"

    # If no overrides are requested, use the base render as-is.
    if [[ -z "$IMAGE" && -z "$STORAGE_CLASS" && -z "$STORAGE_SIZE" ]]; then
        cp "$tmp_dir/manifest.yaml" "$out_file"
        rm -rf "$tmp_dir"
        return
    fi

    # Second, local kustomize pass to apply overrides (manifest.yaml lives inside
    # tmp_dir, so no load-restrictor relaxation is needed here).
    {
        echo "apiVersion: kustomize.config.k8s.io/v1beta1"
        echo "kind: Kustomization"
        echo "resources:"
        echo "- manifest.yaml"

        if [[ -n "$IMAGE" ]]; then
            local new_name new_tag
            if [[ "$IMAGE" == *:* && "${IMAGE##*:}" != */* ]]; then
                new_name="${IMAGE%:*}"
                new_tag="${IMAGE##*:}"
            else
                new_name="$IMAGE"
                new_tag="latest"
            fi
            echo "images:"
            echo "- name: $DEFAULT_IMAGE_NAME"
            echo "  newName: $new_name"
            echo "  newTag: $new_tag"
        fi

        if [[ -n "$STORAGE_CLASS" || -n "$STORAGE_SIZE" ]]; then
            echo "patches:"
            echo "- target:"
            echo "    kind: PersistentVolumeClaim"
            echo "    name: checkpoint-storage"
            echo "  patch: |-"
            [[ -n "$STORAGE_SIZE" ]] && {
                echo "    - op: replace"
                echo "      path: /spec/resources/requests/storage"
                echo "      value: \"$STORAGE_SIZE\""
            }
            [[ -n "$STORAGE_CLASS" ]] && {
                echo "    - op: add"
                echo "      path: /spec/storageClassName"
                echo "      value: \"$STORAGE_CLASS\""
            }
        fi
    } > "$tmp_dir/kustomization.yaml"

    kubectl kustomize "$tmp_dir" > "$out_file"
    rm -rf "$tmp_dir"
}

create_registry_secret() {
    [[ -z "$REGISTRY_USERNAME" ]] && return 0

    print_status "Configuring registry-credentials secret in namespace '$NAMESPACE'..."
    local secret_args=(
        create secret generic registry-credentials
        -n "$NAMESPACE"
        --from-literal=username="$REGISTRY_USERNAME"
        --from-literal=password="$REGISTRY_PASSWORD"
    )
    [[ -n "$REGISTRY_URL" ]] && secret_args+=(--from-literal=registry="$REGISTRY_URL")

    if [[ "$DRY_RUN" == true ]]; then
        kubectl "${KUBECTL_ARGS[@]}" "${secret_args[@]}" --dry-run=client -o yaml
    else
        # Idempotent create-or-update.
        kubectl "${KUBECTL_ARGS[@]}" "${secret_args[@]}" --dry-run=client -o yaml \
            | kubectl "${KUBECTL_ARGS[@]}" apply -f -
        print_success "registry-credentials secret applied"
    fi
}

main() {
    parse_arguments "$@"
    build_kubectl_args
    validate_prerequisites

    echo
    print_status "Single-cluster CheckpointBackup Controller install"
    echo "  Overlay:     $OVERLAY_DIR"
    echo "  Namespace:   $NAMESPACE"
    echo "  Image:       ${IMAGE:-<overlay default>}"
    echo "  StorageClass:${STORAGE_CLASS:+ $STORAGE_CLASS}${STORAGE_CLASS:-  <cluster default>}"
    echo "  Dry run:     $DRY_RUN"
    echo

    print_warning "The checkpoint-storage PVC requests ReadWriteMany (RWX). Ensure the target"
    print_warning "StorageClass supports RWX, or pass --storage-class with an RWX-capable class."

    local manifest_file
    manifest_file="$(mktemp)"
    # manifest_file is local to main(); guard the reference so the trap does not
    # trip 'set -u' when it runs at global scope on exit.
    trap 'rm -f "${manifest_file:-}"' EXIT
    render_manifests "$manifest_file"

    if [[ "$DRY_RUN" == true ]]; then
        print_status "Dry run: manifests that would be applied"
        echo "----------------------------------------"
        cat "$manifest_file"
        echo "----------------------------------------"
        create_registry_secret
        print_success "Dry run complete. Nothing was applied to the cluster."
        return 0
    fi

    print_status "Applying manifests..."
    kubectl "${KUBECTL_ARGS[@]}" apply -f "$manifest_file"

    create_registry_secret

    print_status "Waiting for the CheckpointBackup CRD to be established..."
    kubectl "${KUBECTL_ARGS[@]}" wait --for=condition=established "crd/$CRD_NAME" --timeout=60s || \
        print_warning "CRD did not report Established in time; continuing."

    print_status "Waiting for the DaemonSet rollout..."
    if kubectl "${KUBECTL_ARGS[@]}" rollout status "daemonset/$DAEMONSET_NAME" -n "$NAMESPACE" --timeout=180s; then
        print_success "DaemonSet is rolled out"
    else
        print_warning "DaemonSet did not become ready in time."
        print_status "Inspect with: kubectl -n $NAMESPACE describe daemonset/$DAEMONSET_NAME"
    fi

    echo
    print_success "CheckpointBackup Controller installed on the target cluster."
    echo
    echo "Useful commands:"
    echo "  kubectl -n $NAMESPACE get pods -o wide"
    echo "  kubectl -n $NAMESPACE logs -l app.kubernetes.io/name=checkpoint-backup-controller -f"
    echo "  kubectl get checkpointbackups -A"
}

main "$@"
