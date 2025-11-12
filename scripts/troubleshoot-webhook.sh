#!/bin/bash

# Script to troubleshoot webhook deployment issues
set -e

NAMESPACE=${NAMESPACE:-stateful-migration}

echo "🔍 Troubleshooting Stateful Migration Webhook"
echo "Namespace: $NAMESPACE"
echo ""

# Check if namespace exists
echo "1️⃣  Checking namespace..."
if kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
    echo "✅ Namespace '$NAMESPACE' exists"
else
    echo "❌ Namespace '$NAMESPACE' does not exist"
    exit 1
fi

# Check Deployment
echo ""
echo "2️⃣  Checking Deployment..."
if kubectl get deployment stateful-migration-webhook -n "$NAMESPACE" >/dev/null 2>&1; then
    echo "✅ Deployment exists"
    kubectl get deployment stateful-migration-webhook -n "$NAMESPACE"
    echo ""
    echo "📋 Deployment details:"
    kubectl describe deployment stateful-migration-webhook -n "$NAMESPACE"
else
    echo "❌ Deployment does not exist"
    exit 1
fi

# Check pods
echo ""
echo "3️⃣  Checking pods..."
PODS=$(kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook --no-headers 2>/dev/null || true)
if [ -z "$PODS" ]; then
    echo "❌ No pods found with label app=stateful-migration-webhook"
    echo ""
    echo "🔍 All pods in namespace:"
    kubectl get pods -n "$NAMESPACE"
else
    echo "✅ Found webhook pods:"
    kubectl get pods -n "$NAMESPACE" -l app=stateful-migration-webhook
    echo ""
    echo "📋 Pod details:"
    kubectl describe pods -n "$NAMESPACE" -l app=stateful-migration-webhook
fi

# Check recent events
echo ""
echo "4️⃣  Checking recent events..."
kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' | tail -15

# Check nodes
echo ""
echo "5️⃣  Checking node status..."
kubectl get nodes -o wide

# Check image availability (attempt to describe the image)
echo ""
echo "6️⃣  Checking if webhook image exists..."
IMAGE=$(kubectl get deployment stateful-migration-webhook -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "unknown")
echo "Image: $IMAGE"

if [ "$IMAGE" != "unknown" ]; then
    # Try to pull image information
    echo "🔍 Checking image pull status from pod events..."
    kubectl get events -n "$NAMESPACE" --field-selector reason=Failed,reason=FailedMount,reason=ErrImagePull,reason=ImagePullBackOff 2>/dev/null || echo "No image pull failures found"
fi

# Check Service Account and RBAC
echo ""
echo "7️⃣  Checking ServiceAccount and RBAC..."
if kubectl get serviceaccount stateful-migration-webhook -n "$NAMESPACE" >/dev/null 2>&1; then
    echo "✅ ServiceAccount exists"
else
    echo "❌ ServiceAccount does not exist"
fi

if kubectl get clusterrole stateful-migration-webhook-manager-role >/dev/null 2>&1; then
    echo "✅ ClusterRole exists"
else
    echo "❌ ClusterRole does not exist"
fi

if kubectl get clusterrolebinding stateful-migration-webhook-manager-rolebinding >/dev/null 2>&1; then
    echo "✅ ClusterRoleBinding exists"
else
    echo "❌ ClusterRoleBinding does not exist"
fi

# Check webhook configuration
echo ""
echo "8️⃣  Checking webhook configuration..."
if kubectl get mutatingwebhookconfiguration stateful-migration-pod-mutator-alt >/dev/null 2>&1; then
    echo "✅ MutatingWebhookConfiguration exists"
    kubectl get mutatingwebhookconfiguration stateful-migration-pod-mutator-alt -o yaml
else
    echo "❌ MutatingWebhookConfiguration does not exist"
fi

echo ""
echo "🎯 Summary and Recommendations:"
echo ""

# Provide recommendations based on findings
if [ -z "$PODS" ]; then
    echo "❌ No webhook pods found. Common causes:"
    echo "   1. Image pull issues - check if the webhook image exists and is accessible"
    echo "   2. Resource constraints - cluster might not have enough resources"
    echo "   3. Node selector issues - Deployment might not match any nodes"
    echo "   4. Scheduling issues - pods might be rejected by nodes"
    echo ""
    echo "🔧 Try these commands to diagnose:"
    echo "   kubectl describe deployment stateful-migration-webhook -n $NAMESPACE"
    echo "   kubectl get events -n $NAMESPACE --sort-by='.lastTimestamp'"
    echo "   kubectl get nodes -o wide"
else
    echo "✅ Webhook pods found. Check their status above for any issues."
fi

echo ""
echo "🔧 Manual check commands:"
echo "kubectl get all -n $NAMESPACE"
echo "kubectl logs -n $NAMESPACE -l app=stateful-migration-webhook"
echo "kubectl describe pods -n $NAMESPACE -l app=stateful-migration-webhook"
