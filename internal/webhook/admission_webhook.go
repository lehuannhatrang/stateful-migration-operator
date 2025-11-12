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

package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
)

// PodMutator handles pod creation mutation
type PodMutator struct {
	Client client.Client
}

// Handle implements the admission.Handler interface
func (p *PodMutator) Handle(ctx context.Context, req admission.Request) admission.Response {
	log := logf.FromContext(ctx).WithName("pod-mutator")

	pod := &corev1.Pod{}
	decoder := admission.NewDecoder(p.Client.Scheme())
	if err := decoder.DecodeRaw(req.Object, pod); err != nil {
		log.Error(err, "Failed to decode pod")
		return admission.Errored(http.StatusBadRequest, err)
	}

	log.Info("Processing pod mutation", "pod", pod.Name, "namespace", pod.Namespace)

	// Check if this pod is created by a Job
	if !p.isPodFromJob(pod) {
		log.V(1).Info("Pod is not from a Job, skipping mutation")
		return admission.Allowed("Pod not from Job")
	}

	// Get the Job name from owner references
	jobName := p.getJobName(pod)
	if jobName == "" {
		log.V(1).Info("Could not determine Job name, skipping mutation")
		return admission.Allowed("Job name not found")
	}

	log.Info("Pod is from Job", "job", jobName, "pod", pod.Name)

	// Find matching CheckpointBackup CR based on resourceRef
	checkpointBackup, err := p.findMatchingCheckpointBackup(ctx, pod.Namespace, jobName)
	if err != nil {
		log.Error(err, "Failed to find matching CheckpointBackup")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if checkpointBackup == nil {
		log.V(1).Info("No matching CheckpointBackup found, skipping mutation")
		return admission.Allowed("No matching CheckpointBackup")
	}

	log.Info("Found matching CheckpointBackup", "backup", checkpointBackup.Name)

	// Apply image patches based on CheckpointBackup configuration
	patches, err := p.generateImagePatches(ctx, pod, checkpointBackup)
	if err != nil {
		log.Error(err, "Failed to generate image patches")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	if len(patches) == 0 {
		log.V(1).Info("No image patches needed")
		return admission.Allowed("No patches required")
	}

	// Create JSON patch response
	patchBytes, err := json.Marshal(patches)
	if err != nil {
		log.Error(err, "Failed to marshal patches")
		return admission.Errored(http.StatusInternalServerError, err)
	}

	log.Info("Applying image patches", "patches", string(patchBytes))
	return admission.PatchResponseFromRaw(req.Object.Raw, patchBytes)
}

// isPodFromJob checks if the pod is created by a Job
func (p *PodMutator) isPodFromJob(pod *corev1.Pod) bool {
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "Job" && ownerRef.APIVersion == "batch/v1" {
			return true
		}
	}
	return false
}

// getJobName extracts the Job name from owner references
func (p *PodMutator) getJobName(pod *corev1.Pod) string {
	for _, ownerRef := range pod.OwnerReferences {
		if ownerRef.Kind == "Job" && ownerRef.APIVersion == "batch/v1" {
			return ownerRef.Name
		}
	}
	return ""
}

// findMatchingCheckpointBackup finds a CheckpointBackup CR whose resourceRef matches the Job
func (p *PodMutator) findMatchingCheckpointBackup(ctx context.Context, namespace, jobName string) (*migrationv1.CheckpointBackup, error) {
	log := logf.FromContext(ctx)

	// List all CheckpointBackup CRs in the namespace
	var checkpointBackups migrationv1.CheckpointBackupList
	if err := p.Client.List(ctx, &checkpointBackups, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("failed to list CheckpointBackup CRs: %w", err)
	}

	// Check each CheckpointBackup for matching resourceRef
	for _, backup := range checkpointBackups.Items {
		log.V(1).Info("Checking CheckpointBackup",
			"backup", backup.Name,
			"resourceRef.Kind", backup.Spec.ResourceRef.Kind,
			"resourceRef.Name", backup.Spec.ResourceRef.Name,
			"jobName", jobName)

		// Check if resourceRef matches the Job
		if p.doesResourceRefMatchJob(backup.Spec.ResourceRef, jobName, namespace) {
			return &backup, nil
		}
	}

	return nil, nil
}

// doesResourceRefMatchJob checks if a resourceRef matches the given Job
func (p *PodMutator) doesResourceRefMatchJob(resourceRef migrationv1.ResourceRef, jobName, namespace string) bool {
	// Check if resourceRef points to a Job
	if resourceRef.Kind == "Job" && resourceRef.APIVersion == "batch/v1" {
		refNamespace := resourceRef.Namespace
		if refNamespace == "" {
			refNamespace = namespace
		}
		return resourceRef.Name == jobName && refNamespace == namespace
	}

	// Check if resourceRef points to a CronJob that created this Job
	if resourceRef.Kind == "CronJob" && resourceRef.APIVersion == "batch/v1" {
		// Job names from CronJob typically follow the pattern: <cronjob-name>-<timestamp>
		// or <cronjob-name>-<sequential-number>
		return strings.HasPrefix(jobName, resourceRef.Name+"-")
	}

	return false
}

// generateImagePatches creates JSON patches to modify container images
func (p *PodMutator) generateImagePatches(ctx context.Context, pod *corev1.Pod, backup *migrationv1.CheckpointBackup) ([]map[string]interface{}, error) {
	log := logf.FromContext(ctx)
	var patches []map[string]interface{}

	// Create a map of container name to image name for quick lookup
	imageMap := make(map[string]string)

	// First, try to get images from spec.containers
	for _, container := range backup.Spec.Containers {
		if container.Image != "" {
			imageMap[container.Name] = container.Image
			log.V(1).Info("Found image in spec", "container", container.Name, "image", container.Image)
		}
	}

	// If not found in spec, look in status.builtImages
	for _, builtImage := range backup.Status.BuiltImages {
		if _, exists := imageMap[builtImage.ContainerName]; !exists && builtImage.ImageName != "" {
			imageMap[builtImage.ContainerName] = builtImage.ImageName
			log.V(1).Info("Found image in status", "container", builtImage.ContainerName, "image", builtImage.ImageName)
		}
	}

	// Generate patches for each container in the pod
	for i, container := range pod.Spec.Containers {
		if newImage, exists := imageMap[container.Name]; exists {
			log.Info("Patching container image",
				"container", container.Name,
				"originalImage", container.Image,
				"newImage", newImage)

			patch := map[string]interface{}{
				"op":    "replace",
				"path":  fmt.Sprintf("/spec/containers/%d/image", i),
				"value": newImage,
			}
			patches = append(patches, patch)
		}
	}

	return patches, nil
}

// SetupPodMutator creates and configures the pod mutator webhook
func SetupPodMutator(mgr client.Client) *PodMutator {
	return &PodMutator{
		Client: mgr,
	}
}
