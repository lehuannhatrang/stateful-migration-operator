# Deployment

Deployment tooling is organized by scenario. Pick the directory that matches
your topology.

| Scenario | Directory | Use when |
|----------|-----------|----------|
| Single cluster, CheckpointBackup only | [`single-cluster/`](single-cluster/) | You only need the CheckpointBackup controller on one cluster. No management cluster, no Karmada. |
| Multi-cluster with Karmada | [`multi-cluster-karmada/`](multi-cluster-karmada/) | You run a management cluster with Karmada and propagate controllers to member clusters. |

## Single cluster (CheckpointBackup only)

Installs the CheckpointBackup controller DaemonSet, its CRD, RBAC, namespace,
and PVC onto the cluster in your current kubeconfig context. This is the
smallest useful install and has no Karmada or management-cluster dependency.

```bash
deploy/single-cluster/install.sh            # uses the overlay's default image
deploy/single-cluster/install.sh --help     # all options
deploy/single-cluster/uninstall.sh          # remove the controller
```

The installer is a thin wrapper around the kustomize overlay at
[`config/checkpoint-backup-single-cluster/`](../config/checkpoint-backup-single-cluster/).
To apply the manifests directly without the script:

```bash
kubectl kustomize --load-restrictor LoadRestrictionsNone \
  config/checkpoint-backup-single-cluster | kubectl apply -f -
```

## Multi-cluster with Karmada

The management-cluster and Karmada-based workflow (MigrationBackup /
MigrationRestore controllers plus propagation of the CheckpointBackup controller
to member clusters) lives in [`multi-cluster-karmada/`](multi-cluster-karmada/).
The repository-root `deploy.sh` orchestrates the full multi-controller
deployment; see [`../DEPLOYMENT_GUIDE.md`](../DEPLOYMENT_GUIDE.md).
