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

import "time"

// CreateCheckpointRequest is the request body for triggering a new checkpoint.
// Either podName or kernelId must be provided. If kernelId is set, the API server
// resolves it to a pod by searching for a pod with a matching "kernel_id" label.
type CreateCheckpointRequest struct {
	Name        string              `json:"name,omitempty"`
	Namespace   string              `json:"namespace"`
	PodName     string              `json:"podName,omitempty"`
	KernelID    string              `json:"kernelId,omitempty"`
	ResourceRef *ResourceRefRequest `json:"resourceRef,omitempty"`
	Containers  []ContainerRequest  `json:"containers,omitempty"`
	BuildImage  *bool               `json:"buildImage,omitempty"`
	Schedule    string              `json:"schedule,omitempty"`
	StopPod     *bool               `json:"stopPod,omitempty"`
	Registry    *RegistryRequest    `json:"registry,omitempty"`
}

// ResourceRefRequest maps to the CRD ResourceRef.
type ResourceRefRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// ContainerRequest maps to the CRD Container spec.
type ContainerRequest struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

// RegistryRequest maps to the CRD Registry spec.
type RegistryRequest struct {
	URL        string            `json:"url"`
	Repository string            `json:"repository"`
	SecretRef  *SecretRefRequest `json:"secretRef,omitempty"`
}

// SecretRefRequest maps to the CRD SecretRef.
type SecretRefRequest struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// UpdateCheckpointRequest is the request body for updating a checkpoint.
type UpdateCheckpointRequest struct {
	Schedule   *string            `json:"schedule,omitempty"`
	StopPod    *bool              `json:"stopPod,omitempty"`
	BuildImage *bool              `json:"buildImage,omitempty"`
	Containers []ContainerRequest `json:"containers,omitempty"`
	Registry   *RegistryRequest   `json:"registry,omitempty"`
}

// RestoreCheckpointRequest is the request body for triggering a restore.
type RestoreCheckpointRequest struct {
	TargetNamespace string `json:"targetNamespace,omitempty"`
	TargetPodName   string `json:"targetPodName,omitempty"`
}

// CheckpointResponse is the API response for a single checkpoint.
type CheckpointResponse struct {
	Name               string               `json:"name"`
	Namespace          string               `json:"namespace"`
	Phase              string               `json:"phase,omitempty"`
	Message            string               `json:"message,omitempty"`
	Schedule           string               `json:"schedule"`
	BuildImage         *bool                `json:"buildImage,omitempty"`
	StopPod            *bool                `json:"stopPod,omitempty"`
	PodRef             PodRefResponse       `json:"podRef"`
	ResourceRef        ResourceRefResponse  `json:"resourceRef"`
	LastCheckpointTime *time.Time           `json:"lastCheckpointTime,omitempty"`
	CheckpointFiles    []CheckpointFileResp `json:"checkpointFiles,omitempty"`
	BuiltImages        []BuiltImageResp     `json:"builtImages,omitempty"`
	CreatedAt          time.Time            `json:"createdAt"`
}

// PodRefResponse is the pod reference in the API response.
type PodRefResponse struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// ResourceRefResponse is the resource reference in the API response.
type ResourceRefResponse struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

// CheckpointFileResp is a checkpoint file entry in the API response.
type CheckpointFileResp struct {
	ContainerName  string     `json:"containerName"`
	FilePath       string     `json:"filePath"`
	StoragePath    string     `json:"storagePath,omitempty"`
	CheckpointTime *time.Time `json:"checkpointTime,omitempty"`
}

// BuiltImageResp is a built image entry in the API response.
type BuiltImageResp struct {
	ContainerName string     `json:"containerName"`
	ImageName     string     `json:"imageName"`
	BuildTime     *time.Time `json:"buildTime,omitempty"`
	Pushed        bool       `json:"pushed"`
}

// CheckpointListResponse is the API response for listing checkpoints.
type CheckpointListResponse struct {
	Items      []CheckpointResponse `json:"items"`
	TotalCount int                  `json:"totalCount"`
}

// ErrorResponse is the standard error response.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
	Code    int    `json:"code"`
}

// MessageResponse is a simple success response with a message.
type MessageResponse struct {
	Message string `json:"message"`
}
