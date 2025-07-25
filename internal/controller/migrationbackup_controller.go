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
	Scheme        *runtime.Scheme
	KarmadaClient *KarmadaClient
}

// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=*,verbs=get;list;watch;update;patch

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

	// Step 3: For each source cluster, create/update CheckpointBackup resources for each pod
	for _, cluster := range statefulMigration.Spec.SourceClusters {
		for _, pod := range pods {
			if err := r.reconcileCheckpointBackupForPod(ctx, statefulMigration, &pod, cluster); err != nil {
				log.Error(err, "Failed to reconcile CheckpointBackup for pod", "pod", pod.Name, "cluster", cluster)
				return ctrl.Result{}, err
			}
		}
	}

	// Step 4: Clean up orphaned CheckpointBackup resources
	if err := r.cleanupOrphanedCheckpointBackups(ctx, statefulMigration, pods); err != nil {
		log.Error(err, "Failed to cleanup orphaned CheckpointBackup resources")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled StatefulMigration", "name", statefulMigration.Name)
	return ctrl.Result{RequeueAfter: time.Minute * 5}, nil
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
		var pod corev1.Pod
		if err := r.Get(ctx, types.NamespacedName{
			Name:      resourceRef.Name,
			Namespace: resourceRef.Namespace,
		}, &pod); err != nil {
			return nil, err
		}

		return []corev1.Pod{pod}, nil

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
