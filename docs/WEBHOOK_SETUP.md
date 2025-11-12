# Stateful Migration Admission Webhook

The Stateful Migration Admission Webhook is a Kubernetes mutating admission webhook that automatically patches container images in pods created by Jobs when they match CheckpointBackup Custom Resources.

## Overview

The webhook intercepts pod creation events and:

1. **Filters pods**: Only processes pods created by Kubernetes Jobs
2. **Matches resources**: Finds CheckpointBackup CRs whose `resourceRef` matches the Job
3. **Patches images**: Replaces container images with checkpoint/restored images from the CheckpointBackup
4. **Fallback mechanism**: Uses images from `spec.containers` first, then falls back to `status.builtImages`

## Architecture

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Kubernetes    │    │   Admission     │    │  CheckpointBackup │
│   API Server    │───▶│   Webhook       │───▶│      CRD        │
│                 │    │  (Deployment)   │    │                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         ▼                       ▼                       ▼
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Pod Creation  │    │  Image Patching │    │  Container      │
│   (from Job)    │    │     Logic       │    │  Configurations │
└─────────────────┘    └─────────────────┘    └─────────────────┘
```

## Deployment

### Prerequisites

- Kubernetes cluster with admission webhooks enabled
- `kubectl` CLI tool
- Docker for building the webhook image
- OpenSSL for certificate generation (if not using cert-manager)

### Quick Deploy

```bash
# Deploy the webhook with default settings
./scripts/deploy-webhook.sh
```

### Manual Deployment

1. **Build the webhook image**:
   ```bash
   docker build -f Dockerfile.webhook -t docker.io/lehuannhatrang/stateful-migration-webhook:v1.0 .
   ```

2. **Generate TLS certificates**:
   ```bash
   ./scripts/generate-webhook-certs.sh
   ```

3. **Deploy the components**:
   ```bash
   kubectl apply -f config/webhook/rbac.yaml
   kubectl apply -f config/webhook/deployment.yaml
   ```

### Using Cert-Manager (Alternative)

If you have cert-manager installed:

```bash
kubectl apply -f config/webhook/cert-manager.yaml
```

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `NAMESPACE` | Webhook deployment namespace | `stateful-migration` |
| `IMAGE_TAG` | Webhook image tag | `latest` |
| `SERVICE_NAME` | Webhook service name | `stateful-migration-webhook-service` |

### Webhook Configuration

The webhook is configured via the `MutatingWebhookConfiguration` resource:

- **Target**: Pods created in any namespace (except system namespaces)
- **Operations**: CREATE operations only
- **Failure Policy**: Ignore (non-blocking)
- **Side Effects**: None

## How It Works

### Resource Matching Logic

The webhook matches Jobs to CheckpointBackup CRs using the `resourceRef` field:

1. **Direct Job Match**:
   ```yaml
   resourceRef:
     apiVersion: batch/v1
     kind: Job
     name: my-migration-job
   ```

2. **CronJob Pattern Match**:
   ```yaml
   resourceRef:
     apiVersion: batch/v1
     kind: CronJob
     name: my-cronjob
   # Matches Jobs like: my-cronjob-1234567890, my-cronjob-xyz
   ```

### Image Resolution Priority

1. **Spec Images** (highest priority):
   ```yaml
   spec:
     containers:
     - name: app-container
       image: myapp:restored-v1.0
   ```

2. **Status Built Images** (fallback):
   ```yaml
   status:
     builtImages:
     - containerName: app-container
       imageName: myapp:checkpoint-abc123
       pushed: true
   ```

### Example Patch Operation

When a pod is created from a matching Job, the webhook generates JSON patches:

```json
[
  {
    "op": "replace",
    "path": "/spec/containers/0/image",
    "value": "myapp:restored-v1.0"
  },
  {
    "op": "replace", 
    "path": "/spec/containers/1/image",
    "value": "sidecar:checkpoint-def456"
  }
]
```

## Testing

### Create Test Resources

```bash
kubectl apply -f examples/webhook-test.yaml
```

### Verify Webhook Operation

1. **Check webhook pods**:
   ```bash
   kubectl get pods -n stateful-migration -l app=stateful-migration-webhook
   ```

2. **View logs**:
   ```bash
   kubectl logs -n stateful-migration -l app=stateful-migration-webhook --follow
   ```

3. **Test mutation**:
   ```bash
   # Create the test Job
   kubectl apply -f examples/webhook-test.yaml
   
   # Check if pod images were patched
   kubectl get pod -l job-name=test-migration-job -o jsonpath='{.items[0].spec.containers[*].image}'
   ```

### Expected Log Output

```
INFO    pod-mutator     Processing pod mutation {"pod": "test-migration-job-abc123", "namespace": "default"}
INFO    pod-mutator     Pod is from Job {"job": "test-migration-job", "pod": "test-migration-job-abc123"}
INFO    pod-mutator     Found matching CheckpointBackup {"backup": "test-checkpoint-backup"}
INFO    pod-mutator     Patching container image {"container": "app-container", "originalImage": "nginx:1.19", "newImage": "nginx:1.20-checkpoint"}
INFO    pod-mutator     Applying image patches {"patches": "[{\"op\":\"replace\",\"path\":\"/spec/containers/0/image\",\"value\":\"nginx:1.20-checkpoint\"}]"}
```

## Troubleshooting

### Common Issues

1. **Webhook not intercepting pods**:
   - Check MutatingWebhookConfiguration registration: `kubectl get mutatingwebhookconfigurations`
   - Verify webhook service is accessible: `kubectl get svc -n stateful-migration`
   - Check certificate validity

2. **Certificate errors**:
   - Regenerate certificates: `./scripts/generate-webhook-certs.sh`
   - Verify CA bundle in webhook configuration
   - Check certificate expiration

3. **No image patches applied**:
   - Verify CheckpointBackup CR exists and matches the Job
   - Check resourceRef configuration
   - Ensure container names match between Job and CheckpointBackup

### Debug Commands

```bash
# Check webhook configuration
kubectl describe mutatingwebhookconfiguration stateful-migration-pod-mutator-alt

# View webhook logs with debug level
kubectl logs -n stateful-migration -l app=stateful-migration-webhook --follow

# Test webhook connectivity
kubectl exec -n stateful-migration deployment/test-client -- curl -k https://stateful-migration-webhook-service:443/mutate-v1-pod

# List CheckpointBackup CRs
kubectl get checkpointbackups --all-namespaces -o wide
```

### Performance Considerations

- **Deployment with replicas**: Ensures webhook high availability with multiple replicas
- **Failure policy**: Set to "Ignore" to prevent blocking pod creation
- **Resource limits**: Configured for minimal resource usage
- **Leader election**: Disabled since webhook is stateless

## Security

### RBAC Permissions

The webhook requires minimal permissions:
- Read access to CheckpointBackup CRs
- Read access to pods and Jobs
- No write permissions to any resources

### Network Security

- TLS encryption for all webhook communications
- Service-to-service communication within cluster
- No external network access required

### Certificate Management

- Self-signed certificates for internal communication
- Automatic certificate rotation via cert-manager (optional)
- Certificate validation by Kubernetes API server

## Monitoring

### Metrics

The webhook exposes Prometheus metrics on port 8080:

```bash
# Port forward to access metrics
kubectl port-forward -n stateful-migration svc/stateful-migration-webhook-service 8080:8080

# View metrics
curl http://localhost:8080/metrics
```

### Health Checks

- **Liveness probe**: `/healthz` endpoint
- **Readiness probe**: `/readyz` endpoint
- **Startup probe**: Configured with appropriate delays

## Advanced Configuration

### Custom Namespaces

To deploy in a different namespace:

```bash
export NAMESPACE=my-custom-namespace
./scripts/deploy-webhook.sh
```

### Multiple Webhook Instances

For high availability, the Deployment runs multiple replicas by default. You can adjust the replica count:

```yaml
# In deployment.yaml
apiVersion: apps/v1
kind: Deployment
spec:
  replicas: 3  # Adjust number of replicas for HA
```

### Custom Failure Policies

Modify the MutatingWebhookConfiguration configuration:

```yaml
spec:
  failurePolicy: Fail  # Block pod creation on webhook failure
  # vs
  failurePolicy: Ignore  # Allow pod creation even if webhook fails
```
