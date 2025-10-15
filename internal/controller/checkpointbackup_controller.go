/*
Copyright 2025 Le Huan

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
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
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

	if r.RegistryClient == nil {
		registryClient, err := r.NewRegistryClient(ctx, backup.Spec.Registry)
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
		Timeout: 30 * time.Second,
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

// reconcileNormal handles the normal reconciliation logic
func (r *CheckpointBackupReconciler) reconcileNormal(ctx context.Context, backup *migrationv1.CheckpointBackup) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Schedule checkpoint creation based on the schedule
	backupKey := types.NamespacedName{
		Name:      backup.Name,
		Namespace: backup.Namespace,
	}.String()

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

	// Process each container
	for _, container := range backup.Spec.Containers {
		if err := r.checkpointContainer(ctx, backup, &pod, container); err != nil {
			log.Error(err, "Failed to checkpoint container", "container", container.Name)
			return err
		}
	}

	// Update status
	now := metav1.Now()
	backup.Status.LastCheckpointTime = &now
	backup.Status.Phase = "Completed"
	if err := r.Status().Update(ctx, backup); err != nil {
		log.Error(err, "Failed to update backup status")
		return err
	}

	log.Info("Successfully completed checkpoint operation", "backup", backup.Name)
	return nil
}

// checkpointContainer performs checkpoint operation for a single container
func (r *CheckpointBackupReconciler) checkpointContainer(ctx context.Context, backup *migrationv1.CheckpointBackup, pod *corev1.Pod, container migrationv1.Container) error {
	log := logf.FromContext(ctx)
	log.Info("Checkpointing container", "container", container.Name, "pod", pod.Name)

	// Step 1: Call kubelet checkpoint API
	checkpointPath, err := r.KubeletClient.CreateCheckpoint(backup.Spec.PodRef.Namespace, backup.Spec.PodRef.Name, container.Name)
	if err != nil {
		return fmt.Errorf("failed to create checkpoint via kubelet API: %w", err)
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

	// Step 3: Build checkpoint image using buildah
	if err := r.buildCheckpointImage(checkpointPath, container.Image, baseImage, container.Name); err != nil {
		return fmt.Errorf("failed to build checkpoint image: %w", err)
	}

	// Step 4: Push image to registry
	if err := r.RegistryClient.PushImage(container.Image); err != nil {
		return fmt.Errorf("failed to push checkpoint image: %w", err)
	}

	log.Info("Successfully checkpointed and pushed container image", "container", container.Name, "image", container.Image)
	return nil
}

// CreateCheckpoint calls kubelet checkpoint API
func (kc *KubeletClient) CreateCheckpoint(namespace, podName, containerName string) (string, error) {
	url := fmt.Sprintf("%s/checkpoint/%s/%s/%s?timeout=60", kc.kubeletURL, namespace, podName, containerName)

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

	// Push image
	cmd := exec.Command("buildah", "push", imageName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to push image %s: %w", imageName, err)
	}

	return nil
}

// login performs registry authentication
func (rc *RegistryClient) login(imageName string) error {
	// Use the registry URL from the secret (not extracted from image name)
	// For Docker Hub, this should be "docker.io" or can be empty

	// Login using buildah
	cmd := exec.Command("buildah", "login", "-u", rc.username, "-p", rc.password, rc.registry)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to login to registry %s: %w", rc.registry, err)
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
