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
	// Schedule specifies the backup schedule in cron format
	// +required
	Schedule string `json:"schedule"`

	// PodRef specifies the pod to checkpoint
	// +required
	PodRef PodRef `json:"podRef"`

	// ResourceRef specifies the workload to migrate
	// +required
	ResourceRef ResourceRef `json:"resourceRef"`

	// Registry specifies the registry configuration for storing checkpoints
	// +required
	Registry Registry `json:"registry"`

	// Containers specifies the container configurations for checkpoints
	// +optional
	Containers []Container `json:"containers,omitempty"`
}

// CheckpointBackupStatus defines the observed state of CheckpointBackup.
type CheckpointBackupStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
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
