#!/bin/bash

# Script to test the stateful-migration admission webhook
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
NAMESPACE=${NAMESPACE:-default}
WEBHOOK_NAMESPACE=${WEBHOOK_NAMESPACE:-stateful-migration}

echo "🧪 Testing Stateful Migration Admission Webhook"
echo "Test namespace: $NAMESPACE"
echo "Webhook namespace: $WEBHOOK_NAMESPACE"
echo ""

# Function to cleanup test resources
cleanup() {
    echo "🧹 Cleaning up test resources..."
    kubectl delete job test-migration-job -n "$NAMESPACE" --ignore-not-found=true
    kubectl delete checkpointbackup test-checkpoint-backup -n "$NAMESPACE" --ignore-not-found=true
    echo "✅ Cleanup completed"
}

# Trap cleanup on exit
trap cleanup EXIT

# Check if webhook is running
echo "🔍 Checking webhook status..."
WEBHOOK_PODS=$(kubectl get pods -n "$WEBHOOK_NAMESPACE" -l app=stateful-migration-webhook --field-selector=status.phase=Running --no-headers | wc -l)
if [ "$WEBHOOK_PODS" -eq 0 ]; then
    echo "❌ No webhook pods are running in namespace '$WEBHOOK_NAMESPACE'"
    echo "Please deploy the webhook first using: ./scripts/deploy-webhook.sh"
    exit 1
fi
echo "✅ Found $WEBHOOK_PODS running webhook pod(s)"

# Check if MutatingAdmissionWebhookConfiguration exists
echo "🔍 Checking webhook configuration..."
if ! kubectl get mutatingwebhookconfiguration stateful-migration-pod-mutator-alt >/dev/null 2>&1; then
    echo "❌ MutatingWebhookConfiguration 'stateful-migration-pod-mutator-alt' not found"
    echo "Please deploy the webhook configuration first"
    exit 1
fi
echo "✅ MutatingWebhookConfiguration is registered"

echo ""
echo "🚀 Creating test resources..."

# Create CheckpointBackup CR
echo "📦 Creating CheckpointBackup CR..."
cat <<EOF | kubectl apply -f -
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: test-checkpoint-backup
  namespace: $NAMESPACE
spec:
  schedule: "immediately"
  podRef:
    name: test-app-pod
    namespace: $NAMESPACE
  resourceRef:
    apiVersion: batch/v1
    kind: Job
    name: test-migration-job
    namespace: $NAMESPACE
  containers:
  - name: test-container
    image: nginx:1.20-checkpoint
EOF

echo "✅ CheckpointBackup CR created"

# Wait a moment for the CR to be available
sleep 2

# Create Job that should trigger webhook
echo "🎯 Creating Job that should trigger webhook..."
cat <<EOF | kubectl apply -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: test-migration-job
  namespace: $NAMESPACE
  labels:
    test: webhook-mutation
spec:
  template:
    spec:
      containers:
      - name: test-container
        image: nginx:1.19
        command: ["echo", "Testing webhook mutation"]
      restartPolicy: Never
  backoffLimit: 1
EOF

echo "✅ Job created"

# Wait for pod to be created
echo "⏳ Waiting for pod to be created..."
sleep 5

# Check if pod was created
POD_NAME=$(kubectl get pods -n "$NAMESPACE" -l job-name=test-migration-job --no-headers -o name | head -1)
if [ -z "$POD_NAME" ]; then
    echo "❌ No pod found for job 'test-migration-job'"
    exit 1
fi

POD_NAME=$(echo "$POD_NAME" | cut -d'/' -f2)
echo "✅ Pod created: $POD_NAME"

# Check if image was patched
echo "🔍 Checking if image was patched by webhook..."
ACTUAL_IMAGE=$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.containers[0].image}')
EXPECTED_IMAGE="nginx:1.20-checkpoint"

echo "Expected image: $EXPECTED_IMAGE"
echo "Actual image:   $ACTUAL_IMAGE"

if [ "$ACTUAL_IMAGE" = "$EXPECTED_IMAGE" ]; then
    echo "✅ SUCCESS: Webhook successfully patched the image!"
    echo "   Original: nginx:1.19"
    echo "   Patched:  $ACTUAL_IMAGE"
else
    echo "❌ FAILURE: Webhook did not patch the image"
    echo "   Expected: $EXPECTED_IMAGE"
    echo "   Got:      $ACTUAL_IMAGE"
    
    # Show webhook logs for debugging
    echo ""
    echo "🔍 Recent webhook logs:"
    kubectl logs -n "$WEBHOOK_NAMESPACE" -l app=stateful-migration-webhook --tail=20
    
    exit 1
fi

# Check webhook logs for mutation entry
echo ""
echo "🔍 Checking webhook logs for mutation activity..."
if kubectl logs -n "$WEBHOOK_NAMESPACE" -l app=stateful-migration-webhook --tail=10 | grep -q "Processing pod mutation"; then
    echo "✅ Found webhook mutation activity in logs"
else
    echo "⚠️  No mutation activity found in recent logs"
fi

echo ""
echo "🎉 Webhook test completed successfully!"
echo ""
echo "📋 Test Summary:"
echo "- Webhook intercepted pod creation: ✅"
echo "- Found matching CheckpointBackup CR: ✅"
echo "- Applied image patch correctly: ✅"
echo "- Pod created with patched image: ✅"
echo ""
echo "🔍 To view detailed logs:"
echo "kubectl logs -n $WEBHOOK_NAMESPACE -l app=stateful-migration-webhook --follow"
