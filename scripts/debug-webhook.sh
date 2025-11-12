#!/bin/bash

# Quick debug script for webhook issues
NAMESPACE=${NAMESPACE:-stateful-migration}

echo "🔍 Webhook Debug Information"
echo "================================"
echo ""

echo "1️⃣  Checking webhook pods..."
kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook
echo ""

echo "2️⃣  Checking pod details..."
kubectl describe pods -n "$NAMESPACE" -l app=stateful-migration-webhook | grep -A 20 "Events:"
echo ""

echo "3️⃣  Checking service endpoints..."
kubectl get endpoints stateful-migration-webhook-service -n "$NAMESPACE"
echo ""

echo "4️⃣  Checking webhook logs (last 20 lines)..."
kubectl logs -n "$NAMESPACE" -l app=stateful-migration-webhook --tail=20
echo ""

echo "5️⃣  Checking if TLS secret exists..."
if kubectl get secret stateful-migration-webhook-certs -n "$NAMESPACE" >/dev/null 2>&1; then
    echo "✅ TLS secret exists"
    kubectl get secret stateful-migration-webhook-certs -n "$NAMESPACE"
else
    echo "❌ TLS secret NOT found!"
    echo "   Run: ./scripts/generate-webhook-certs.sh"
fi
echo ""

echo "6️⃣  Checking webhook configuration..."
kubectl get mutatingwebhookconfiguration stateful-migration-pod-mutator-alt -o yaml | grep -A 5 "caBundle:"
echo ""

echo "💡 Quick Fixes:"
echo "  - If pods are not running: check 'kubectl describe pods' output above"
echo "  - If TLS secret missing: run ./scripts/generate-webhook-certs.sh"
echo "  - If image pull errors: check imagePullPolicy in deployment.yaml"
echo "  - If connection refused: pods might still be starting, wait a moment"

