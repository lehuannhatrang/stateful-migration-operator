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
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
)

const (
	CheckpointBackupFinalizer = "checkpointbackup.migration.dcnlab.com/finalizer"
	CheckpointBasePath        = "/var/lib/kubelet/checkpoints"
	ServiceAccountPath        = "/var/run/secrets/kubernetes.io/serviceaccount"

	// Phase constants
	PhaseCheckpointing       = "Checkpointing"
	PhaseCheckpointed        = "Checkpointed"
	PhaseImageBuilding       = "ImageBuilding"
	PhaseImageBuilt          = "ImageBuilt"
	PhaseImagePushing        = "ImagePushing"
	PhaseImagePushed         = "ImagePushed"
	PhaseCompleted           = "Completed"
	PhaseCompletedPodDeleted = "CompletedPodDeleted"
	PhaseCompletedWithError  = "CompletedWithError"
	PhaseFailed              = "Failed"
)

// CheckpointResponse represents the response from kubelet checkpoint API
// The actual response format contains an "items" array with checkpoint file paths
type CheckpointResponse struct {
	Items []string `json:"items"`
}

// CheckpointBackupReconciler reconciles a CheckpointBackup object
type CheckpointBackupReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	NodeName       string
	KubeletClient  *KubeletClient
	RegistryClient *RegistryClient
	Scheduler      *cron.Cron
	scheduledJobs  map[string]cron.EntryID // Track scheduled jobs
}

// KubeletClient handles communication with kubelet API
type KubeletClient struct {
	httpClient *http.Client
	token      string
	kubeletURL string
}

// RegistryClient handles container registry operations
type RegistryClient struct {
	username string
	password string
	registry string
}

// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=checkpointbackups/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods/checkpoint,verbs=patch;create;update;proxy
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *CheckpointBackupReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the CheckpointBackup instance
	var checkpointBackup migrationv1.CheckpointBackup
	if err := r.Get(ctx, req.NamespacedName, &checkpointBackup); err != nil {
		if errors.IsNotFound(err) {
			log.Info("CheckpointBackup resource not found. Ignoring since object must be deleted")
			// Clean up any scheduled job
			if r.scheduledJobs != nil {
				if entryID, exists := r.scheduledJobs[req.NamespacedName.String()]; exists {
					r.Scheduler.Remove(entryID)
					delete(r.scheduledJobs, req.NamespacedName.String())
				}
			}
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get CheckpointBackup")
		return ctrl.Result{}, err
	}

	// Initialize clients if not already done
	if err := r.initializeClients(ctx, &checkpointBackup); err != nil {
		log.Error(err, "Failed to initialize clients")
		return ctrl.Result{}, err
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&checkpointBackup, CheckpointBackupFinalizer) {
		controllerutil.AddFinalizer(&checkpointBackup, CheckpointBackupFinalizer)
		if err := r.Update(ctx, &checkpointBackup); err != nil {
			log.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Handle deletion
	if checkpointBackup.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, &checkpointBackup)
	}

	// Check if pod was already deleted (when stopPod was used)
	if checkpointBackup.Status.Phase == PhaseCompletedPodDeleted {
		log.Info("Pod was already deleted after checkpoint, no further action needed", "backup", checkpointBackup.Name)
		return ctrl.Result{}, nil
	}

	// Check if the pod is on this node
	isOnThisNode, err := r.isPodOnThisNode(ctx, &checkpointBackup)
	if err != nil {
		log.Error(err, "Failed to check if pod is on this node")
		return ctrl.Result{}, err
	}

	if !isOnThisNode {
		log.Info("Pod is not on this node, skipping", "pod", checkpointBackup.Spec.PodRef.Name, "node", r.NodeName)
		return ctrl.Result{}, nil
	}

	// Handle normal reconciliation
	return r.reconcileNormal(ctx, &checkpointBackup)
}

// initializeClients initializes the kubelet and registry clients
func (r *CheckpointBackupReconciler) initializeClients(ctx context.Context, backup *migrationv1.CheckpointBackup) error {
	if r.KubeletClient == nil {
		kubeletClient, err := NewKubeletClient()
		if err != nil {
			return fmt.Errorf("failed to create kubelet client: %w", err)
		}
		r.KubeletClient = kubeletClient
	}

	if r.RegistryClient == nil && backup.Spec.Registry != nil {
		registryClient, err := r.NewRegistryClient(ctx, *backup.Spec.Registry)
		if err != nil {
			return fmt.Errorf("failed to create registry client: %w", err)
		}
		r.RegistryClient = registryClient
	}

	if r.Scheduler == nil {
		r.Scheduler = cron.New()
		r.Scheduler.Start()
		r.scheduledJobs = make(map[string]cron.EntryID)
	}

	return nil
}

// NewKubeletClient creates a new kubelet client
func NewKubeletClient() (*KubeletClient, error) {
	// Read service account token
	tokenBytes, err := os.ReadFile(filepath.Join(ServiceAccountPath, "token"))
	if err != nil {
		return nil, fmt.Errorf("failed to read service account token: %w", err)
	}

	// Get node IP from environment or use localhost
	kubeletURL := "https://localhost:10250"
	if nodeIP := os.Getenv("NODE_IP"); nodeIP != "" {
		kubeletURL = fmt.Sprintf("https://%s:10250", nodeIP)
	}

	// Create HTTP client with TLS config (skip verification for kubelet)
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 300 * time.Second,
	}

	return &KubeletClient{
		httpClient: httpClient,
		token:      string(tokenBytes),
		kubeletURL: kubeletURL,
	}, nil
}

// NewRegistryClient creates a new registry client using the registry configuration from CheckpointBackup
func (r *CheckpointBackupReconciler) NewRegistryClient(ctx context.Context, registryConfig migrationv1.Registry) (*RegistryClient, error) {
	// Determine secret name and namespace
	secretName := "registry-credentials"    // default fallback
	secretNamespace := "stateful-migration" // default fallback

	if registryConfig.SecretRef != nil {
		secretName = registryConfig.SecretRef.Name
		if registryConfig.SecretRef.Namespace != "" {
			secretNamespace = registryConfig.SecretRef.Namespace
		}
	}

	// Get registry credentials from secret
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("failed to get registry credentials secret %s/%s: %w", secretNamespace, secretName, err)
	}

	username := string(secret.Data["username"])
	password := string(secret.Data["password"])
	registry := string(secret.Data["registry"])

	if username == "" || password == "" {
		return nil, fmt.Errorf("registry credentials are empty in secret %s/%s", secretNamespace, secretName)
	}

	// Use registry URL from configuration, fall back to secret data, then default
	registryURL := registryConfig.URL
	if registryURL == "" && registry != "" {
		registryURL = registry
	}
	if registryURL == "" {
		registryURL = "docker.io" // Default to Docker Hub if no registry specified
	}

	return &RegistryClient{
		username: username,
		password: password,
		registry: registryURL,
	}, nil
}

// isPodOnThisNode checks if the pod referenced in CheckpointBackup is on this node
func (r *CheckpointBackupReconciler) isPodOnThisNode(ctx context.Context, backup *migrationv1.CheckpointBackup) (bool, error) {
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{
		Name:      backup.Spec.PodRef.Name,
		Namespace: backup.Spec.PodRef.Namespace,
	}, &pod); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	return pod.Spec.NodeName == r.NodeName, nil
}

// shouldStopPod returns true if the pod should be deleted after checkpointing
func (r *CheckpointBackupReconciler) shouldStopPod(backup *migrationv1.CheckpointBackup) bool {
	return backup.Spec.StopPod != nil && *backup.Spec.StopPod
}

// getCheckpointFilePath returns the checkpoint file path from status if it exists
func (r *CheckpointBackupReconciler) getCheckpointFilePath(backup *migrationv1.CheckpointBackup, containerName string) (string, bool) {
	for _, checkpointFile := range backup.Status.CheckpointFiles {
		if checkpointFile.ContainerName == containerName {
			return checkpointFile.FilePath, true
		}
	}
	return "", false
}

// updatePhase updates the phase and message in the backup status with retry on conflict
func (r *CheckpointBackupReconciler) updatePhase(ctx context.Context, backup *migrationv1.CheckpointBackup, phase, message string) error {
	// Use retry logic to handle conflicts
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		// Get the latest version of the backup to avoid conflicts
		var latestBackup migrationv1.CheckpointBackup
		if err := r.Get(ctx, types.NamespacedName{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		}, &latestBackup); err != nil {
			return fmt.Errorf("failed to get latest backup: %w", err)
		}

		// Update phase and message
		latestBackup.Status.Phase = phase
		latestBackup.Status.Message = message

		// Update the status
		if err := r.Status().Update(ctx, &latestBackup); err != nil {
			if errors.IsConflict(err) && i < maxRetries-1 {
				// Conflict detected, retry after a short delay
				time.Sleep(time.Millisecond * 100 * time.Duration(i+1))
				continue
			}
			return fmt.Errorf("failed to update backup status: %w", err)
		}

		// Update succeeded, also update the passed-in backup object to keep it in sync
		backup.Status.Phase = phase
		backup.Status.Message = message
		return nil
	}

	return fmt.Errorf("failed to update backup status after %d retries", maxRetries)
}

// deleteCheckpointFile deletes a checkpoint file from disk
func (r *CheckpointBackupReconciler) deleteCheckpointFile(checkpointPath string) error {
	if _, err := os.Stat(checkpointPath); os.IsNotExist(err) {
		// File doesn't exist, nothing to delete
		return nil
	}

	if err := os.Remove(checkpointPath); err != nil {
		return fmt.Errorf("failed to remove checkpoint file %s: %w", checkpointPath, err)
	}

	return nil
}

// recordCheckpointFile adds the checkpoint file information to the backup status with retry on conflict
func (r *CheckpointBackupReconciler) recordCheckpointFile(ctx context.Context, backup *migrationv1.CheckpointBackup, containerName, checkpointPath string) error {
	// Use retry logic to handle conflicts
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		// Get the latest version of the backup to avoid conflicts
		var latestBackup migrationv1.CheckpointBackup
		if err := r.Get(ctx, types.NamespacedName{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		}, &latestBackup); err != nil {
			return fmt.Errorf("failed to get latest backup: %w", err)
		}

		// Check if this checkpoint file is already recorded (avoid duplicates)
		alreadyRecorded := false
		for _, checkpointFile := range latestBackup.Status.CheckpointFiles {
			if checkpointFile.ContainerName == containerName && checkpointFile.FilePath == checkpointPath {
				// Checkpoint file already recorded, no need to add again
				alreadyRecorded = true
				break
			}
		}

		if alreadyRecorded {
			return nil
		}

		// Add the new checkpoint file
		now := metav1.Now()
		newCheckpointFile := migrationv1.CheckpointFile{
			ContainerName:  containerName,
			FilePath:       checkpointPath,
			CheckpointTime: &now,
		}

		latestBackup.Status.CheckpointFiles = append(latestBackup.Status.CheckpointFiles, newCheckpointFile)

		// Update the status
		if err := r.Status().Update(ctx, &latestBackup); err != nil {
			if errors.IsConflict(err) && i < maxRetries-1 {
				// Conflict detected, retry after a short delay
				time.Sleep(time.Millisecond * 100 * time.Duration(i+1))
				continue
			}
			return fmt.Errorf("failed to update backup status with checkpoint file: %w", err)
		}

		// Update succeeded, also update the passed-in backup object to keep it in sync
		backup.Status.CheckpointFiles = latestBackup.Status.CheckpointFiles
		return nil
	}

	return fmt.Errorf("failed to record checkpoint file after %d retries", maxRetries)
}

// recordBuiltImage adds the built image information to the backup status with retry on conflict
func (r *CheckpointBackupReconciler) recordBuiltImage(ctx context.Context, backup *migrationv1.CheckpointBackup, containerName, imageName string, pushed bool) error {
	// Use retry logic to handle conflicts
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		// Get the latest version of the backup to avoid conflicts
		var latestBackup migrationv1.CheckpointBackup
		if err := r.Get(ctx, types.NamespacedName{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		}, &latestBackup); err != nil {
			return fmt.Errorf("failed to get latest backup: %w", err)
		}

		// Check if this image is already recorded (avoid duplicates)
		alreadyRecorded := false
		for _, builtImage := range latestBackup.Status.BuiltImages {
			if builtImage.ContainerName == containerName && builtImage.ImageName == imageName {
				// Image already recorded, no need to add again
				alreadyRecorded = true
				break
			}
		}

		if alreadyRecorded {
			return nil
		}

		// Add the new built image
		now := metav1.Now()
		newBuiltImage := migrationv1.BuiltImage{
			ContainerName: containerName,
			ImageName:     imageName,
			BuildTime:     &now,
			Pushed:        pushed,
		}

		latestBackup.Status.BuiltImages = append(latestBackup.Status.BuiltImages, newBuiltImage)

		// Update the status
		if err := r.Status().Update(ctx, &latestBackup); err != nil {
			if errors.IsConflict(err) && i < maxRetries-1 {
				// Conflict detected, retry after a short delay
				time.Sleep(time.Millisecond * 100 * time.Duration(i+1))
				continue
			}
			return fmt.Errorf("failed to update backup status with built image: %w", err)
		}

		// Update succeeded, also update the passed-in backup object to keep it in sync
		backup.Status.BuiltImages = latestBackup.Status.BuiltImages
		return nil
	}

	return fmt.Errorf("failed to record built image after %d retries", maxRetries)
}

// reconcileNormal handles the normal reconciliation logic
func (r *CheckpointBackupReconciler) reconcileNormal(ctx context.Context, backup *migrationv1.CheckpointBackup) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	backupKey := types.NamespacedName{
		Name:      backup.Name,
		Namespace: backup.Namespace,
	}.String()

	// Handle "immediately" schedule - perform checkpoint once and mark as completed
	if backup.Spec.Schedule == "immediately" {
		// Check if we've already started or completed processing
		// Skip if we're in any phase (already started) or if LastCheckpointTime is set
		if backup.Status.Phase != "" {
			// Already processing or completed
			if backup.Status.Phase == PhaseCompleted || backup.Status.Phase == PhaseCompletedPodDeleted {
				log.Info("Immediate checkpoint already completed",
					"backup", backup.Name,
					"phase", backup.Status.Phase)
			} else if backup.Status.Phase == PhaseFailed || backup.Status.Phase == PhaseCompletedWithError {
				log.Info("Immediate checkpoint previously failed",
					"backup", backup.Name,
					"phase", backup.Status.Phase,
					"message", backup.Status.Message)
			} else {
				// In progress (Checkpointing, ImageBuilding, etc.)
				log.Info("Immediate checkpoint already in progress",
					"backup", backup.Name,
					"phase", backup.Status.Phase)
			}
			return ctrl.Result{}, nil
		}

		// No phase set yet - this is the first time, proceed with checkpoint
		log.Info("Starting immediate checkpoint for the first time", "backup", backup.Name)

		// Perform immediate checkpoint
		if err := r.performCheckpoint(ctx, backup); err != nil {
			log.Error(err, "Failed to perform immediate checkpoint")
			return ctrl.Result{}, err
		}

		log.Info("Immediate checkpoint completed", "backup", backup.Name)
		return ctrl.Result{}, nil
	}

	// Handle regular cron schedule
	// Remove existing job if schedule changed
	if entryID, exists := r.scheduledJobs[backupKey]; exists {
		r.Scheduler.Remove(entryID)
		delete(r.scheduledJobs, backupKey)
	}

	// Add new scheduled job
	entryID, err := r.Scheduler.AddFunc(backup.Spec.Schedule, func() {
		if err := r.performCheckpoint(context.Background(), backup); err != nil {
			log.Error(err, "Failed to perform checkpoint", "backup", backup.Name)
		}
	})
	if err != nil {
		log.Error(err, "Failed to schedule checkpoint job")
		return ctrl.Result{}, err
	}

	r.scheduledJobs[backupKey] = entryID
	log.Info("Scheduled checkpoint job", "backup", backup.Name, "schedule", backup.Spec.Schedule)

	// Also perform immediate checkpoint on first reconcile
	if backup.Status.LastCheckpointTime == nil {
		if err := r.performCheckpoint(ctx, backup); err != nil {
			log.Error(err, "Failed to perform initial checkpoint")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: time.Hour}, nil
}

// reconcileDelete handles the deletion logic
func (r *CheckpointBackupReconciler) reconcileDelete(ctx context.Context, backup *migrationv1.CheckpointBackup) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Remove scheduled job
	backupKey := types.NamespacedName{
		Name:      backup.Name,
		Namespace: backup.Namespace,
	}.String()

	if entryID, exists := r.scheduledJobs[backupKey]; exists {
		r.Scheduler.Remove(entryID)
		delete(r.scheduledJobs, backupKey)
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(backup, CheckpointBackupFinalizer)
	if err := r.Update(ctx, backup); err != nil {
		log.Error(err, "Failed to remove finalizer")
		return ctrl.Result{}, err
	}

	log.Info("Successfully deleted CheckpointBackup", "name", backup.Name)
	return ctrl.Result{}, nil
}

// performCheckpoint performs the actual checkpoint operation
func (r *CheckpointBackupReconciler) performCheckpoint(ctx context.Context, backup *migrationv1.CheckpointBackup) error {
	log := logf.FromContext(ctx)

	// Check if already completed or in terminal state to avoid re-running
	if backup.Status.Phase == PhaseCompleted ||
		backup.Status.Phase == PhaseCompletedPodDeleted ||
		backup.Status.Phase == PhaseCompletedWithError {
		log.Info("Checkpoint already in terminal state, skipping",
			"backup", backup.Name,
			"phase", backup.Status.Phase)
		return nil
	}

	// Check if already in progress (shouldn't happen, but defensive check)
	if backup.Status.Phase == PhaseCheckpointing ||
		backup.Status.Phase == PhaseCheckpointed ||
		backup.Status.Phase == PhaseImageBuilding ||
		backup.Status.Phase == PhaseImageBuilt ||
		backup.Status.Phase == PhaseImagePushing ||
		backup.Status.Phase == PhaseImagePushed {
		log.Info("Checkpoint already in progress, skipping duplicate",
			"backup", backup.Name,
			"phase", backup.Status.Phase)
		return nil
	}

	log.Info("Starting checkpoint operation", "backup", backup.Name, "pod", backup.Spec.PodRef.Name)

	// Get pod to ensure it exists and is ready
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{
		Name:      backup.Spec.PodRef.Name,
		Namespace: backup.Spec.PodRef.Namespace,
	}, &pod); err != nil {
		return fmt.Errorf("failed to get pod: %w", err)
	}

	if pod.Status.Phase != corev1.PodRunning {
		log.Info("Pod is not running, skipping checkpoint", "pod", pod.Name, "phase", pod.Status.Phase)
		return nil
	}

	// Process containers - if none specified and no registry, checkpoint all containers in pod
	containersToProcess := backup.Spec.Containers
	if len(containersToProcess) == 0 && backup.Spec.Registry == nil {
		// Auto-generate container specs for all containers in the pod
		for _, podContainer := range pod.Spec.Containers {
			containersToProcess = append(containersToProcess, migrationv1.Container{
				Name:  podContainer.Name,
				Image: "", // Will be generated in checkpointContainer
			})
		}
		log.Info("No containers specified and no registry provided, checkpointing all pod containers",
			"containerCount", len(containersToProcess))
	}

	// Process each container
	for _, container := range containersToProcess {
		if err := r.checkpointContainer(ctx, backup, &pod, container); err != nil {
			log.Error(err, "Failed to checkpoint container", "container", container.Name)
			return err
		}
	}

	// Update status: Completed
	now := metav1.Now()
	backup.Status.LastCheckpointTime = &now
	if err := r.updatePhase(ctx, backup, PhaseCompleted, "All containers checkpointed successfully"); err != nil {
		log.Error(err, "Failed to update phase to Completed")
		return err
	}

	// Handle stopPod logic - delete the pod after successful checkpoint
	if r.shouldStopPod(backup) {
		log.Info("StopPod is enabled, deleting pod after checkpoint", "pod", backup.Spec.PodRef.Name)

		if err := r.Delete(ctx, &pod); err != nil {
			log.Error(err, "Failed to delete pod after checkpoint", "pod", pod.Name)
			// Update status to reflect the error
			if updateErr := r.updatePhase(ctx, backup, PhaseCompletedWithError,
				fmt.Sprintf("Checkpoint completed but failed to delete pod: %v", err)); updateErr != nil {
				log.Error(updateErr, "Failed to update backup status after pod deletion error")
			}
			return err
		}

		// Remove any scheduled jobs since pod is deleted and no further checkpoints are needed
		backupKey := types.NamespacedName{
			Name:      backup.Name,
			Namespace: backup.Namespace,
		}.String()

		if entryID, exists := r.scheduledJobs[backupKey]; exists {
			r.Scheduler.Remove(entryID)
			delete(r.scheduledJobs, backupKey)
			log.Info("Removed scheduled job after pod deletion", "backup", backup.Name)
		}

		// Update status to reflect pod deletion
		if err := r.updatePhase(ctx, backup, PhaseCompletedPodDeleted, "Checkpoint completed and pod deleted successfully"); err != nil {
			log.Error(err, "Failed to update backup status after pod deletion")
			return err
		}

		log.Info("Successfully deleted pod after checkpoint", "pod", pod.Name)
	}

	log.Info("Successfully completed checkpoint operation", "backup", backup.Name)
	return nil
}

// checkpointContainer performs checkpoint operation for a single container
func (r *CheckpointBackupReconciler) checkpointContainer(ctx context.Context, backup *migrationv1.CheckpointBackup, pod *corev1.Pod, container migrationv1.Container) error {
	log := logf.FromContext(ctx)
	log.Info("Checkpointing container", "container", container.Name, "pod", pod.Name)

	var checkpointPath string
	var err error

	// Check if checkpoint file already exists in status
	if existingPath, found := r.getCheckpointFilePath(backup, container.Name); found {
		log.Info("Checkpoint file already exists in status, skipping checkpoint creation",
			"container", container.Name, "path", existingPath)
		checkpointPath = existingPath

		// Verify the file still exists on disk
		fullCheckpointPath := filepath.Join(CheckpointBasePath, checkpointPath)
		if _, err := os.Stat(fullCheckpointPath); os.IsNotExist(err) {
			// File doesn't exist - check if we've already built an image for this container
			// If image is already built, we don't need the checkpoint file anymore
			imageAlreadyBuilt := false
			for _, builtImage := range backup.Status.BuiltImages {
				if builtImage.ContainerName == container.Name {
					imageAlreadyBuilt = true
					log.Info("Image already built for container, checkpoint file was cleaned up",
						"container", container.Name,
						"image", builtImage.ImageName)
					break
				}
			}

			if imageAlreadyBuilt {
				// Image exists, checkpoint file was deleted - this is expected
				// Just return, no need to do anything
				log.Info("Skipping container as image already built", "container", container.Name)
				return nil
			} else {
				// Image not built yet, but checkpoint file is missing - need to recreate
				log.Info("Checkpoint file in status does not exist on disk, will recreate", "path", fullCheckpointPath)
				checkpointPath = ""
			}
		} else {
			log.Info("Checkpoint file verified on disk", "path", fullCheckpointPath)
		}
	}

	// If checkpoint doesn't exist or file is missing, create it
	if checkpointPath == "" {
		// Update status: Checkpointing
		if err := r.updatePhase(ctx, backup, PhaseCheckpointing, fmt.Sprintf("Creating checkpoint for container %s", container.Name)); err != nil {
			log.Error(err, "Failed to update phase to Checkpointing")
		}

		// Step 1: Call kubelet checkpoint API
		checkpointPath, err = r.KubeletClient.CreateCheckpoint(backup.Spec.PodRef.Namespace, backup.Spec.PodRef.Name, container.Name)
		if err != nil {
			if updateErr := r.updatePhase(ctx, backup, PhaseFailed, fmt.Sprintf("Failed to create checkpoint: %v", err)); updateErr != nil {
				log.Error(updateErr, "Failed to update phase to Failed")
			}
			return fmt.Errorf("failed to create checkpoint via kubelet API: %w", err)
		}

		// Record the checkpoint file in status
		if err := r.recordCheckpointFile(ctx, backup, container.Name, checkpointPath); err != nil {
			log.Error(err, "Failed to record checkpoint file", "container", container.Name, "path", checkpointPath)
			// Don't fail here, just log the error
		}

		// Update status: Checkpointed
		if err := r.updatePhase(ctx, backup, PhaseCheckpointed, fmt.Sprintf("Checkpoint created for container %s: %s", container.Name, checkpointPath)); err != nil {
			log.Error(err, "Failed to update phase to Checkpointed")
		}
	}

	// Step 1.5: Verify the checkpoint file exists (kubelet API should have returned the exact path)
	fullCheckpointPath := filepath.Join(CheckpointBasePath, checkpointPath)
	if _, err := os.Stat(fullCheckpointPath); os.IsNotExist(err) {
		// If the file doesn't exist, fall back to file search
		log.Info("Checkpoint file from API response not found, searching for alternative", "expectedPath", checkpointPath)
		actualCheckpointPath, err := r.findCheckpointFile(backup.Spec.PodRef.Namespace, backup.Spec.PodRef.Name, container.Name, checkpointPath)
		if err != nil {
			return fmt.Errorf("failed to find checkpoint file after creation: %w", err)
		}
		log.Info("Found alternative checkpoint file", "actualPath", actualCheckpointPath, "originalExpected", checkpointPath)
		checkpointPath = actualCheckpointPath
	} else {
		log.Info("Checkpoint file found as expected", "path", checkpointPath)
	}

	// Step 2: Get the original container image
	var baseImage string
	for _, c := range pod.Spec.Containers {
		if c.Name == container.Name {
			baseImage = c.Image
			break
		}
	}
	if baseImage == "" {
		return fmt.Errorf("could not find base image for container %s", container.Name)
	}

	// Step 3: Determine the image name to use
	imageName := container.Image
	if backup.Spec.Registry == nil || container.Image == "" {
		// If no registry is provided or no image specified, use localhost with a generated name
		imageName = fmt.Sprintf("localhost/checkpoint-%s-%s:%s",
			backup.Spec.PodRef.Name,
			container.Name,
			time.Now().Format("20060102-150405"))
		log.Info("Using localhost image", "image", imageName, "reason",
			map[bool]string{true: "no registry provided", false: "no image specified"}[backup.Spec.Registry == nil])
	}

	// Update status: Building image
	if err := r.updatePhase(ctx, backup, PhaseImageBuilding, fmt.Sprintf("Building checkpoint image for container %s", container.Name)); err != nil {
		log.Error(err, "Failed to update phase to ImageBuilding")
	}

	// Step 4: Build checkpoint image using buildah
	if err := r.buildCheckpointImage(checkpointPath, imageName, baseImage, container.Name); err != nil {
		if updateErr := r.updatePhase(ctx, backup, PhaseFailed, fmt.Sprintf("Failed to build image: %v", err)); updateErr != nil {
			log.Error(updateErr, "Failed to update phase to Failed")
		}
		return fmt.Errorf("failed to build checkpoint image: %w", err)
	}

	// Update status: Image built
	if err := r.updatePhase(ctx, backup, PhaseImageBuilt, fmt.Sprintf("Image built successfully for container %s: %s", container.Name, imageName)); err != nil {
		log.Error(err, "Failed to update phase to ImageBuilt")
	}

	// Step 5: Push image to registry (only if registry is configured)
	pushed := false
	if backup.Spec.Registry != nil && r.RegistryClient != nil {
		// Update status: Pushing image
		if err := r.updatePhase(ctx, backup, PhaseImagePushing, fmt.Sprintf("Pushing image %s to registry", imageName)); err != nil {
			log.Error(err, "Failed to update phase to ImagePushing")
		}

		if err := r.RegistryClient.PushImage(imageName); err != nil {
			if updateErr := r.updatePhase(ctx, backup, PhaseFailed, fmt.Sprintf("Failed to push image: %v", err)); updateErr != nil {
				log.Error(updateErr, "Failed to update phase to Failed")
			}
			return fmt.Errorf("failed to push checkpoint image: %w", err)
		}
		pushed = true

		// Update status: Image pushed
		if err := r.updatePhase(ctx, backup, PhaseImagePushed, fmt.Sprintf("Image pushed successfully: %s", imageName)); err != nil {
			log.Error(err, "Failed to update phase to ImagePushed")
		}
		log.Info("Successfully checkpointed and pushed container image", "container", container.Name, "image", imageName)
	} else {
		log.Info("Successfully checkpointed container image locally", "container", container.Name, "image", imageName)
	}

	// Step 6: Record the built image in the backup status
	if err := r.recordBuiltImage(ctx, backup, container.Name, imageName, pushed); err != nil {
		log.Error(err, "Failed to record built image", "container", container.Name, "image", imageName)
		// Don't return error here as the checkpoint was successful
	}

	// Step 7: Clean up checkpoint file after successful build and push (if configured)
	if backup.Spec.Registry == nil || pushed {
		// Delete checkpoint file if:
		// - No registry (localhost only), image is built
		// - Registry configured and image was pushed successfully
		if err := r.deleteCheckpointFile(fullCheckpointPath); err != nil {
			log.Error(err, "Failed to delete checkpoint file", "path", fullCheckpointPath)
			// Don't return error here, just log it
		} else {
			log.Info("Deleted checkpoint file after successful completion", "path", fullCheckpointPath)
		}
	}

	return nil
}

// CreateCheckpoint calls kubelet checkpoint API
func (kc *KubeletClient) CreateCheckpoint(namespace, podName, containerName string) (string, error) {
	url := fmt.Sprintf("%s/checkpoint/%s/%s/%s?timeout=300", kc.kubeletURL, namespace, podName, containerName)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+kc.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := kc.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call kubelet checkpoint API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("kubelet checkpoint API returned status %d: %s", resp.StatusCode, string(body))
	}

	// First, try to read the response body to see what we actually get
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read checkpoint response body: %w", err)
	}

	// Log the raw response for debugging
	fmt.Printf("DEBUG: Kubelet checkpoint API response body: %s\n", string(body))

	var checkpointResp CheckpointResponse
	if err := json.Unmarshal(body, &checkpointResp); err != nil {
		// If JSON parsing fails, fall back to file search by returning a placeholder
		responseText := strings.TrimSpace(string(body))
		fmt.Printf("DEBUG: Failed to parse JSON response from kubelet: %s\n", responseText)
		return "unknown-checkpoint-file", nil
	}

	if len(checkpointResp.Items) == 0 {
		// If JSON response doesn't contain any items, fall back to file search
		fmt.Printf("DEBUG: JSON response has no checkpoint items, falling back to file search\n")
		return "unknown-checkpoint-file", nil
	}

	// Use the first (and likely only) checkpoint path from the response
	checkpointPath := checkpointResp.Items[0]
	fmt.Printf("DEBUG: Successfully parsed JSON response, checkpoint path: %s\n", checkpointPath)

	// Convert absolute path to relative path (remove the base path prefix)
	if strings.HasPrefix(checkpointPath, CheckpointBasePath+"/") {
		relativePath := strings.TrimPrefix(checkpointPath, CheckpointBasePath+"/")
		fmt.Printf("DEBUG: Converted to relative path: %s\n", relativePath)
		return relativePath, nil
	} else if strings.HasPrefix(checkpointPath, "/var/lib/kubelet/checkpoints/") {
		relativePath := strings.TrimPrefix(checkpointPath, "/var/lib/kubelet/checkpoints/")
		fmt.Printf("DEBUG: Converted to relative path: %s\n", relativePath)
		return relativePath, nil
	}

	// If path doesn't have expected prefix, just return the filename
	relativePath := filepath.Base(checkpointPath)
	fmt.Printf("DEBUG: Using filename only: %s\n", relativePath)
	return relativePath, nil
}

// findCheckpointFile finds the most recent checkpoint file for a given pod and container
func (r *CheckpointBackupReconciler) findCheckpointFile(namespace, podName, containerName, expectedPath string) (string, error) {
	// First try the expected path
	fullExpectedPath := filepath.Join(CheckpointBasePath, expectedPath)
	if _, err := os.Stat(fullExpectedPath); err == nil {
		return expectedPath, nil
	}

	// If expected path doesn't exist, search for checkpoint files matching the pattern
	podFullName := fmt.Sprintf("%s_%s", namespace, podName)
	pattern := fmt.Sprintf("checkpoint-%s-%s-*.tar", podFullName, containerName)
	fullPattern := filepath.Join(CheckpointBasePath, pattern)

	fmt.Printf("DEBUG: Searching for checkpoint files with pattern: %s\n", fullPattern)
	matches, err := filepath.Glob(fullPattern)
	if err != nil {
		return "", fmt.Errorf("failed to search for checkpoint files with pattern %s: %w", pattern, err)
	}
	fmt.Printf("DEBUG: Found %d matching files: %v\n", len(matches), matches)

	if len(matches) == 0 {
		// List all files in checkpoint directory for debugging
		files, _ := os.ReadDir(CheckpointBasePath)
		var fileNames []string
		for _, file := range files {
			fileNames = append(fileNames, file.Name())
		}
		return "", fmt.Errorf("no checkpoint files found for pattern %s. Available files: %v", pattern, fileNames)
	}

	// Sort matches to get the most recent file (files are naturally sorted by timestamp)
	// Find the most recent file (look for files created in the last few seconds)
	var mostRecentFile string
	var mostRecentTime time.Time

	now := time.Now()
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}

		// Only consider files created in the last 30 seconds (recent checkpoint)
		if now.Sub(info.ModTime()) < 30*time.Second {
			if info.ModTime().After(mostRecentTime) {
				mostRecentTime = info.ModTime()
				mostRecentFile = match
			}
		}
	}

	if mostRecentFile == "" {
		// If no recent file found, use the lexicographically last one (likely most recent by timestamp)
		mostRecentFile = matches[len(matches)-1]
	}

	relativePath, _ := filepath.Rel(CheckpointBasePath, mostRecentFile)
	return relativePath, nil
}

// buildCheckpointImage builds the checkpoint image using buildah
func (r *CheckpointBackupReconciler) buildCheckpointImage(checkpointPath, imageName, baseImage, containerName string) error {
	log := logf.FromContext(context.Background())

	// Verify the checkpoint file exists (should have been found by findCheckpointFile)
	fullCheckpointPath := filepath.Join(CheckpointBasePath, checkpointPath)
	if _, err := os.Stat(fullCheckpointPath); os.IsNotExist(err) {
		return fmt.Errorf("checkpoint file does not exist: %s (this should not happen after findCheckpointFile)", fullCheckpointPath)
	}

	log.Info("Building checkpoint image", "checkpointFile", fullCheckpointPath, "imageName", imageName, "baseImage", baseImage)

	// Step 1: Create new container from scratch
	cmd := exec.Command("buildah", "from", "scratch")
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to create buildah container: %w", err)
	}
	newContainer := strings.TrimSpace(string(out))

	// Ensure cleanup
	defer func() {
		exec.Command("buildah", "rm", newContainer).Run()
	}()

	// Step 2: Add checkpoint tar to root
	if err := exec.Command("buildah", "add", newContainer, fullCheckpointPath, "/").Run(); err != nil {
		return fmt.Errorf("failed to add checkpoint to container (%s): %w", fullCheckpointPath, err)
	}

	// Step 3: Add CRI-O checkpoint annotations
	if err := exec.Command("buildah", "config",
		"--annotation=io.kubernetes.cri-o.annotations.checkpoint.name="+imageName,
		newContainer).Run(); err != nil {
		return fmt.Errorf("failed to add checkpoint name annotation: %w", err)
	}

	if err := exec.Command("buildah", "config",
		"--annotation=io.kubernetes.cri-o.annotations.checkpoint.rootfsImageName="+baseImage,
		newContainer).Run(); err != nil {
		return fmt.Errorf("failed to add rootfs image annotation: %w", err)
	}

	// Step 4: Commit and tag image
	if err := exec.Command("buildah", "commit", newContainer, imageName).Run(); err != nil {
		return fmt.Errorf("failed to commit image: %w", err)
	}

	log.Info("Successfully built checkpoint image", "image", imageName, "baseImage", baseImage)
	return nil
}

// PushImage pushes the image to the registry
func (rc *RegistryClient) PushImage(imageName string) error {
	// Login to registry
	if err := rc.login(imageName); err != nil {
		return fmt.Errorf("failed to login to registry: %w", err)
	}

	// Trim http:// or https:// prefix from registry URL
	registryURL := rc.registry
	registryURL = strings.TrimPrefix(registryURL, "http://")
	registryURL = strings.TrimPrefix(registryURL, "https://")

	// Construct destination image: <registry>/<image-name>
	destinationImage := registryURL + "/" + imageName

	// Push image: buildah push <local-image> <destination-image>
	cmd := exec.Command("buildah", "push", imageName, destinationImage)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push image %s to %s: %w", imageName, destinationImage, err)
	}

	return nil
}

// login performs registry authentication
func (rc *RegistryClient) login(imageName string) error {
	// Use the registry URL from the secret (not extracted from image name)
	// For Docker Hub, this should be "docker.io" or can be empty

	// Trim http:// or https:// prefix from registry URL
	registryURL := rc.registry
	registryURL = strings.TrimPrefix(registryURL, "http://")
	registryURL = strings.TrimPrefix(registryURL, "https://")

	// Login using buildah
	cmd := exec.Command("buildah", "login", "-u", rc.username, "-p", rc.password, registryURL)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to login to registry %s: %w", registryURL, err)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager
func (r *CheckpointBackupReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Get node name from environment
	r.NodeName = os.Getenv("NODE_NAME")
	if r.NodeName == "" {
		return fmt.Errorf("NODE_NAME environment variable is required")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1.CheckpointBackup{}).
		Named("checkpointbackup").
		Complete(r)
}
