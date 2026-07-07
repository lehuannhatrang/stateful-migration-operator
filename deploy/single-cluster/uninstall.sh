#!/bin/bash

# Single-cluster uninstaller for the CheckpointBackup Controller.
#
# Removes the resources created by install.sh. By default it deletes the
# DaemonSet, RBAC, ServiceAccount, and PVC but keeps the CheckpointBackup CRD
# and the namespace so that existing CheckpointBackup resources are preserved.
# Pass --purge to also remove the CRD (and any CheckpointBackup resources) and
# the namespace.

set -euo pipefail

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

print_status()  { echo -e "${BLUE}[INFO]${NC} $1"; }
print_success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
print_warning() { echo -e "${YELLOW}[WARNING]${NC} $1"; }
print_error()   { echo -e "${RED}[ERROR]${NC} $1"; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
OVERLAY_DIR="$REPO_ROOT/config/checkpoint-backup-single-cluster"

NAMESPACE="stateful-migration"
CRD_NAME="checkpointbackups.migration.dcnlab.com"

PURGE=false
KUBE_CONTEXT=""
KUBECONFIG_PATH=""

show_usage() {
    cat <<EOF
Single-cluster CheckpointBackup Controller uninstaller
======================================================

Usage: $0 [options]

Options:
      --purge             Also delete the CheckpointBackup CRD (and its resources)
                          and the $NAMESPACE namespace.
  -c, --context NAME      kubectl context to target.
  -k, --kubeconfig PATH   Path to a kubeconfig file.
  -h, --help              Show this help message.
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --purge)          PURGE=true; shift ;;
        -c|--context)     KUBE_CONTEXT="$2"; shift 2 ;;
        -k|--kubeconfig)  KUBECONFIG_PATH="$2"; shift 2 ;;
        -h|--help)        show_usage; exit 0 ;;
        *) print_error "Unknown option: $1"; echo; show_usage; exit 1 ;;
    esac
done

KUBECTL_ARGS=()
[[ -n "$KUBE_CONTEXT" ]] && KUBECTL_ARGS+=(--context "$KUBE_CONTEXT")
[[ -n "$KUBECONFIG_PATH" ]] && KUBECTL_ARGS+=(--kubeconfig "$KUBECONFIG_PATH")

if ! command -v kubectl &>/dev/null; then
    print_error "kubectl is not installed or not in PATH"
    exit 1
fi

TMP_MANIFEST="$(mktemp)"
trap 'rm -f "$TMP_MANIFEST"' EXIT
kubectl kustomize --load-restrictor LoadRestrictionsNone "$OVERLAY_DIR" > "$TMP_MANIFEST"

if [[ "$PURGE" == true ]]; then
    print_warning "Purge mode: deleting all rendered resources including the CRD and namespace."
    kubectl "${KUBECTL_ARGS[@]}" delete -f "$TMP_MANIFEST" --ignore-not-found=true
    kubectl "${KUBECTL_ARGS[@]}" delete namespace "$NAMESPACE" --ignore-not-found=true
    print_success "Purge complete."
else
    print_status "Deleting controller resources (CRD and namespace are kept)..."
    # Delete the controller resources explicitly, leaving the CRD and namespace
    # (and any existing CheckpointBackup resources) untouched.
    kubectl "${KUBECTL_ARGS[@]}" -n "$NAMESPACE" delete daemonset checkpoint-backup-controller --ignore-not-found=true
    kubectl "${KUBECTL_ARGS[@]}" -n "$NAMESPACE" delete pvc checkpoint-storage --ignore-not-found=true
    kubectl "${KUBECTL_ARGS[@]}" -n "$NAMESPACE" delete serviceaccount checkpoint-backup-sa --ignore-not-found=true
    kubectl "${KUBECTL_ARGS[@]}" delete clusterrole checkpoint-backup-role --ignore-not-found=true
    kubectl "${KUBECTL_ARGS[@]}" delete clusterrolebinding checkpoint-backup-rolebinding --ignore-not-found=true
    print_success "Controller resources removed. CRD '$CRD_NAME' and namespace '$NAMESPACE' kept."
    print_status "Run with --purge to remove those as well."
fi
