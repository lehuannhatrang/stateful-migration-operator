#!/bin/bash

# Script to deploy the stateful-migration admission webhook
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
NAMESPACE=${NAMESPACE:-stateful-migration}
IMAGE_TAG=${IMAGE_TAG:-latest}

echo "🚀 Deploying Stateful Migration Admission Webhook"
echo "Namespace: $NAMESPACE" 
echo "Image Tag: $IMAGE_TAG"
echo ""

# Function to check if a command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Check prerequisites
echo "🔍 Checking prerequisites..."
if ! command_exists kubectl; then
    echo "❌ kubectl is required but not installed"
    exit 1
fi

# Check if admission controllers are enabled
echo "🔍 Checking if admission controllers are enabled..."
if kubectl api-resources | grep -q "mutatingwebhookconfigurations"; then
    echo "✅ MutatingWebhookConfiguration API is available"
else
    echo "❌ MutatingWebhookConfiguration API is not available"
    echo "   Please ensure your Kubernetes cluster has admission controllers enabled"
    echo "   and supports the admissionregistration.k8s.io/v1 API"
    exit 1
fi

echo "✅ Prerequisites satisfied"
echo ""

# Create namespace
echo "📦 Creating namespace..."
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
echo "✅ Namespace '$NAMESPACE' ready"
echo ""

# Generate and install certificates
echo "🔐 Generating TLS certificates..."
"$SCRIPT_DIR/generate-webhook-certs.sh"
echo "✅ Certificates installed"
echo ""

# Deploy RBAC
echo "👤 Deploying RBAC resources..."
kubectl apply -f "$PROJECT_ROOT/config/webhook/rbac.yaml"
echo "✅ RBAC resources deployed"
echo ""

# Deploy Deployment
echo "🚢 Deploying webhook Deployment..."
# Update image tag in deployment
sed "s|docker.io/lehuannhatrang/stateful-migration-webhook:v1.0|docker.io/lehuannhatrang/stateful-migration-webhook:$IMAGE_TAG|g" \
    "$PROJECT_ROOT/config/webhook/deployment.yaml" | kubectl apply -f -
echo "✅ Deployment deployed"
echo ""

# Wait for webhook to be ready
echo "⏳ Waiting for webhook pods to be ready..."
echo "🔍 Checking Deployment status..."
kubectl get deployment stateful-migration-webhook -n "$NAMESPACE"

echo ""
echo "🔍 Checking for pods..."
kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook

echo ""
echo "🔍 Checking all pods in namespace..."
kubectl get pods -n "$NAMESPACE"

# Check if any pods exist before waiting
POD_COUNT=$(kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook --no-headers 2>/dev/null | wc -l)
if [ "$POD_COUNT" -eq 0 ]; then
    echo "❌ No webhook pods found. Checking Deployment events..."
    kubectl describe deployment stateful-migration-webhook -n "$NAMESPACE"
    echo ""
    echo "📋 Recent events in namespace:"
    kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' | tail -10
    echo ""
    echo "⚠️  Pods may still be starting. Let's wait a bit longer..."
    sleep 10
    kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook
fi

# Try to wait for pods if they exist
if kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook --no-headers 2>/dev/null | grep -q .; then
    echo "✅ Found webhook pods, waiting for them to be ready..."
    kubectl wait --for=condition=ready pod -l app=stateful-migration-webhook -n "$NAMESPACE" --timeout=120s
    echo "✅ Webhook pods are ready"
else
    echo "⚠️  No webhook pods found yet. This might indicate an issue with the Deployment."
    echo "    Let's continue and check the final status..."
fi
echo ""

# Verify deployment
echo "🔍 Verifying deployment..."
WEBHOOK_PODS=$(kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook --no-headers | wc -l)
READY_PODS=$(kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook --field-selector=status.phase=Running --no-headers | wc -l)

echo "Webhook pods: $WEBHOOK_PODS"
echo "Ready pods: $READY_PODS"

if [ "$WEBHOOK_PODS" -eq "$READY_PODS" ] && [ "$WEBHOOK_PODS" -gt 0 ]; then
    echo "✅ All webhook pods are running"
else
    echo "⚠️  Some webhook pods may not be ready"
    kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook
fi

# Check MutatingWebhookConfiguration
echo ""
echo "🔗 Checking MutatingWebhookConfiguration..."
if kubectl get mutatingwebhookconfiguration stateful-migration-pod-mutator-alt >/dev/null 2>&1; then
    echo "✅ MutatingWebhookConfiguration is registered"
else
    echo "❌ MutatingWebhookConfiguration is not registered"
    exit 1
fi

echo ""
echo "🎉 Webhook deployment completed successfully!"
echo ""
echo "📋 Summary:"
echo "- Namespace: $NAMESPACE"
echo "- Webhook Image: stateful-migration-webhook:$IMAGE_TAG"
echo "- Webhook Pods: $WEBHOOK_PODS"
echo "- Service: stateful-migration-webhook-service"
echo "- MutatingWebhookConfiguration: stateful-migration-pod-mutator-alt"
echo ""
echo "📖 Next steps:"
echo "1. Test the webhook by creating a pod from a Job that matches a CheckpointBackup resourceRef"
echo "2. Monitor logs: kubectl logs -n $NAMESPACE -l app=stateful-migration-webhook --follow"
echo "3. Check webhook metrics: kubectl port-forward -n $NAMESPACE svc/stateful-migration-webhook-service 8080:8080"
echo ""

# Optional: Show current CheckpointBackup CRs
CHECKPOINT_BACKUPS=$(kubectl get checkpointbackups --all-namespaces --no-headers 2>/dev/null | wc -l || echo "0")
if [ "$CHECKPOINT_BACKUPS" -gt 0 ]; then
    echo "📊 Current CheckpointBackup CRs:"
    kubectl get checkpointbackups --all-namespaces
else
    echo "📊 No CheckpointBackup CRs found. Create some to test the webhook."
fi
