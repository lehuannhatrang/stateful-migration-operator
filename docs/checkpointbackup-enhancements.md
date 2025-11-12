# CheckpointBackup Enhancements

This document describes the new features added to the CheckpointBackup Custom Resource.

## New Fields

### `registry` (Now Optional)

- **Type**: `*Registry` (pointer to Registry struct)
- **Default**: `nil` (when not specified)
- **Description**: When not provided, checkpoint images will be built locally using `localhost` names without pushing to any registry.

**Behavior when registry is not provided**:
- Images are built with names like `localhost/checkpoint-{podName}-{containerName}:{timestamp}`
- No registry authentication or push operations are performed
- If no containers are specified in the spec, all containers in the pod are automatically checkpointed
- Perfect for local development or when you only need local checkpoint images

### `stopPod` (Optional)

- **Type**: `*bool` (pointer to boolean)
- **Default**: `false` (when not specified)
- **Description**: When set to `true`, the pod will be deleted after a successful checkpoint operation.

**Behavior**:
- The controller will perform the checkpoint operation normally
- After successful checkpoint completion, the pod will be deleted
- Any scheduled jobs for this CheckpointBackup will be removed (no further checkpoints will occur)
- The status will be updated to `CompletedPodDeleted` to indicate the pod was successfully deleted
- If pod deletion fails, the status will be set to `CompletedWithError` with an appropriate error message

### `schedule` Field Enhancement

The `schedule` field now accepts two types of values:

1. **Cron format** (existing behavior): Standard cron expressions like `"0 2 * * *"` for scheduled recurring checkpoints
2. **"immediately"** (new): Performs a one-time checkpoint operation immediately

**Behavior with "immediately"**:
- The checkpoint operation is performed once during the first reconciliation
- No cron job is scheduled
- Subsequent reconciliations will skip checkpoint creation if already completed
- Works well with `stopPod: true` for one-time checkpoint and pod deletion

## Usage Examples

### Example 1: Localhost Checkpoint (No Registry)

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: localhost-checkpoint
  namespace: default
spec:
  schedule: "immediately"
  podRef:
    name: my-pod
    namespace: default
  resourceRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-deployment
    namespace: default
  # No registry field - builds images locally
  # No containers field - checkpoints all containers automatically
```

### Example 2: Localhost Checkpoint with Pod Deletion

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: localhost-checkpoint-stop
  namespace: default
spec:
  schedule: "immediately"
  stopPod: true
  podRef:
    name: my-pod
    namespace: default
  resourceRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-deployment
    namespace: default
  # No registry - builds locally and deletes pod
```

### Example 3: Immediate Checkpoint (No Pod Deletion)

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: immediate-checkpoint
  namespace: default
spec:
  schedule: "immediately"
  stopPod: false  # Optional, defaults to false
  podRef:
    name: my-pod
    namespace: default
  # ... other required fields
```

### Example 2: Immediate Checkpoint with Pod Deletion

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: checkpoint-and-stop
  namespace: default
spec:
  schedule: "immediately"
  stopPod: true
  podRef:
    name: my-pod
    namespace: default
  # ... other required fields
```

### Example 3: Scheduled Checkpoint with Pod Deletion

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: scheduled-checkpoint-stop
  namespace: default
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  stopPod: true
  podRef:
    name: my-pod
    namespace: default
  # ... other required fields
```

**Note**: In Example 3, the pod will be deleted after the first successful checkpoint, and no further scheduled checkpoints will occur.

## Status Updates

The controller now provides detailed phase tracking throughout the checkpoint process:

### Phase Progression

1. **`Checkpointing`**: Creating checkpoint via kubelet API
2. **`Checkpointed`**: Checkpoint file created successfully
3. **`ImageBuilding`**: Building checkpoint image with buildah
4. **`ImageBuilt`**: Checkpoint image built successfully
5. **`ImagePushing`**: Pushing image to registry (only if registry configured)
6. **`ImagePushed`**: Image pushed to registry successfully (only if registry configured)
7. **`Completed`**: All operations completed successfully
8. **`CompletedPodDeleted`**: Checkpoint completed and pod deleted successfully
9. **`CompletedWithError`**: Checkpoint completed but pod deletion failed
10. **`Failed`**: Operation failed at some step

### Checkpoint Files Tracking

The status includes a `checkpointFiles` array that tracks checkpoint files created for each container:

```yaml
status:
  phase: Checkpointed
  message: "Checkpoint created for container app-container: checkpoint-default_my-pod-app-container-2025-01-04T14:30:22Z.tar"
  checkpointFiles:
  - containerName: app-container
    filePath: checkpoint-default_my-pod-app-container-2025-01-04T14:30:22Z.tar
    checkpointTime: "2025-01-04T14:30:22Z"
```

**Resumable Operations**: If the controller restarts or crashes after checkpointing but before building images, it will:
1. Check the status for existing checkpoint file paths
2. Verify the files still exist on disk
3. Skip checkpoint creation and proceed directly to image building
4. If files are missing, recreate the checkpoints

This makes the controller resilient to interruptions and avoids re-checkpointing running pods.

### Built Images Tracking

The status also includes a `builtImages` array that tracks all successfully built checkpoint images:

```yaml
status:
  phase: Completed
  message: "All containers checkpointed successfully"
  builtImages:
  - containerName: app-container
    imageName: localhost/checkpoint-my-pod-app-container:20250104-143022
    buildTime: "2025-01-04T14:30:22Z"
    pushed: false
  - containerName: sidecar
    imageName: localhost/checkpoint-my-pod-sidecar:20250104-143025
    buildTime: "2025-01-04T14:30:25Z"
    pushed: false
```

### Checkpoint File Cleanup

After successful image build and push (if registry is configured), the checkpoint tar file is automatically deleted from `/var/lib/kubelet/checkpoints/` to save disk space.

### Status Update Conflict Resolution

The controller implements automatic retry logic with exponential backoff to handle resource version conflicts when updating status. This ensures that status updates succeed even under concurrent operations:

- Maximum 3 retry attempts per status update
- Exponential backoff: 100ms, 200ms, 300ms
- Automatic re-fetch of latest resource version on conflict
- In-memory backup object kept in sync with status updates

## RBAC Changes

The controller now requires additional RBAC permissions to delete pods:

```yaml
# +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
```

This permission is automatically included in the generated RBAC manifests.

## Implementation Details

### Controller Logic

1. **Immediate Schedule Handling**: When `schedule: "immediately"` is detected, the controller skips cron job creation and performs the checkpoint once.

2. **Pod Deletion Logic**: After successful checkpoint completion, if `stopPod` is `true`, the controller:
   - Deletes the pod using the Kubernetes API
   - Removes any scheduled cron jobs
   - Updates the status appropriately

3. **Reconciliation Prevention**: Once a pod is deleted (`CompletedPodDeleted` status), further reconciliations are skipped to prevent unnecessary processing.

### Error Handling

- If checkpoint creation fails, pod deletion is not attempted
- If pod deletion fails after successful checkpoint, the error is logged and status is updated to `CompletedWithError`
- Scheduled jobs are cleaned up regardless of pod deletion success/failure

## Migration Guide

Existing CheckpointBackup resources will continue to work without any changes:
- The `registry` field is now optional but existing resources with registry configurations will work unchanged
- The `stopPod` field is optional and defaults to `false`
- The `schedule` field continues to accept cron expressions as before
- No breaking changes to existing functionality

### New Capabilities for Simplified Usage

For simpler use cases, you can now create minimal CheckpointBackup resources:

```yaml
apiVersion: migration.dcnlab.com/v1
kind: CheckpointBackup
metadata:
  name: simple-checkpoint
spec:
  schedule: "immediately"
  podRef:
    name: my-pod
  resourceRef:
    apiVersion: apps/v1
    kind: Deployment
    name: my-deployment
```

This minimal configuration will:
- Checkpoint all containers in the pod automatically
- Build images locally with `localhost/checkpoint-*` names
- Skip registry push operations
- Perfect for development and testing scenarios
