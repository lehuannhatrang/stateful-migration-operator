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

// ResourceRef defines a reference to a Kubernetes resource
type ResourceRef struct {
	// APIVersion of the referenced resource
	// +required
	APIVersion string `json:"apiVersion"`

	// Kind of the referenced resource
	// +required
	Kind string `json:"kind"`

	// Namespace of the referenced resource
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the referenced resource
	// +required
	Name string `json:"name"`
}

// PodRef defines a reference to a Pod
type PodRef struct {
	// Namespace of the referenced pod
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Name of the referenced pod
	// +required
	Name string `json:"name"`
}

// BackupRef defines a reference to a backup
type BackupRef struct {
	// Name of the referenced backup
	// +required
	Name string `json:"name"`
}

// SecretRef defines a reference to a Secret
type SecretRef struct {
	// Name of the referenced secret
	// +required
	Name string `json:"name"`
}

// Registry defines registry configuration
type Registry struct {
	// URL of the registry
	// +required
	URL string `json:"url"`

	// Repository path in the registry
	// +required
	Repository string `json:"repository"`

	// SecretRef contains credentials for the registry
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// Container defines a container configuration for checkpoints
type Container struct {
	// Name of the container
	// +required
	Name string `json:"name"`

	// Image of the container in the registry
	// +required
	Image string `json:"image"`
}

// StatefulMigrationSpec defines the desired state of StatefulMigration
type StatefulMigrationSpec struct {
	// ResourceRef specifies the workload to migrate
	// +required
	ResourceRef ResourceRef `json:"resourceRef"`

	// SourceClusters specifies which clusters to back up from
	// +required
	SourceClusters []string `json:"sourceClusters"`

	// Registry specifies the registry configuration for storing checkpoints
	// +required
	Registry Registry `json:"registry"`

	// Schedule specifies the backup schedule in cron format
	// +required
	Schedule string `json:"schedule"`
}

// StatefulMigrationStatus defines the observed state of StatefulMigration.
type StatefulMigrationStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// StatefulMigration is the Schema for the statefulmigrations API
type StatefulMigration struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of StatefulMigration
	// +required
	Spec StatefulMigrationSpec `json:"spec"`

	// status defines the observed state of StatefulMigration
	// +optional
	Status StatefulMigrationStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// StatefulMigrationList contains a list of StatefulMigration
type StatefulMigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []StatefulMigration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&StatefulMigration{}, &StatefulMigrationList{})
}
