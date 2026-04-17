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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// CheckpointBackupSpec defines the desired state of CheckpointBackup
type CheckpointBackupSpec struct {
	// Schedule specifies the backup schedule in cron format or "immediately" for one-time execution
	// +required
	Schedule string `json:"schedule"`

	// StopPod specifies whether to delete the pod after checkpointing (default: false)
	// When true, the pod will be deleted after successful checkpoint creation and no further schedules will be processed
	// +optional
	StopPod *bool `json:"stopPod,omitempty"`

	// BuildImage specifies whether to build a container image from the checkpoint (default: true)
	// When false, only the checkpoint file is created and stored to checkpoint storage
	// +optional
	BuildImage *bool `json:"buildImage,omitempty"`

	// PodRef specifies the pod to checkpoint
	// +required
	PodRef PodRef `json:"podRef"`

	// ResourceRef specifies the workload to migrate
	// +required
	ResourceRef ResourceRef `json:"resourceRef"`

	// Registry specifies the registry configuration for storing checkpoints
	// If not provided, images will be built locally without pushing to a registry
	// +optional
	Registry *Registry `json:"registry,omitempty"`

	// Containers specifies the container configurations for checkpoints
	// +optional
	Containers []Container `json:"containers,omitempty"`

	// Metadata holds optional contextual information about the checkpoint origin
	// +optional
	Metadata *CheckpointMetadata `json:"metadata,omitempty"`
}

// BusyCell holds information about a notebook cell that was actively executing
// at the time the checkpoint was requested.
type BusyCell struct {
	// CellIndex is the zero-based position of the cell in the notebook
	// +optional
	CellIndex int `json:"cellIndex,omitempty"`

	// CellID is the unique identifier of the cell
	// +optional
	CellID string `json:"cellId,omitempty"`

	// MsgID is the kernel message ID associated with the cell execution
	// +optional
	MsgID string `json:"msgId,omitempty"`

	// ExecutionCount is the execution counter value shown next to the cell
	// +optional
	ExecutionCount int `json:"executionCount,omitempty"`
}

// CheckpointMetadata holds optional contextual information about the checkpoint origin,
// such as the Jupyter kernel or notebook it was created from.
type CheckpointMetadata struct {
	// KernelID is the Jupyter kernel ID associated with this checkpoint
	// +optional
	KernelID string `json:"kernelId,omitempty"`

	// KernelName is the human-readable name of the Jupyter kernel
	// +optional
	KernelName string `json:"kernelName,omitempty"`

	// NotebookName is the name of the notebook associated with this checkpoint
	// +optional
	NotebookName string `json:"notebookName,omitempty"`

	// BusyCells is the list of notebook cells that were actively executing when the checkpoint was created
	// +optional
	BusyCells []BusyCell `json:"busyCells,omitempty"`
}

// CheckpointBackupStatus defines the observed state of CheckpointBackup.
type CheckpointBackupStatus struct {
	// Phase represents the current phase of the checkpoint backup operation
	// +optional
	Phase string `json:"phase,omitempty"`

	// LastCheckpointTime represents the last time a checkpoint was successfully created
	// +optional
	LastCheckpointTime *metav1.Time `json:"lastCheckpointTime,omitempty"`

	// Message provides additional information about the current state
	// +optional
	Message string `json:"message,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed CheckpointBackup
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the CheckpointBackup's current state
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// BuiltImages contains the list of checkpoint images that were successfully built
	// +optional
	BuiltImages []BuiltImage `json:"builtImages,omitempty"`

	// CheckpointFiles contains the paths to checkpoint files that have been created
	// +optional
	CheckpointFiles []CheckpointFile `json:"checkpointFiles,omitempty"`
}

// CheckpointFile represents a checkpoint file that has been created
type CheckpointFile struct {
	// ContainerName is the name of the container that was checkpointed
	// +required
	ContainerName string `json:"containerName"`

	// FilePath is the relative path to the checkpoint file on the node
	// +required
	FilePath string `json:"filePath"`

	// StoragePath is the path within the checkpoint storage PVC where the file is stored
	// +optional
	StoragePath string `json:"storagePath,omitempty"`

	// CheckpointTime is when the checkpoint was created
	// +optional
	CheckpointTime *metav1.Time `json:"checkpointTime,omitempty"`
}

// BuiltImage represents a successfully built checkpoint image
type BuiltImage struct {
	// ContainerName is the name of the container that was checkpointed
	// +required
	ContainerName string `json:"containerName"`

	// ImageName is the full name of the built checkpoint image
	// +required
	ImageName string `json:"imageName"`

	// BuildTime is when the image was built
	// +optional
	BuildTime *metav1.Time `json:"buildTime,omitempty"`

	// Pushed indicates whether the image was pushed to a registry
	// +optional
	Pushed bool `json:"pushed,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// CheckpointBackup is the Schema for the checkpointbackups API
type CheckpointBackup struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of CheckpointBackup
	// +required
	Spec CheckpointBackupSpec `json:"spec"`

	// status defines the observed state of CheckpointBackup
	// +optional
	Status CheckpointBackupStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CheckpointBackupList contains a list of CheckpointBackup
type CheckpointBackupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CheckpointBackup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CheckpointBackup{}, &CheckpointBackupList{})
}
