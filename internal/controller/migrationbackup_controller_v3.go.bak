/*
Copyright 2025 Le huan and Jeong SeungJun

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package controller

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"

	karmadav1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	corev1 "k8s.io/api/core/v1"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// ---- Constants & utility labels/annotations ---------------------------------

const (
	// Finalizer to ensure proper cleanup.
	MigrationBackupFinalizer = "migrationbackup.migration.dcnlab.com/finalizer"

	CheckpointMigrationLabel = "checkpoint-migration.dcn.io" // "true" on target resources
)

// ---- Reconciler --------------------------------------------------------------

type MigrationBackupReconciler struct {
	client.Client
	Scheme              *runtime.Scheme
	KarmadaClient       *KarmadaClient
	MemberClusterClient *MemberClusterClient
}

// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile moves the current state closer to the desired state.
func (r *MigrationBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Initialize Karmada client if not already done.
	if r.KarmadaClient == nil {
		kc, err := NewKarmadaClient()
		if err != nil {
			log.Error(err, "Failed to initialize Karmada client")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.KarmadaClient = kc
		log.Info("Successfully initialized Karmada client")
	}

	// Initialize MemberClusterClient.
	if r.MemberClusterClient == nil {
		mc, err := NewMemberClusterClient(r.KarmadaClient)
		if err != nil {
			log.Error(err, "Failed to initialize MemberClusterClient")
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}
		r.MemberClusterClient = mc
		log.Info("Successfully initialized MemberClusterClient")
	}

	// Fetch the StatefulMigration instance.
	var sm migrationv1.StatefulMigration
	if err := r.Get(ctx, req.NamespacedName, &sm); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("StatefulMigration resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get StatefulMigration")
		return ctrl.Result{}, err
	}

	// Handle deletion with finalizer.
	if sm.DeletionTimestamp != nil {
		if containsString(sm.Finalizers, MigrationBackupFinalizer) {
			if err := r.reconcileDelete(ctx, &sm); err != nil {
				log.Error(err, "Failed to reconcile delete")
				return ctrl.Result{}, err
			}
			// remove finalizer
			sm.Finalizers = removeString(sm.Finalizers, MigrationBackupFinalizer)
			if err := r.Update(ctx, &sm); err != nil {
				log.Error(err, "Failed to remove finalizer")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Ensure finalizer present.
	if !containsString(sm.Finalizers, MigrationBackupFinalizer) {
		sm.Finalizers = append(sm.Finalizers, MigrationBackupFinalizer)
		if err := r.Update(ctx, &sm); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Normal reconcile path.
	return r.reconcileNormal(ctx, &sm)
}

// ---- Normal reconcile (MULTI-CLUSTER) ---------------------------------------

func (r *MigrationBackupReconciler) reconcileNormal(ctx context.Context, sm *migrationv1.StatefulMigration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// A. decide target cluster SET (multi)
	targetSet, err := r.determineTargetClusters(&sm.ObjectMeta, &sm.Spec)
	if err != nil {
		log.Error(err, "Failed to determine target clusters")
		return ctrl.Result{}, err
	}
	targets := targetSet.UnsortedList()
	sort.Strings(targets)
	log.Info("Determined target clusters", "targets", targets)

	// B. Ensure namespace on Karmada + propagate to ALL targets
	if err := r.ensureStatefulMigrationNamespace(ctx, sm, targets); err != nil {
		log.Error(err, "Failed to ensure SM namespace propagation to targets")
		return ctrl.Result{}, err
	}

	// C. Per-cluster loop: label target resource on cluster, list pods, ensure CRD, create/update backups+PPs
	// Also build current pod map per cluster for later orphan cleanup.
	podsByCluster := make(map[string][]corev1.Pod, len(targets))

	for _, cluster := range targets {
		// C1. add operating label on target resource (per cluster)
		if err := r.addLabelToTargetResource(ctx, sm.Spec.ResourceRef, cluster); err != nil {
			log.Error(err, "Failed to add label on target resource", "cluster", cluster)
			return ctrl.Result{}, err
		}

		// C2. list pods from ref (per cluster)
		pods, err := r.getPodsFromResourceRef(ctx, sm.Spec.ResourceRef, cluster)
		if err != nil {
			log.Error(err, "Failed to list pods from ref", "cluster", cluster)
			return ctrl.Result{}, err
		}
		podsByCluster[cluster] = pods

		// C3. ensure CRD exists on cluster (if you have such helper; noop otherwise)
/* 		if err := r.MemberClusterClient.EnsureCheckpointBackupCRD(ctx, cluster); err != nil {
			log.Error(err, "Failed to ensure CheckpointBackup CRD on cluster", "cluster", cluster)
			return ctrl.Result{}, err
		} */

		// C4. For each pod: create/update CheckpointBackup on Karmada + PP to propagate to THIS cluster only
		for _, pod := range pods {
			if err := r.reconcileCheckpointBackupForPod(ctx, sm, &pod, cluster); err != nil {
				log.Error(err, "Failed to reconcile CheckpointBackup", "pod", pod.Name, "cluster", cluster)
				return ctrl.Result{}, err
			}
		}
	}

	// D. Garbage collection
	// D1) remove backups on Karmada that belong to non-target clusters
	if err := r.cleanupBackupsOutsideTargets(ctx, sm, targetSet); err != nil {
		log.Error(err, "Failed to cleanup non-target cluster backups")
		return ctrl.Result{}, err
	}
	// D2) per-cluster orphan cleanup (pods gone)
	if err := r.cleanupOrphanedCheckpointBackupsAcrossClusters(ctx, sm, podsByCluster); err != nil {
		log.Error(err, "Failed to cleanup orphaned CheckpointBackups")
		return ctrl.Result{}, err
	}

	log.Info("Reconciled StatefulMigration (multi-cluster)", "name", sm.Name, "targets", targets)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// ---- Namespace ensure + PP (multi) ------------------------------------------

func (r *MigrationBackupReconciler) ensureStatefulMigrationNamespace(ctx context.Context, sm *migrationv1.StatefulMigration, targetClusters []string) error {
	log := logf.FromContext(ctx)

	nsName := sm.Namespace
	if nsName == "" {
		return fmt.Errorf("StatefulMigration.metadata.namespace must be set")
	}

	// Ensure namespace exists on Karmada control plane
	var ns corev1.Namespace
	if err := r.KarmadaClient.Get(ctx, types.NamespacedName{Name: nsName}, &ns); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get namespace %s on Karmada: %w", nsName, err)
		}
		// create
		ns = corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: nsName,
				Labels: map[string]string{
					"created-by":                "stateful-migration-operator",
					"app.kubernetes.io/name":    "stateful-migration",
					"app.kubernetes.io/part-of": "stateful-migration-operator",
				},
			},
		}
		if err := r.KarmadaClient.Create(ctx, &ns); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create namespace %s on Karmada: %w", nsName, err)
		}
		log.Info("Ensured namespace on Karmada", "namespace", nsName)
	}

	// Ensure PropagationPolicy that sends this namespace to ALL target clusters
	if err := r.ensureNamespacePropagationPolicy(ctx, nsName, targetClusters); err != nil {
		return err
	}

	return nil
}

func (r *MigrationBackupReconciler) ensureNamespacePropagationPolicy(ctx context.Context, namespaceName string, targetClusters []string) error {
	policyName := fmt.Sprintf("%s-namespace-propagation", namespaceName)

	existing := &karmadav1alpha1.PropagationPolicy{}
	err := r.KarmadaClient.Get(ctx, types.NamespacedName{
		Namespace: namespaceName,
		Name:      policyName,
	}, existing)

	desired := &karmadav1alpha1.PropagationPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: karmadav1alpha1.SchemeGroupVersion.String(),
			Kind:       "PropagationPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: namespaceName,
			Labels: map[string]string{
				"created-by":                "stateful-migration-operator",
				"app.kubernetes.io/name":    "stateful-migration",
				"app.kubernetes.io/part-of": "stateful-migration-operator",
				"resource-type":             "namespace",
			},
		},
		Spec: karmadav1alpha1.PropagationSpec{
			ResourceSelectors: []karmadav1alpha1.ResourceSelector{{
				APIVersion: "v1", Kind: "Namespace", Name: namespaceName,
			}},
			Placement: karmadav1alpha1.Placement{
				ClusterAffinity: &karmadav1alpha1.ClusterAffinity{
					ClusterNames: append([]string{}, targetClusters...),
				},
			},
		},
	}

	if apierrors.IsNotFound(err) {
		return r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("failed to get PropagationPolicy %s/%s: %w", namespaceName, policyName, err)
	}

	// Compare placement; update when differs
	needUpdate := true
	if existing.Spec.Placement.ClusterAffinity != nil {
		old := sets.NewString(existing.Spec.Placement.ClusterAffinity.ClusterNames...)
		new := sets.NewString(targetClusters...)
		needUpdate = !old.Equal(new)
	}
	if needUpdate {
		existing.Spec = desired.Spec
		return r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, existing)
	}
	return nil
}

// ---- Delete reconcile --------------------------------------------------------

func (r *MigrationBackupReconciler) reconcileDelete(ctx context.Context, sm *migrationv1.StatefulMigration) error {
	// Remove operating label from target resource on all clusters we know (best effort)
	targets := sets.NewString(sm.Spec.SourceClusters...)
	for cluster := range targets {
		_ = r.removeLabelFromTargetResource(ctx, sm.Spec.ResourceRef, cluster)
	}

	// Delete all CheckpointBackups and their PPs in this namespace for this SM
	if err := r.deleteAllCheckpointBackups(ctx, sm); err != nil {
		return err
	}
	return nil
}

// ---- Resource label ops (per cluster) ---------------------------------------

func (r *MigrationBackupReconciler) addLabelToTargetResource(ctx context.Context, ref migrationv1.ResourceRef, cluster string) error {
	switch strings.ToLower(ref.Kind) {
	case "statefulset":
		sts, err := r.MemberClusterClient.GetStatefulSetFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if sts.Labels == nil {
			sts.Labels = map[string]string{}
		}
		if sts.Labels[CheckpointMigrationLabel] != "true" {
			sts.Labels[CheckpointMigrationLabel] = "true"
			return r.MemberClusterClient.UpdateStatefulSetInCluster(ctx, cluster, sts)
		}
		return nil

	case "deployment":
		dep, err := r.MemberClusterClient.GetDeploymentFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if dep.Labels == nil {
			dep.Labels = map[string]string{}
		}
		if dep.Labels[CheckpointMigrationLabel] != "true" {
			dep.Labels[CheckpointMigrationLabel] = "true"
			return r.MemberClusterClient.UpdateDeploymentInCluster(ctx, cluster, dep)
		}
		return nil

	case "pod":
		pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if pod.Labels == nil {
			pod.Labels = map[string]string{}
		}
		if pod.Labels[CheckpointMigrationLabel] != "true" {
			pod.Labels[CheckpointMigrationLabel] = "true"
			return r.MemberClusterClient.UpdatePodInCluster(ctx, cluster, pod)
		}
		return nil

	default:
		return fmt.Errorf("unsupported resource kind: %s", ref.Kind)
	}
}

func (r *MigrationBackupReconciler) removeLabelFromTargetResource(ctx context.Context, ref migrationv1.ResourceRef, cluster string) error {
	switch strings.ToLower(ref.Kind) {
	case "statefulset":
		sts, err := r.MemberClusterClient.GetStatefulSetFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if sts.Labels != nil && sts.Labels[CheckpointMigrationLabel] == "true" {
			delete(sts.Labels, CheckpointMigrationLabel)
			return r.MemberClusterClient.UpdateStatefulSetInCluster(ctx, cluster, sts)
		}
		return nil

	case "deployment":
		dep, err := r.MemberClusterClient.GetDeploymentFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if dep.Labels != nil && dep.Labels[CheckpointMigrationLabel] == "true" {
			delete(dep.Labels, CheckpointMigrationLabel)
			return r.MemberClusterClient.UpdateDeploymentInCluster(ctx, cluster, dep)
		}
		return nil

	case "pod":
		pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return err
		}
		if pod.Labels != nil && pod.Labels[CheckpointMigrationLabel] == "true" {
			delete(pod.Labels, CheckpointMigrationLabel)
			return r.MemberClusterClient.UpdatePodInCluster(ctx, cluster, pod)
		}
		return nil

	default:
		return fmt.Errorf("unsupported resource kind: %s", ref.Kind)
	}
}

// ---- Pod listing (per cluster) ----------------------------------------------

func (r *MigrationBackupReconciler) getPodsFromResourceRef(ctx context.Context, ref migrationv1.ResourceRef, cluster string) ([]corev1.Pod, error) {
	switch strings.ToLower(ref.Kind) {
	case "statefulset":
		sts, err := r.MemberClusterClient.GetStatefulSetFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return nil, err
		}
		if sts.Spec.Selector == nil {
			return nil, fmt.Errorf("statefulset %s/%s has nil .spec.selector", ref.Namespace, ref.Name)
		}
		sel, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
		if err != nil {
			return nil, err
		}
		return r.MemberClusterClient.ListPodsBySelector(ctx, cluster, ref.Namespace, sel)

	case "deployment":
		dep, err := r.MemberClusterClient.GetDeploymentFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return nil, err
		}
		if dep.Spec.Selector == nil {
			return nil, fmt.Errorf("deployment %s/%s has nil .spec.selector", ref.Namespace, ref.Name)
		}
		sel, err := metav1.LabelSelectorAsSelector(dep.Spec.Selector)
		if err != nil {
			return nil, err
		}
		return r.MemberClusterClient.ListPodsBySelector(ctx, cluster, ref.Namespace, sel)

	case "pod":
		pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, cluster, ref.Namespace, ref.Name)
		if err != nil {
			return nil, err
		}
		return []corev1.Pod{*pod}, nil

	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", ref.Kind)
	}
}

// ---- Backup creation (per pod/per cluster) ----------------------------------

func (r *MigrationBackupReconciler) reconcileCheckpointBackupForPod(
    ctx context.Context, sm *migrationv1.StatefulMigration, pod *corev1.Pod, cluster string,
) error {
    backupName := fmt.Sprintf("%s-%s-%s", sm.Name, pod.Name, cluster)

    cntName, _ := r.extractContainerInfo(pod)

    // 태그: 클러스터/Pod/컨테이너 조합(충돌 방지)
    tag := fmt.Sprintf("%s_%s_%s", cluster, pod.Name, cntName)

    // 예시처럼 "repository:tag" 문자열을 image에 넣음
    // (원한다면 docker.io 접두어를 붙여 "docker.io/jeongseungjun/checkpoints:..." 로 만들 수도 있음)
    image := fmt.Sprintf("%s:%s", sm.Spec.Registry.Repository, tag)

    // PP 이름(예시와 동일 규칙)
    ppName := fmt.Sprintf("%s-policy", backupName)

    desired := &migrationv1.CheckpointBackup{
        TypeMeta: metav1.TypeMeta{
            APIVersion: migrationv1.GroupVersion.String(),
            Kind:       "CheckpointBackup",
        },
        ObjectMeta: metav1.ObjectMeta{
            Name:      backupName,
            Namespace: sm.Namespace,
            Labels: map[string]string{
                "stateful-migration": sm.Name,
                "target-pod":         pod.Name,
                "target-cluster":     cluster, // ← 예시와 동일 키로 변경
            },
            Annotations: map[string]string{
                "propagationpolicy.karmada.io/name":      ppName,       // ← 예시 주석 추가
                "propagationpolicy.karmada.io/namespace": sm.Namespace, // ← 예시 주석 추가
            },
        },
        Spec: migrationv1.CheckpointBackupSpec{
            Schedule: sm.Spec.Schedule,
            PodRef: migrationv1.PodRef{
                Namespace: pod.Namespace,
                Name:      pod.Name,
            },
            ResourceRef: migrationv1.ResourceRef{
                APIVersion: sm.Spec.ResourceRef.APIVersion,
                Kind:       sm.Spec.ResourceRef.Kind,
                Namespace:  sm.Spec.ResourceRef.Namespace,
                Name:       sm.Spec.ResourceRef.Name,
            },
            Registry: sm.Spec.Registry,
            Containers: []migrationv1.Container{
                {
                    Name:  cntName,
                    Image: image, // ← 컨테이너 구조에 Image 필드가 있고 json:"image" 여야 합니다.
                },
            },
        },
    }

    // Create or Update (동일)
    var existing migrationv1.CheckpointBackup
    err := r.KarmadaClient.Get(ctx, types.NamespacedName{
        Name:      backupName,
        Namespace: sm.Namespace,
    }, &existing)

    switch {
    case apierrors.IsNotFound(err):
        if err := r.KarmadaClient.Create(ctx, desired); err != nil {
            return fmt.Errorf("create CheckpointBackup %s/%s: %w", sm.Namespace, backupName, err)
        }
    case err != nil:
        return fmt.Errorf("get CheckpointBackup %s/%s: %w", sm.Namespace, backupName, err)
    default:
        existing.Spec = desired.Spec
        if existing.Labels == nil {
            existing.Labels = map[string]string{}
        }
        for k, v := range desired.Labels {
            existing.Labels[k] = v
        }
        if existing.Annotations == nil {
            existing.Annotations = map[string]string{}
        }
        for k, v := range desired.Annotations {
            existing.Annotations[k] = v
        }
        if err := r.KarmadaClient.Update(ctx, &existing); err != nil {
            return fmt.Errorf("update CheckpointBackup %s/%s: %w", sm.Namespace, backupName, err)
        }
    }

    // PP는 여전히 "해당 백업 → 해당 클러스터만" 전파
    if err := r.createOrUpdatePropagationPolicy(ctx, sm.Namespace, backupName, []string{cluster}); err != nil {
        return err
    }
    return nil
}


func (r *MigrationBackupReconciler) extractContainerInfo(pod *corev1.Pod) (containerName string, image string) {
	// prefer first regular container
	if len(pod.Spec.Containers) > 0 {
		return pod.Spec.Containers[0].Name, pod.Spec.Containers[0].Image
	}
	// fallback to first init container
	if len(pod.Spec.InitContainers) > 0 {
		return pod.Spec.InitContainers[0].Name, pod.Spec.InitContainers[0].Image
	}
	return "unknown", ""
}

func (r *MigrationBackupReconciler) createOrUpdatePropagationPolicy(ctx context.Context, namespace string, backupName string, clusterNames []string) error {
	policyName := fmt.Sprintf("%s-policy", backupName)

	existing := &karmadav1alpha1.PropagationPolicy{}
	err := r.KarmadaClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: policyName}, existing)

	desired := &karmadav1alpha1.PropagationPolicy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: karmadav1alpha1.SchemeGroupVersion.String(),
			Kind:       "PropagationPolicy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: namespace,
			Labels: map[string]string{
				"created-by":                "stateful-migration-operator",
				"app.kubernetes.io/name":    "stateful-migration",
				"app.kubernetes.io/part-of": "stateful-migration-operator",
				"resource-type":             "checkpointbackup",
			},
		},
		Spec: karmadav1alpha1.PropagationSpec{
			ResourceSelectors: []karmadav1alpha1.ResourceSelector{{
				APIVersion: migrationv1.GroupVersion.String(),
				Kind:       "CheckpointBackup",
				Name:       backupName,
			}},
			Placement: karmadav1alpha1.Placement{
				ClusterAffinity: &karmadav1alpha1.ClusterAffinity{
					ClusterNames: append([]string{}, clusterNames...),
				},
			},
		},
	}

	if apierrors.IsNotFound(err) {
		return r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, desired)
	}
	if err != nil {
		return fmt.Errorf("failed to get PP %s/%s: %w", namespace, policyName, err)
	}

	// Compare placement
	needUpdate := true
	if existing.Spec.Placement.ClusterAffinity != nil {
		old := sets.NewString(existing.Spec.Placement.ClusterAffinity.ClusterNames...)
		new := sets.NewString(clusterNames...)
		needUpdate = !old.Equal(new)
	}
	if needUpdate {
		existing.Spec = desired.Spec
		return r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, existing)
	}
	return nil
}

// ---- Cleanup helpers ---------------------------------------------------------

// cleanupBackupsOutsideTargets removes CheckpointBackup resources that are labeled with a cluster
// NOT in the target set (and their per-backup PP).
func (r *MigrationBackupReconciler) cleanupBackupsOutsideTargets(ctx context.Context, sm *migrationv1.StatefulMigration, targets sets.Set[string]) error {
	if r.KarmadaClient == nil {
		return fmt.Errorf("Karmada client not initialized")
	}

	var list migrationv1.CheckpointBackupList
	if err := r.KarmadaClient.List(ctx, &list, &client.ListOptions{
		Namespace:     sm.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{"stateful-migration": sm.Name}),
	}); err != nil {
		return fmt.Errorf("failed to list CheckpointBackups: %w", err)
	}

	for i := range list.Items {
		bk := &list.Items[i]
		cluster := bk.Labels["target-cluster"]
		if cluster == "" || !targets.Has(cluster) {
			// delete PP first, then backup
			if err := r.deletePolicyForBackup(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := r.KarmadaClient.Delete(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete CheckpointBackup %s/%s: %w", bk.Namespace, bk.Name, err)
			}
		}
	}
	return nil
}

// cleanupOrphanedCheckpointBackupsAcrossClusters removes backups for pods that no longer exist,
// per cluster (to avoid same pod name collisions across clusters).
func (r *MigrationBackupReconciler) cleanupOrphanedCheckpointBackupsAcrossClusters(
	ctx context.Context,
	sm *migrationv1.StatefulMigration,
	podsByCluster map[string][]corev1.Pod,
) error {
	var list migrationv1.CheckpointBackupList
	if err := r.KarmadaClient.List(ctx, &list, &client.ListOptions{
		Namespace:     sm.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{"stateful-migration": sm.Name}),
	}); err != nil {
		return fmt.Errorf("failed to list CheckpointBackups: %w", err)
	}

	// Build map[cluster] -> set[podName]
	current := map[string]sets.Set[string]{}
	for cluster, pods := range podsByCluster {
		s := sets.New[string]()
		for _, p := range pods {
			s.Insert(p.Name)
		}
		current[cluster] = s
	}

	for i := range list.Items {
		bk := &list.Items[i]
		cluster := bk.Labels["target-cluster"]
		pod := bk.Labels["target-pod"]

		// if cluster missing from label, treat as orphan
		if cluster == "" {
			if err := r.deletePolicyForBackup(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := r.KarmadaClient.Delete(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete unlabeled backup %s/%s: %w", bk.Namespace, bk.Name, err)
			}
			continue
		}

		set, ok := current[cluster]
		if !ok || !set.Has(pod) {
			// pod no longer exists on that cluster
			if err := r.deletePolicyForBackup(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			if err := r.KarmadaClient.Delete(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete orphan backup %s/%s: %w", bk.Namespace, bk.Name, err)
			}
		}
	}
	return nil
}

// deleteAllCheckpointBackups deletes every CheckpointBackup for this SM (and their PPs).
func (r *MigrationBackupReconciler) deleteAllCheckpointBackups(ctx context.Context, sm *migrationv1.StatefulMigration) error {
	var list migrationv1.CheckpointBackupList
	if err := r.KarmadaClient.List(ctx, &list, &client.ListOptions{
		Namespace:     sm.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{"stateful-migration": sm.Name}),
	}); err != nil {
		return fmt.Errorf("failed to list CheckpointBackups: %w", err)
	}
	for i := range list.Items {
		bk := &list.Items[i]
		if err := r.deletePolicyForBackup(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err := r.KarmadaClient.Delete(ctx, bk); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to delete CheckpointBackup %s/%s: %w", bk.Namespace, bk.Name, err)
		}
	}
	return nil
}

// deletePolicyForBackup deletes PP named "<backupName>-policy" in the same namespace.
func (r *MigrationBackupReconciler) deletePolicyForBackup(ctx context.Context, backup *migrationv1.CheckpointBackup) error {
	policyName := fmt.Sprintf("%s-policy", backup.Name)
	pp := &karmadav1alpha1.PropagationPolicy{}
	if err := r.KarmadaClient.Get(ctx, types.NamespacedName{
		Namespace: backup.Namespace,
		Name:      policyName,
	}, pp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return r.KarmadaClient.Delete(ctx, pp)
}

// ---- Target cluster decision (MULTI) ----------------------------------------

func (r *MigrationBackupReconciler) determineTargetClusters(meta *metav1.ObjectMeta, spec *migrationv1.StatefulMigrationSpec) (sets.Set[string], error) {
	targets := sets.New[string]()

	// 1) metadata.labels["target-cluster"] (optional, add)
	if v := meta.GetLabels()["target-cluster"]; strings.TrimSpace(v) != "" {
		targets.Insert(v)
	}

	// 2) spec.sourceClusters (multi) - primary source of truth
	for _, c := range spec.SourceClusters {
		if strings.TrimSpace(c) != "" {
			targets.Insert(c)
		}
	}

	// If nothing specified, we fail-fast (you may extend: auto-detect actual placement here)
	if targets.Len() == 0 {
		return nil, fmt.Errorf("no target clusters determined: specify spec.sourceClusters (or label target-cluster)")
	}
	return targets, nil
}

// ---- Setup ------------------------------------------------------------------

func (r *MigrationBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1.StatefulMigration{}).
		Owns(&migrationv1.CheckpointBackup{}).
		Named("migrationbackup").
		Complete(r)
}

// ---- small helpers ----------------------------------------------------------

func containsString(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}
func removeString(slice []string, s string) []string {
	out := make([]string, 0, len(slice))
	for _, x := range slice {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}
