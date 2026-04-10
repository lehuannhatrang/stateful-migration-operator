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

package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
)

// handleCreateCheckpoint creates a new CheckpointBackup CRD.
// The caller may provide podName directly or a kernelId to resolve the target pod.
func (s *Server) handleCreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req CreateCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	kernelID := ""
	if req.Metadata != nil {
		kernelID = req.Metadata.KernelID
	}

	if req.Namespace == "" {
		s.writeError(w, http.StatusBadRequest, "namespace is required", "")
		return
	}
	if req.PodName == "" && kernelID == "" {
		s.writeError(w, http.StatusBadRequest, "either podName or metadata.kernelId is required", "")
		return
	}

	// Resolve pod from kernelId if podName is not provided
	var resolvedPod *corev1.Pod
	if req.PodName == "" {
		pod, err := s.findPodByKernelID(r, req.Namespace, kernelID)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "failed to find pod by kernel ID", err.Error())
			return
		}
		if pod == nil {
			s.writeError(w, http.StatusNotFound,
				"no pod found with kernel_id label",
				fmt.Sprintf("no running pod with label kernel_id=%s in namespace %s", kernelID, req.Namespace))
			return
		}
		req.PodName = pod.Name
		resolvedPod = pod
		s.logger.Info("resolved pod from kernel ID", "kernelId", kernelID, "pod", pod.Name)
	}

	// Derive resourceRef from pod owner references when not provided
	if req.ResourceRef == nil {
		if resolvedPod == nil {
			var pod corev1.Pod
			if err := s.client.Get(r.Context(), types.NamespacedName{
				Namespace: req.Namespace,
				Name:      req.PodName,
			}, &pod); err != nil {
				s.writeError(w, http.StatusNotFound, "pod not found", err.Error())
				return
			}
			resolvedPod = &pod
		}
		ref := deriveResourceRef(resolvedPod)
		req.ResourceRef = &ref
	}

	schedule := req.Schedule
	if schedule == "" {
		schedule = "immediately"
	}

	checkpointName := req.Name
	if checkpointName == "" {
		checkpointName = fmt.Sprintf("checkpoint-%s-%s", req.PodName, metav1.Now().Format("20060102-150405"))
	}

	backup := &migrationv1.CheckpointBackup{
		ObjectMeta: metav1.ObjectMeta{
			Name:      checkpointName,
			Namespace: req.Namespace,
		},
		Spec: migrationv1.CheckpointBackupSpec{
			Schedule:   schedule,
			BuildImage: req.BuildImage,
			StopPod:    req.StopPod,
			PodRef: migrationv1.PodRef{
				Name:      req.PodName,
				Namespace: req.Namespace,
			},
			ResourceRef: migrationv1.ResourceRef{
				APIVersion: req.ResourceRef.APIVersion,
				Kind:       req.ResourceRef.Kind,
				Name:       req.ResourceRef.Name,
				Namespace:  req.ResourceRef.Namespace,
			},
		},
	}

	if req.Metadata != nil {
		backup.Spec.Metadata = &migrationv1.CheckpointMetadata{
			KernelID:     req.Metadata.KernelID,
			KernelName:   req.Metadata.KernelName,
			NotebookName: req.Metadata.NotebookName,
		}
		if req.Metadata.KernelID != "" {
			if backup.ObjectMeta.Labels == nil {
				backup.ObjectMeta.Labels = make(map[string]string)
			}
			backup.ObjectMeta.Labels["migration.dcnlab.com/kernel-id"] = req.Metadata.KernelID
		}
	}

	for _, c := range req.Containers {
		backup.Spec.Containers = append(backup.Spec.Containers, migrationv1.Container{
			Name:  c.Name,
			Image: c.Image,
		})
	}

	if req.Registry != nil {
		backup.Spec.Registry = &migrationv1.Registry{
			URL:        req.Registry.URL,
			Repository: req.Registry.Repository,
		}
		if req.Registry.SecretRef != nil {
			backup.Spec.Registry.SecretRef = &migrationv1.SecretRef{
				Name:      req.Registry.SecretRef.Name,
				Namespace: req.Registry.SecretRef.Namespace,
			}
		}
	}

	if err := s.client.Create(r.Context(), backup); err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to create checkpoint", err.Error())
		return
	}

	s.logger.Info("checkpoint created", "name", backup.Name, "namespace", backup.Namespace)
	s.writeJSON(w, http.StatusAccepted, toCheckpointResponse(backup))
}

// findPodByKernelID searches for a running pod in the given namespace with a "kernel_id" label
// matching the provided kernelID.
func (s *Server) findPodByKernelID(r *http.Request, namespace, kernelID string) (*corev1.Pod, error) {
	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList,
		client.InNamespace(namespace),
		client.MatchingLabelsSelector{
			Selector: labels.SelectorFromSet(labels.Set{"kernel_id": kernelID}),
		},
	); err != nil {
		return nil, fmt.Errorf("failed to list pods with kernel_id=%s: %w", kernelID, err)
	}

	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
			return pod, nil
		}
	}

	// Fall back to any non-deleted pod if none are Running
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.DeletionTimestamp == nil {
			return pod, nil
		}
	}

	return nil, nil
}

// deriveResourceRef attempts to build a ResourceRefRequest from the pod's owner references.
func deriveResourceRef(pod *corev1.Pod) ResourceRefRequest {
	for _, owner := range pod.OwnerReferences {
		return ResourceRefRequest{
			APIVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Name:       owner.Name,
			Namespace:  pod.Namespace,
		}
	}
	return ResourceRefRequest{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		Namespace:  pod.Namespace,
	}
}

// handleListCheckpoints lists CheckpointBackup CRDs, optionally filtered by namespace.
func (s *Server) handleListCheckpoints(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")

	var listOpts []client.ListOption
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}

	var backupList migrationv1.CheckpointBackupList
	if err := s.client.List(r.Context(), &backupList, listOpts...); err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to list checkpoints", err.Error())
		return
	}

	items := make([]CheckpointResponse, 0, len(backupList.Items))
	for i := range backupList.Items {
		items = append(items, toCheckpointResponse(&backupList.Items[i]))
	}

	s.writeJSON(w, http.StatusOK, CheckpointListResponse{
		Items:      items,
		TotalCount: len(items),
	})
}

// handleGetCheckpoint returns a single CheckpointBackup by namespace/name.
func (s *Server) handleGetCheckpoint(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	var backup migrationv1.CheckpointBackup
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &backup); err != nil {
		s.writeError(w, http.StatusNotFound, "checkpoint not found", err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, toCheckpointResponse(&backup))
}

// handleUpdateCheckpoint updates a CheckpointBackup CRD.
func (s *Server) handleUpdateCheckpoint(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	var req UpdateCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	var backup migrationv1.CheckpointBackup
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &backup); err != nil {
		s.writeError(w, http.StatusNotFound, "checkpoint not found", err.Error())
		return
	}

	if req.Schedule != nil {
		backup.Spec.Schedule = *req.Schedule
	}
	if req.StopPod != nil {
		backup.Spec.StopPod = req.StopPod
	}
	if req.BuildImage != nil {
		backup.Spec.BuildImage = req.BuildImage
	}
	if req.Containers != nil {
		backup.Spec.Containers = nil
		for _, c := range req.Containers {
			backup.Spec.Containers = append(backup.Spec.Containers, migrationv1.Container{
				Name:  c.Name,
				Image: c.Image,
			})
		}
	}
	if req.Registry != nil {
		backup.Spec.Registry = &migrationv1.Registry{
			URL:        req.Registry.URL,
			Repository: req.Registry.Repository,
		}
		if req.Registry.SecretRef != nil {
			backup.Spec.Registry.SecretRef = &migrationv1.SecretRef{
				Name:      req.Registry.SecretRef.Name,
				Namespace: req.Registry.SecretRef.Namespace,
			}
		}
	}

	if err := s.client.Update(r.Context(), &backup); err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to update checkpoint", err.Error())
		return
	}

	s.logger.Info("checkpoint updated", "name", name, "namespace", namespace)
	s.writeJSON(w, http.StatusOK, toCheckpointResponse(&backup))
}

// handleDeleteCheckpoint deletes a CheckpointBackup CRD.
func (s *Server) handleDeleteCheckpoint(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	var backup migrationv1.CheckpointBackup
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &backup); err != nil {
		s.writeError(w, http.StatusNotFound, "checkpoint not found", err.Error())
		return
	}

	if err := s.client.Delete(r.Context(), &backup); err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to delete checkpoint", err.Error())
		return
	}

	s.logger.Info("checkpoint deleted", "name", name, "namespace", namespace)
	s.writeJSON(w, http.StatusOK, MessageResponse{
		Message: fmt.Sprintf("checkpoint %s/%s deleted", namespace, name),
	})
}

// handleRestoreCheckpoint triggers a restore by creating a placeholder CRD or annotation.
// For now, this updates the checkpoint with a restore annotation that a restore controller can watch.
func (s *Server) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request) {
	namespace := r.PathValue("namespace")
	name := r.PathValue("name")

	var req RestoreCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow empty body for simple restore triggers
		req = RestoreCheckpointRequest{}
	}

	var backup migrationv1.CheckpointBackup
	if err := s.client.Get(r.Context(), types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, &backup); err != nil {
		s.writeError(w, http.StatusNotFound, "checkpoint not found", err.Error())
		return
	}

	if backup.Annotations == nil {
		backup.Annotations = make(map[string]string)
	}
	backup.Annotations["migration.dcnlab.com/restore-requested"] = metav1.Now().Format(metav1.RFC3339Micro)
	if req.TargetNamespace != "" {
		backup.Annotations["migration.dcnlab.com/restore-target-namespace"] = req.TargetNamespace
	}
	if req.TargetPodName != "" {
		backup.Annotations["migration.dcnlab.com/restore-target-pod"] = req.TargetPodName
	}

	if err := s.client.Update(r.Context(), &backup); err != nil {
		s.writeError(w, http.StatusInternalServerError, "failed to trigger restore", err.Error())
		return
	}

	s.logger.Info("restore triggered", "name", name, "namespace", namespace)
	s.writeJSON(w, http.StatusAccepted, MessageResponse{
		Message: fmt.Sprintf("restore triggered for checkpoint %s/%s", namespace, name),
	})
}

// toCheckpointResponse converts a CheckpointBackup CRD to the API response type.
func toCheckpointResponse(backup *migrationv1.CheckpointBackup) CheckpointResponse {
	resp := CheckpointResponse{
		Name:       backup.Name,
		Namespace:  backup.Namespace,
		Phase:      backup.Status.Phase,
		Message:    backup.Status.Message,
		Schedule:   backup.Spec.Schedule,
		BuildImage: backup.Spec.BuildImage,
		StopPod:    backup.Spec.StopPod,
		PodRef: PodRefResponse{
			Name:      backup.Spec.PodRef.Name,
			Namespace: backup.Spec.PodRef.Namespace,
		},
		ResourceRef: ResourceRefResponse{
			APIVersion: backup.Spec.ResourceRef.APIVersion,
			Kind:       backup.Spec.ResourceRef.Kind,
			Name:       backup.Spec.ResourceRef.Name,
			Namespace:  backup.Spec.ResourceRef.Namespace,
		},
		CreatedAt: backup.CreationTimestamp.Time,
	}

	if backup.Spec.Metadata != nil {
		resp.Metadata = &CheckpointMetadataResp{
			KernelID:     backup.Spec.Metadata.KernelID,
			KernelName:   backup.Spec.Metadata.KernelName,
			NotebookName: backup.Spec.Metadata.NotebookName,
		}
	}

	if backup.Status.LastCheckpointTime != nil {
		t := backup.Status.LastCheckpointTime.Time
		resp.LastCheckpointTime = &t
	}

	for _, cf := range backup.Status.CheckpointFiles {
		entry := CheckpointFileResp{
			ContainerName: cf.ContainerName,
			FilePath:      cf.FilePath,
			StoragePath:   cf.StoragePath,
		}
		if cf.CheckpointTime != nil {
			t := cf.CheckpointTime.Time
			entry.CheckpointTime = &t
		}
		resp.CheckpointFiles = append(resp.CheckpointFiles, entry)
	}

	for _, bi := range backup.Status.BuiltImages {
		entry := BuiltImageResp{
			ContainerName: bi.ContainerName,
			ImageName:     bi.ImageName,
			Pushed:        bi.Pushed,
		}
		if bi.BuildTime != nil {
			t := bi.BuildTime.Time
			entry.BuildTime = &t
		}
		resp.BuiltImages = append(resp.BuiltImages, entry)
	}

	return resp
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.Error("failed to encode response", "error", err)
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, errMsg, detail string) {
	s.writeJSON(w, status, ErrorResponse{
		Error:   errMsg,
		Message: detail,
		Code:    status,
	})
}
