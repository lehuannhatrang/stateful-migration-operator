# Single-cluster CheckpointBackup Controller

Install the CheckpointBackup controller onto a single Kubernetes cluster with no
management cluster and no Karmada. The controller runs as a DaemonSet on every
node, watches `CheckpointBackup` resources, checkpoints the targeted containers
via the kubelet checkpoint API, builds checkpoint images with buildah, and
pushes them to a container registry.

## What gets installed

`install.sh` applies the [`config/checkpoint-backup-single-cluster`](../../config/checkpoint-backup-single-cluster/)
kustomize overlay, which contains only single-cluster resources (no Karmada
objects):

- `CustomResourceDefinition` `checkpointbackups.migration.dcnlab.com`
- `Namespace` `stateful-migration`
- `ServiceAccount`, `ClusterRole`, `ClusterRoleBinding` for the controller
- `PersistentVolumeClaim` `checkpoint-storage` (ReadWriteMany)
- `DaemonSet` `checkpoint-backup-controller`

Optionally, when registry credentials are supplied, the installer also creates a
`registry-credentials` secret in the `stateful-migration` namespace.

## Prerequisites

- `kubectl` configured for the target cluster.
- Nodes with a container runtime that supports checkpointing (CRI-O, or
  containerd with CRIU) and privileged containers enabled.
- A StorageClass that supports `ReadWriteMany` for the `checkpoint-storage` PVC,
  or pass `--storage-class` pointing to one.

## Usage

```bash
# Minimal install using the overlay's default image
deploy/single-cluster/install.sh

# Pin a specific image and target a named context
deploy/single-cluster/install.sh \
  --image myrepo/stateful-migration-operator:checkpointBackup_v2.16 \
  --context my-cluster

# Provide registry credentials and an RWX-capable StorageClass
deploy/single-cluster/install.sh \
  --registry-user myuser --registry-pass 'mypassword' --registry-url myregistry.com \
  --storage-class rook-cephfs

# Preview the rendered manifests without applying
deploy/single-cluster/install.sh --dry-run
```

Run `deploy/single-cluster/install.sh --help` for the full option list.

## Applying without the script

```bash
kubectl kustomize --load-restrictor LoadRestrictionsNone \
  config/checkpoint-backup-single-cluster | kubectl apply -f -
```

The `--load-restrictor LoadRestrictionsNone` flag is required because the overlay
references resource files from sibling directories under `config/`.

## Verification

```bash
kubectl -n stateful-migration get pods -o wide
kubectl -n stateful-migration logs -l app.kubernetes.io/name=checkpoint-backup-controller -f
kubectl get checkpointbackups -A
```

## Uninstall

```bash
# Remove the controller but keep the CRD and namespace (and existing CheckpointBackups)
deploy/single-cluster/uninstall.sh

# Remove everything, including the CRD and namespace
deploy/single-cluster/uninstall.sh --purge
```
