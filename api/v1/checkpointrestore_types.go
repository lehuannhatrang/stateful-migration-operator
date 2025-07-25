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

// CheckpointRestoreSpec defines the desired state of CheckpointRestore
type CheckpointRestoreSpec struct {
	// BackupRef specifies the backup to restore from
	// +required
	BackupRef BackupRef `json:"backupRef"`

	// PodName specifies the name of the pod to restore
	// +required
	PodName string `json:"podName"`

	// Containers specifies the container configurations for restore
	// +optional
	Containers []Container `json:"containers,omitempty"`
}

// CheckpointRestoreStatus defines the observed state of CheckpointRestore.
type CheckpointRestoreStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// CheckpointRestore is the Schema for the checkpointrestores API
type CheckpointRestore struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of CheckpointRestore
	// +required
	Spec CheckpointRestoreSpec `json:"spec"`

	// status defines the observed state of CheckpointRestore
	// +optional
	Status CheckpointRestoreStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// CheckpointRestoreList contains a list of CheckpointRestore
type CheckpointRestoreList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CheckpointRestore `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CheckpointRestore{}, &CheckpointRestoreList{})
}
