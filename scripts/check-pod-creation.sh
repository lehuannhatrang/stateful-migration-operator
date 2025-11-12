#!/bin/bash

# Script to diagnose why pods aren't being created after webhook mutation
NAMESPACE=${1:-demo-preemption}
JOB_NAME=${2:-lpj-2}

echo "🔍 Diagnosing Pod Creation Issues"
echo "Namespace: $NAMESPACE"
echo "Job: $JOB_NAME"
echo "================================"
echo ""

# Check Job status
echo "1️⃣  Checking Job status..."
kubectl get job "$JOB_NAME" -n "$NAMESPACE" 2>/dev/null || echo "Job not found"
echo ""

# Check Job details
echo "2️⃣  Job details and conditions..."
kubectl describe job "$JOB_NAME" -n "$NAMESPACE" 2>/dev/null | grep -A 10 "Conditions:"
echo ""

# Check for pods from this Job
echo "3️⃣  Checking pods for this Job..."
kubectl get pods -n "$NAMESPACE" -l job-name="$JOB_NAME"
echo ""

# Check pod events
echo "4️⃣  Recent pod events..."
kubectl get pods -n "$NAMESPACE" -l job-name="$JOB_NAME" --no-headers 2>/dev/null | while read pod rest; do
    echo "Events for pod: $pod"
    kubectl describe pod "$pod" -n "$NAMESPACE" | grep -A 20 "Events:"
    echo ""
done

# If no pods exist, check for failed pod creation events
if [ $(kubectl get pods -n "$NAMESPACE" -l job-name="$JOB_NAME" --no-headers 2>/dev/null | wc -l) -eq 0 ]; then
    echo "⚠️  No pods found for Job $JOB_NAME"
    echo ""
    echo "5️⃣  Checking namespace events for failed pod creation..."
    kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' | tail -20
fi

# Check if the patched image exists
echo ""
echo "6️⃣  Checking if patched image exists locally..."
PATCHED_IMAGE=$(kubectl logs -n stateful-migration -l app=stateful-migration-webhook --tail=50 | grep "newImage" | tail -1 | sed 's/.*newImage":"\([^"]*\)".*/\1/')
if [ -n "$PATCHED_IMAGE" ]; then
    echo "Patched image: $PATCHED_IMAGE"
    echo ""
    echo "Checking if image exists on nodes..."
    # This is a best-effort check
    kubectl get nodes -o name | while read node; do
        node_name=$(echo $node | cut -d'/' -f2)
        echo "  Node: $node_name"
    done
else
    echo "Could not determine patched image from webhook logs"
fi

echo ""
echo "7️⃣  Checking webhook logs for any errors..."
kubectl logs -n stateful-migration -l app=stateful-migration-webhook --tail=30 | grep -i "error\|failed\|deny"

echo ""
echo "💡 Common Issues:"
echo "  1. Patched image doesn't exist - check if 'checkpoint/preemption:lpj-2-gv555' exists"
echo "  2. ImagePullBackOff - the mutated image can't be pulled"
echo "  3. Resource constraints - insufficient resources on nodes"
echo "  4. Webhook returning error response - check webhook logs"
echo "  5. Admission controller rejecting - check other admission webhooks"

