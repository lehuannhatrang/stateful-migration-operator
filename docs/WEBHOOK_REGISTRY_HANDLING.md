# Webhook Registry URL Handling

The admission webhook automatically handles container registry URLs when patching pod images based on CheckpointBackup CRs.

## How It Works

The webhook intelligently prepends the registry URL from the CheckpointBackup CR to container images when needed.

### Logic Flow

1. **Get the image** from CheckpointBackup `spec.containers[].image` or `status.builtImages[].imageName`
2. **Get the registry URL** from CheckpointBackup `spec.registry.url`
3. **Normalize the registry URL** (remove `http://` or `https://` prefix)
4. **Check if image already has a registry URL**:
   - If YES: Use the image as-is
   - If NO: Prepend the registry URL
5. **Apply the patch** to the pod

## Examples

### Case 1: Image Without Registry URL

**CheckpointBackup CR:**
```yaml
spec:
  containers:
  - name: trainer
    image: checkpoint/preemption:lpj-2-6p54x
  registry:
    url: http://192.168.40.246:30080
    repository: checkpoint
```

**Result:**
- Original pod image: `deepspeed/deepspeed:latest`
- Patched pod image: `192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x`

**Webhook logs:**
```
INFO  Patching container image
  container: trainer
  originalImage: deepspeed/deepspeed:latest
  backupImage: checkpoint/preemption:lpj-2-6p54x
  finalImage: 192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x
```

### Case 2: Image Already Has Registry URL

**CheckpointBackup CR:**
```yaml
spec:
  containers:
  - name: trainer
    image: 192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x
  registry:
    url: http://192.168.40.246:30080
    repository: checkpoint
```

**Result:**
- Original pod image: `deepspeed/deepspeed:latest`
- Patched pod image: `192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x` (unchanged)

**Webhook logs:**
```
INFO  Patching container image
  container: trainer
  originalImage: deepspeed/deepspeed:latest
  backupImage: 192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x
  finalImage: 192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x
```

### Case 3: Docker Hub Image with Registry

**CheckpointBackup CR:**
```yaml
spec:
  containers:
  - name: app
    image: docker.io/library/nginx:1.21-checkpoint
  registry:
    url: http://192.168.40.246:30080
```

**Result:**
- Patched pod image: `docker.io/library/nginx:1.21-checkpoint` (registry already present, not modified)

### Case 4: No Registry in CheckpointBackup

**CheckpointBackup CR:**
```yaml
spec:
  containers:
  - name: app
    image: nginx:1.21-checkpoint
  # No registry specified
```

**Result:**
- Patched pod image: `nginx:1.21-checkpoint` (no registry URL to prepend)

## Registry URL Detection

The webhook detects if an image already has a registry URL by checking if the first part contains:

1. **Protocol**: `://` (e.g., `https://registry.example.com/image`)
2. **Port**: `:` (e.g., `192.168.40.246:30080/image`)
3. **Domain**: `.` (e.g., `docker.io/library/nginx`)

### Examples of Detection

| Image | Has Registry? | Reason |
|-------|---------------|--------|
| `nginx:latest` | ❌ No | No domain, port, or protocol |
| `checkpoint/preemption:v1` | ❌ No | Just repo/image format |
| `docker.io/nginx:latest` | ✅ Yes | Contains `.` (domain) |
| `192.168.40.246:30080/checkpoint/image:v1` | ✅ Yes | Contains `:` (port) |
| `https://registry.com/image:v1` | ✅ Yes | Contains `://` (protocol) |
| `registry.example.com/repo/image:v1` | ✅ Yes | Contains `.` (domain) |

## URL Normalization

The registry URL from `spec.registry.url` is normalized before use:

| Input URL | Normalized URL |
|-----------|----------------|
| `http://192.168.40.246:30080` | `192.168.40.246:30080` |
| `https://registry.example.com/` | `registry.example.com` |
| `registry.io` | `registry.io` |
| `192.168.1.1:5000/` | `192.168.1.1:5000` |

## Best Practices

### Option 1: Let the Webhook Handle Registry URLs (Recommended)

Store images without registry URLs in the CheckpointBackup CR:

```yaml
spec:
  containers:
  - name: trainer
    image: checkpoint/preemption:lpj-2-6p54x  # No registry URL
  registry:
    url: http://192.168.40.246:30080
```

**Advantages:**
- More flexible - easy to change registry
- Cleaner CR definitions
- Registry URL managed in one place

### Option 2: Include Full Registry URLs

Store complete image references:

```yaml
spec:
  containers:
  - name: trainer
    image: 192.168.40.246:30080/checkpoint/preemption:lpj-2-6p54x
  registry:
    url: http://192.168.40.246:30080  # Still needed for other operations
```

**Advantages:**
- Explicit and clear
- No ambiguity
- Works even if registry field is missing

## Troubleshooting

### Image Pull Errors After Patching

If pods fail with `ImagePullBackOff` after webhook patching:

1. **Check the final image in webhook logs:**
   ```bash
   kubectl logs -n stateful-migration -l app=stateful-migration-webhook | grep "finalImage"
   ```

2. **Verify the image exists in your registry:**
   ```bash
   # If using HTTP registry
   curl http://192.168.40.246:30080/v2/checkpoint/preemption/tags/list
   ```

3. **Check if pods can pull from the registry:**
   ```bash
   kubectl describe pod <pod-name> | grep -A 10 "Events:"
   ```

### Incorrect Registry URL Format

If the patched image has an incorrect format:

- **Check normalization:** Ensure the registry URL in the CR is correct
- **Check webhook logs:** Look for `backupImage` and `finalImage` in logs
- **Verify detection logic:** Check if the image is being detected as having a registry when it shouldn't

Example debug:
```bash
kubectl logs -n stateful-migration -l app=stateful-migration-webhook --tail=50 | \
  grep -E "(backupImage|finalImage)"
```

## Testing

To test the registry URL handling:

```bash
# 1. Create a CheckpointBackup with image lacking registry URL
kubectl apply -f - <<EOF
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: test-registry-handling
  namespace: demo
spec:
  schedule: "immediately"
  podRef:
    name: test-pod
  resourceRef:
    apiVersion: batch/v1
    kind: Job
    name: test-job
  registry:
    url: http://192.168.40.246:30080
    repository: checkpoint
  containers:
  - name: app
    image: checkpoint/test:v1.0
EOF

# 2. Create a Job
kubectl create job test-job --image=nginx:latest -n demo

# 3. Check webhook logs
kubectl logs -n stateful-migration -l app=stateful-migration-webhook | \
  grep -A 3 "Patching container image"

# Expected: finalImage should be "192.168.40.246:30080/checkpoint/test:v1.0"
```

