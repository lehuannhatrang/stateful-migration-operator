/*
Copyright 2025.

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
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	karmadav1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
)

const (
	// Label to mark resources as managed by checkpoint migration
	CheckpointMigrationLabel = "checkpoint-migration.dcn.io"
	// Finalizer to ensure proper cleanup
	MigrationBackupFinalizer = "migrationbackup.migration.dcnlab.com/finalizer"
)

// MigrationBackupReconciler reconciles a StatefulMigration object
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

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MigrationBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Initialize Karmada client if not already done
	if r.KarmadaClient == nil {
		karmadaClient, err := NewKarmadaClient()
		if err != nil {
			log.Error(err, "Failed to initialize Karmada client")
			// Continue without Karmada client - PropagationPolicies will be skipped
			log.Info("Continuing without Karmada client - PropagationPolicies will be skipped")
		} else {
			r.KarmadaClient = karmadaClient
			log.Info("Successfully initialized Karmada client")

			// Test Karmada connection
			if err := r.KarmadaClient.TestConnection(ctx); err != nil {
				log.Error(err, "Failed to connect to Karmada")
			}

			// Initialize MemberClusterClient
			if r.MemberClusterClient == nil {
				memberClient, err := NewMemberClusterClient(r.KarmadaClient)
				if err != nil {
					log.Error(err, "Failed to initialize MemberClusterClient")
				} else {
					r.MemberClusterClient = memberClient
					log.Info("Successfully initialized MemberClusterClient")
				}
			}
		}
	}

	// Fetch the StatefulMigration instance
	var statefulMigration migrationv1.StatefulMigration
	if err := r.Get(ctx, req.NamespacedName, &statefulMigration); err != nil {
		if errors.IsNotFound(err) {
			log.Info("StatefulMigration resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get StatefulMigration")
		return ctrl.Result{}, err
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&statefulMigration, MigrationBackupFinalizer) {
		controllerutil.AddFinalizer(&statefulMigration, MigrationBackupFinalizer)
		if err := r.Update(ctx, &statefulMigration); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Handle deletion
	if statefulMigration.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, &statefulMigration)
	}

	// Handle normal reconciliation
	return r.reconcileNormal(ctx, &statefulMigration)
}

// reconcileNormal handles the normal reconciliation logic
func (r *MigrationBackupReconciler) reconcileNormal(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Step 1: Add label to target resource
	if err := r.addLabelToTargetResource(ctx, statefulMigration); err != nil {
		log.Error(err, "Failed to add label to target resource")
		return ctrl.Result{}, err
	}

	// Step 2: Discover pods from the target resource
	pods, err := r.getPodsFromResourceRef(ctx, statefulMigration)
	if err != nil {
		log.Error(err, "Failed to get pods from resource reference")
		return ctrl.Result{}, err
	}

	// Step 3: Ensure stateful-migration namespace on Karmada and propagate to clusters
	if err := r.ensureStatefulMigrationNamespace(ctx, statefulMigration); err != nil {
		log.Error(err, "Failed to ensure stateful-migration namespace")
		return ctrl.Result{}, err
	}

	// Step 4: Ensure CheckpointBackup CRD on member clusters
	for _, cluster := range statefulMigration.Spec.SourceClusters {
		if r.MemberClusterClient != nil {
			// Ensure CheckpointBackup CRD exists on member cluster
			if err := r.MemberClusterClient.EnsureCRD(ctx, cluster); err != nil {
				log.Error(err, "Failed to ensure CheckpointBackup CRD on cluster", "cluster", cluster)
				return ctrl.Result{}, err
			}
		}
	}

	// Step 5: For each source cluster, create/update CheckpointBackup resources for each pod
	for _, cluster := range statefulMigration.Spec.SourceClusters {
		for _, pod := range pods {
			if err := r.reconcileCheckpointBackupForPod(ctx, statefulMigration, &pod, cluster); err != nil {
				log.Error(err, "Failed to reconcile CheckpointBackup for pod", "pod", pod.Name, "cluster", cluster)
				return ctrl.Result{}, err
			}
		}
	}

	// Step 6: Clean up orphaned CheckpointBackup resources
	if err := r.cleanupOrphanedCheckpointBackups(ctx, statefulMigration, pods); err != nil {
		log.Error(err, "Failed to cleanup orphaned CheckpointBackup resources")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled StatefulMigration", "name", statefulMigration.Name)
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
}

// ensureStatefulMigrationNamespace ensures the stateful-migration namespace exists on Karmada and is propagated to member clusters
func (r *MigrationBackupReconciler) ensureStatefulMigrationNamespace(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) error {
	log := logf.FromContext(ctx)
	namespaceName := "stateful-migration"

	// Check if namespace exists on Karmada control plane
	var existingNamespace corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: namespaceName}, &existingNamespace)

	if errors.IsNotFound(err) {
		// Create namespace on Karmada control plane
		log.Info("Creating stateful-migration namespace on Karmada", "namespace", namespaceName)

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
				Labels: map[string]string{
					"created-by":                "stateful-migration-operator",
					"app.kubernetes.io/name":    "stateful-migration",
					"app.kubernetes.io/part-of": "stateful-migration-operator",
				},
			},
		}

		if err := r.Create(ctx, namespace); err != nil {
			return fmt.Errorf("failed to create namespace %s on Karmada: %w", namespaceName, err)
		}
		log.Info("Successfully created namespace on Karmada", "namespace", namespaceName)
	} else if err != nil {
		return fmt.Errorf("failed to check namespace %s on Karmada: %w", namespaceName, err)
	} else {
		log.Info("Namespace already exists on Karmada", "namespace", namespaceName)
	}

	// Create PropagationPolicy to propagate namespace to member clusters
	if r.KarmadaClient != nil {
		if err := r.ensureNamespacePropagationPolicy(ctx, statefulMigration, namespaceName); err != nil {
			return fmt.Errorf("failed to ensure namespace propagation policy: %w", err)
		}
	}

	return nil
}

// ensureNamespacePropagationPolicy ensures PropagationPolicy exists for the stateful-migration namespace
func (r *MigrationBackupReconciler) ensureNamespacePropagationPolicy(ctx context.Context, statefulMigration *migrationv1.StatefulMigration, namespaceName string) error {
	log := logf.FromContext(ctx)
	policyName := namespaceName + "-propagation"

	// Check if PropagationPolicy already exists
	existingPolicy := &karmadav1alpha1.PropagationPolicy{}
	err := r.KarmadaClient.Get(ctx, types.NamespacedName{
		Name:      policyName,
		Namespace: namespaceName,
	}, existingPolicy)

	if errors.IsNotFound(err) {
		// Create PropagationPolicy for namespace
		log.Info("Creating PropagationPolicy for namespace", "policy", policyName, "namespace", namespaceName)

		policy := &karmadav1alpha1.PropagationPolicy{
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
				ResourceSelectors: []karmadav1alpha1.ResourceSelector{
					{
						APIVersion: "v1",
						Kind:       "Namespace",
						Name:       namespaceName,
					},
				},
				Placement: karmadav1alpha1.Placement{
					ClusterAffinity: &karmadav1alpha1.ClusterAffinity{
						ClusterNames: statefulMigration.Spec.SourceClusters,
					},
				},
			},
		}

		if err := r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, policy); err != nil {
			return fmt.Errorf("failed to create namespace PropagationPolicy: %w", err)
		}
		log.Info("Successfully created PropagationPolicy for namespace", "policy", policyName)
	} else if err != nil {
		return fmt.Errorf("failed to check PropagationPolicy %s: %w", policyName, err)
	} else {
		log.Info("PropagationPolicy already exists for namespace", "policy", policyName, "namespace", namespaceName)
	}

	return nil
}

// reconcileDelete handles the deletion logic
func (r *MigrationBackupReconciler) reconcileDelete(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Remove label from target resource
	if err := r.removeLabelFromTargetResource(ctx, statefulMigration); err != nil {
		log.Error(err, "Failed to remove label from target resource")
		return ctrl.Result{}, err
	}

	// Delete all related CheckpointBackup resources
	if err := r.deleteAllCheckpointBackups(ctx, statefulMigration); err != nil {
		log.Error(err, "Failed to delete CheckpointBackup resources")
		return ctrl.Result{}, err
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(statefulMigration, MigrationBackupFinalizer)
	if err := r.Update(ctx, statefulMigration); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	log.Info("Successfully deleted StatefulMigration", "name", statefulMigration.Name)
	return ctrl.Result{}, nil
}

// addLabelToTargetResource adds the checkpoint migration label to the target resource
func (r *MigrationBackupReconciler) addLabelToTargetResource(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) error {
	resourceRef := statefulMigration.Spec.ResourceRef

	switch strings.ToLower(resourceRef.Kind) {
	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &sts); err != nil {
			return err
		}

		if sts.Labels == nil {
			sts.Labels = make(map[string]string)
		}
		sts.Labels[CheckpointMigrationLabel] = "true"

		return r.Update(ctx, &sts)

	case "deployment":
		var deployment appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &deployment); err != nil {
			return err
		}

		if deployment.Labels == nil {
			deployment.Labels = make(map[string]string)
		}
		deployment.Labels[CheckpointMigrationLabel] = "true"

		return r.Update(ctx, &deployment)

	case "pod":
		// For pods, we need to access them on the member clusters, not the management cluster
		if r.MemberClusterClient == nil {
			return fmt.Errorf("member cluster client not initialized")
		}

		// Get the pod from the first source cluster (assuming single cluster for pod resource)
		// In practice, a pod can only exist on one cluster at a time
		if len(statefulMigration.Spec.SourceClusters) == 0 {
			return fmt.Errorf("no source clusters specified for pod resource")
		}

		clusterName := statefulMigration.Spec.SourceClusters[0]
		pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, clusterName, resourceRef.Namespace, resourceRef.Name)
		if err != nil {
			return fmt.Errorf("failed to get pod from cluster %s: %w", clusterName, err)
		}

		if pod.Labels == nil {
			pod.Labels = make(map[string]string)
		}
		pod.Labels[CheckpointMigrationLabel] = "true"

		return r.MemberClusterClient.UpdatePodInCluster(ctx, clusterName, pod)

	default:
		return fmt.Errorf("unsupported resource kind: %s", resourceRef.Kind)
	}
}

// removeLabelFromTargetResource removes the checkpoint migration label from the target resource
func (r *MigrationBackupReconciler) removeLabelFromTargetResource(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) error {
	resourceRef := statefulMigration.Spec.ResourceRef

	switch strings.ToLower(resourceRef.Kind) {
	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &sts); err != nil {
			if errors.IsNotFound(err) {
				return nil // Resource already deleted
			}
			return err
		}

		if sts.Labels != nil {
			delete(sts.Labels, CheckpointMigrationLabel)
		}

		return r.Update(ctx, &sts)

	case "deployment":
		var deployment appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &deployment); err != nil {
			if errors.IsNotFound(err) {
				return nil // Resource already deleted
			}
			return err
		}

		if deployment.Labels != nil {
			delete(deployment.Labels, CheckpointMigrationLabel)
		}

		return r.Update(ctx, &deployment)

	case "pod":
		// For pods, we need to access them on the member clusters, not the management cluster
		if r.MemberClusterClient == nil {
			return nil // Skip if member cluster client not available
		}

		// Try to remove label from pod on each source cluster
		for _, clusterName := range statefulMigration.Spec.SourceClusters {
			pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, clusterName, resourceRef.Namespace, resourceRef.Name)
			if err != nil {
				if errors.IsNotFound(err) {
					continue // Pod not found on this cluster, skip
				}
				return fmt.Errorf("failed to get pod from cluster %s: %w", clusterName, err)
			}

			if pod.Labels != nil {
				delete(pod.Labels, CheckpointMigrationLabel)
			}

			if err := r.MemberClusterClient.UpdatePodInCluster(ctx, clusterName, pod); err != nil {
				return fmt.Errorf("failed to update pod on cluster %s: %w", clusterName, err)
			}
		}

		return nil

	default:
		return fmt.Errorf("unsupported resource kind: %s", resourceRef.Kind)
	}
}

// getPodsFromResourceRef gets all pods related to the resource reference
func (r *MigrationBackupReconciler) getPodsFromResourceRef(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) ([]corev1.Pod, error) {
	resourceRef := statefulMigration.Spec.ResourceRef

	switch strings.ToLower(resourceRef.Kind) {
	case "statefulset":
		var sts appsv1.StatefulSet
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &sts); err != nil {
			return nil, err
		}

		return r.getPodsFromSelector(ctx, resourceRef.Namespace, sts.Spec.Selector)

	case "deployment":
		var deployment appsv1.Deployment
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &deployment); err != nil {
			return nil, err
		}

		return r.getPodsFromSelector(ctx, resourceRef.Namespace, deployment.Spec.Selector)

	case "pod":
		// For pods, we need to access them on the member clusters, not the management cluster
		if r.MemberClusterClient == nil {
			return nil, fmt.Errorf("member cluster client not initialized")
		}

		var allPods []corev1.Pod

		// Get pod from each source cluster
		for _, clusterName := range statefulMigration.Spec.SourceClusters {
			pod, err := r.MemberClusterClient.GetPodFromCluster(ctx, clusterName, resourceRef.Namespace, resourceRef.Name)
			if err != nil {
				if errors.IsNotFound(err) {
					continue // Pod not found on this cluster, skip
				}
				return nil, fmt.Errorf("failed to get pod from cluster %s: %w", clusterName, err)
			}
			allPods = append(allPods, *pod)
		}

		return allPods, nil

	default:
		return nil, fmt.Errorf("unsupported resource kind: %s", resourceRef.Kind)
	}
}

// getPodsFromSelector gets pods matching the given selector
func (r *MigrationBackupReconciler) getPodsFromSelector(ctx context.Context, namespace string, selector *metav1.LabelSelector) ([]corev1.Pod, error) {
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, err
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList, &client.ListOptions{
		Namespace:     namespace,
		LabelSelector: labelSelector,
	}); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

// reconcileCheckpointBackupForPod creates or updates a CheckpointBackup for a specific pod and cluster
func (r *MigrationBackupReconciler) reconcileCheckpointBackupForPod(ctx context.Context, statefulMigration *migrationv1.StatefulMigration, pod *corev1.Pod, cluster string) error {
	// Generate CheckpointBackup name
	backupName := fmt.Sprintf("%s-%s-%s", statefulMigration.Name, pod.Name, cluster)

	// Create CheckpointBackup spec
	backup := &migrationv1.CheckpointBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      backupName,
			Namespace: statefulMigration.Namespace,
			Labels: map[string]string{
				"stateful-migration": statefulMigration.Name,
				"target-cluster":     cluster,
				"target-pod":         pod.Name,
			},
		},
		Spec: migrationv1.CheckpointBackupSpec{
			Schedule: statefulMigration.Spec.Schedule,
			PodRef: migrationv1.PodRef{
				Namespace: pod.Namespace,
				Name:      pod.Name,
			},
			ResourceRef: statefulMigration.Spec.ResourceRef,
			Registry:    statefulMigration.Spec.Registry,
			Containers:  r.extractContainerInfo(pod),
		},
	}

	// Set StatefulMigration as owner
	if err := controllerutil.SetControllerReference(statefulMigration, backup, r.Scheme); err != nil {
		return err
	}

	// Create or update CheckpointBackup
	var existingBackup migrationv1.CheckpointBackup
	if err := r.Get(ctx, types.NamespacedName{Name: backupName, Namespace: statefulMigration.Namespace}, &existingBackup); err != nil {
		if errors.IsNotFound(err) {
			// Create new CheckpointBackup
			if err := r.Create(ctx, backup); err != nil {
				return err
			}
		} else {
			return err
		}
	} else {
		// Update existing CheckpointBackup
		existingBackup.Spec = backup.Spec
		if err := r.Update(ctx, &existingBackup); err != nil {
			return err
		}
	}

	// Create Karmada PropagationPolicy to distribute CheckpointBackup to target cluster
	return r.createOrUpdatePropagationPolicy(ctx, backup, cluster)
}

// extractContainerInfo extracts container information from a pod
func (r *MigrationBackupReconciler) extractContainerInfo(pod *corev1.Pod) []migrationv1.Container {
	var containers []migrationv1.Container

	for _, container := range pod.Spec.Containers {
		containers = append(containers, migrationv1.Container{
			Name:  container.Name,
			Image: container.Image,
		})
	}

	return containers
}

// createOrUpdatePropagationPolicy creates or updates a Karmada PropagationPolicy for the CheckpointBackup
func (r *MigrationBackupReconciler) createOrUpdatePropagationPolicy(ctx context.Context, backup *migrationv1.CheckpointBackup, cluster string) error {
	log := logf.FromContext(ctx)

	// Skip if Karmada client is not available
	if r.KarmadaClient == nil {
		log.Info("Skipping PropagationPolicy creation - Karmada client not available", "backup", backup.Name)
		return nil
	}

	policyName := fmt.Sprintf("%s-policy", backup.Name)

	policy := &karmadav1alpha1.PropagationPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      policyName,
			Namespace: backup.Namespace,
		},
		Spec: karmadav1alpha1.PropagationSpec{
			ResourceSelectors: []karmadav1alpha1.ResourceSelector{
				{
					APIVersion: "migration.dcnlab.com/v1",
					Kind:       "CheckpointBackup",
					Name:       backup.Name,
				},
			},
			Placement: karmadav1alpha1.Placement{
				ClusterAffinity: &karmadav1alpha1.ClusterAffinity{
					ClusterNames: []string{cluster},
				},
			},
		},
	}

	return r.KarmadaClient.CreateOrUpdatePropagationPolicy(ctx, policy)
}

// cleanupOrphanedCheckpointBackups removes CheckpointBackup resources that no longer have corresponding pods
func (r *MigrationBackupReconciler) cleanupOrphanedCheckpointBackups(ctx context.Context, statefulMigration *migrationv1.StatefulMigration, currentPods []corev1.Pod) error {
	// Get all CheckpointBackup resources owned by this StatefulMigration
	var backupList migrationv1.CheckpointBackupList
	if err := r.List(ctx, &backupList, &client.ListOptions{
		Namespace: statefulMigration.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"stateful-migration": statefulMigration.Name,
		}),
	}); err != nil {
		return err
	}

	// Create a set of current pod names for quick lookup
	currentPodNames := make(map[string]bool)
	for _, pod := range currentPods {
		currentPodNames[pod.Name] = true
	}

	// Delete CheckpointBackup resources for pods that no longer exist
	for _, backup := range backupList.Items {
		podName, exists := backup.Labels["target-pod"]
		if !exists || !currentPodNames[podName] {
			if err := r.Delete(ctx, &backup); err != nil && !errors.IsNotFound(err) {
				return err
			}
		}
	}

	return nil
}

// deleteAllCheckpointBackups deletes all CheckpointBackup resources owned by the StatefulMigration
func (r *MigrationBackupReconciler) deleteAllCheckpointBackups(ctx context.Context, statefulMigration *migrationv1.StatefulMigration) error {
	var backupList migrationv1.CheckpointBackupList
	if err := r.List(ctx, &backupList, &client.ListOptions{
		Namespace: statefulMigration.Namespace,
		LabelSelector: labels.SelectorFromSet(map[string]string{
			"stateful-migration": statefulMigration.Name,
		}),
	}); err != nil {
		return err
	}

	for _, backup := range backupList.Items {
		if err := r.Delete(ctx, &backup); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MigrationBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1.StatefulMigration{}).
		Owns(&migrationv1.CheckpointBackup{}).
		Named("migrationbackup").
		Complete(r)
}
