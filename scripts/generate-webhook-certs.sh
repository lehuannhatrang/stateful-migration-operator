#!/bin/bash

# Script to generate TLS certificates for the stateful-migration webhook
# This script can be used when cert-manager is not available

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
NAMESPACE=${NAMESPACE:-stateful-migration}
SERVICE_NAME=${SERVICE_NAME:-stateful-migration-webhook-service}
SECRET_NAME=${SECRET_NAME:-stateful-migration-webhook-certs}
WEBHOOK_NAME=${WEBHOOK_NAME:-stateful-migration-pod-mutator-alt}

echo "Generating TLS certificates for webhook..."
echo "Namespace: $NAMESPACE"
echo "Service: $SERVICE_NAME"
echo "Secret: $SECRET_NAME"

# Create a temporary directory for certificates
CERT_DIR=$(mktemp -d)
echo "Using temporary directory: $CERT_DIR"

# Generate CA private key
openssl genrsa -out "$CERT_DIR/ca.key" 2048

# Generate CA certificate
openssl req -new -x509 -key "$CERT_DIR/ca.key" -out "$CERT_DIR/ca.crt" -days 365 -subj "/CN=stateful-migration-webhook-ca"

# Generate server private key
openssl genrsa -out "$CERT_DIR/tls.key" 2048

# Generate certificate signing request
cat > "$CERT_DIR/csr.conf" <<EOF
[req]
default_bits = 2048
prompt = no
req_extensions = req_ext
distinguished_name = dn

[dn]
CN = $SERVICE_NAME.$NAMESPACE.svc

[req_ext]
subjectAltName = @alt_names

[alt_names]
DNS.1 = $SERVICE_NAME
DNS.2 = $SERVICE_NAME.$NAMESPACE
DNS.3 = $SERVICE_NAME.$NAMESPACE.svc
DNS.4 = $SERVICE_NAME.$NAMESPACE.svc.cluster.local
EOF

openssl req -new -key "$CERT_DIR/tls.key" -out "$CERT_DIR/server.csr" -config "$CERT_DIR/csr.conf"

# Generate server certificate signed by CA
openssl x509 -req -in "$CERT_DIR/server.csr" -CA "$CERT_DIR/ca.crt" -CAkey "$CERT_DIR/ca.key" -CAcreateserial -out "$CERT_DIR/tls.crt" -days 365 -extensions req_ext -extfile "$CERT_DIR/csr.conf"

echo "Certificates generated successfully"

# Create namespace if it doesn't exist
kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# Create secret with certificates
kubectl create secret tls "$SECRET_NAME" \
    --cert="$CERT_DIR/tls.crt" \
    --key="$CERT_DIR/tls.key" \
    --namespace="$NAMESPACE" \
    --dry-run=client -o yaml | kubectl apply -f -

# Get CA bundle and update MutatingWebhookConfiguration
CA_BUNDLE=$(base64 -w 0 < "$CERT_DIR/ca.crt")

echo "Updating MutatingWebhookConfiguration with CA bundle..."

# Create temporary webhook configuration with CA bundle
cat > "$CERT_DIR/webhook-config.yaml" <<EOF
apiVersion: admissionregistration.k8s.io/v1
kind: MutatingWebhookConfiguration
metadata:
  name: $WEBHOOK_NAME
  labels:
    app: stateful-migration-webhook
webhooks:
- name: stateful-migration-pod-mutator.migration.dcnlab.com
  clientConfig:
    service:
      name: $SERVICE_NAME
      namespace: $NAMESPACE
      path: "/mutate-v1-pod"
    caBundle: $CA_BUNDLE
  rules:
  - operations: ["CREATE"]
    apiGroups: [""]
    apiVersions: ["v1"]
    resources: ["pods"]
  admissionReviewVersions: ["v1", "v1beta1"]
  sideEffects: None
  failurePolicy: Ignore
  reinvocationPolicy: Never
  matchPolicy: Equivalent
  namespaceSelector:
    matchExpressions:
    - key: name
      operator: NotIn
      values: ["kube-system", "kube-public", "kube-node-lease", "stateful-migration"]
EOF

kubectl apply -f "$CERT_DIR/webhook-config.yaml"

echo "✅ Webhook certificates generated and installed successfully!"
echo "Secret '$SECRET_NAME' created in namespace '$NAMESPACE'"
echo "MutatingWebhookConfiguration '$WEBHOOK_NAME' updated with CA bundle"

# Cleanup
rm -rf "$CERT_DIR"
echo "Temporary files cleaned up"

echo ""
echo "Next steps:"
echo "1. Deploy the webhook Deployment using: kubectl apply -f config/webhook/deployment.yaml"
echo "2. Verify the webhook is running: kubectl get pods -n $NAMESPACE -l app=stateful-migration-webhook"
echo "3. Check logs: kubectl logs -n $NAMESPACE -l app=stateful-migration-webhook --follow"
