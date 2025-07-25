package controller

import (
	"context"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// MemberClusterClient manages connections to Karmada member clusters using aggregated API
type MemberClusterClient struct {
	karmadaClient *KarmadaClient
	restConfig    *rest.Config
}

// NewMemberClusterClient creates a new member cluster client manager using Karmada aggregated API
func NewMemberClusterClient(karmadaClient *KarmadaClient) (*MemberClusterClient, error) {
	return &MemberClusterClient{
		karmadaClient: karmadaClient,
		restConfig:    nil, // Not needed since we use karmadaClient.RESTClient()
	}, nil
}

// getKarmadaRESTConfig gets the REST config for Karmada API server
func getKarmadaRESTConfig() (*rest.Config, error) {
	// For now, return a placeholder since we'll use the Karmada client's REST config
	// This function isn't actually used in the current implementation
	return nil, fmt.Errorf("not implemented - using Karmada client REST config instead")
}

// GetPodFromCluster gets a pod from the specified member cluster using Karmada aggregated API
func (m *MemberClusterClient) GetPodFromCluster(ctx context.Context, clusterName, namespace, podName string) (*corev1.Pod, error) {
	logger := log.FromContext(ctx)

	// Use Karmada aggregated API to proxy request to member cluster
	// The URL pattern is: /apis/cluster.karmada.io/v1alpha1/clusters/{cluster}/proxy/api/v1/namespaces/{namespace}/pods/{pod}

	var pod corev1.Pod

	// Create a REST client for the aggregated API request
	restClient := m.karmadaClient.RESTClient()

	result := restClient.Get().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces/%s/pods/%s",
			clusterName, namespace, podName)).
		Do(ctx)

	if err := result.Into(&pod); err != nil {
		return nil, fmt.Errorf("failed to get pod %s/%s from cluster %s: %w", namespace, podName, clusterName, err)
	}

	logger.Info("Successfully retrieved pod from member cluster",
		"cluster", clusterName, "namespace", namespace, "pod", podName)

	return &pod, nil
}

// UpdatePodInCluster updates a pod in the specified member cluster using Karmada aggregated API
func (m *MemberClusterClient) UpdatePodInCluster(ctx context.Context, clusterName string, pod *corev1.Pod) error {
	logger := log.FromContext(ctx)

	// Use Karmada aggregated API to proxy update request to member cluster
	restClient := m.karmadaClient.RESTClient()

	result := restClient.Put().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces/%s/pods/%s",
			clusterName, pod.Namespace, pod.Name)).
		Body(pod).
		Do(ctx)

	if err := result.Error(); err != nil {
		return fmt.Errorf("failed to update pod %s/%s on cluster %s: %w", pod.Namespace, pod.Name, clusterName, err)
	}

	logger.Info("Successfully updated pod on member cluster",
		"cluster", clusterName, "namespace", pod.Namespace, "pod", pod.Name)

	return nil
}

// ListPodsFromCluster lists pods from the specified member cluster using Karmada aggregated API
func (m *MemberClusterClient) ListPodsFromCluster(ctx context.Context, clusterName, namespace string, labelSelector string) (*corev1.PodList, error) {
	logger := log.FromContext(ctx)

	// Use Karmada aggregated API to proxy list request to member cluster
	restClient := m.karmadaClient.RESTClient()

	req := restClient.Get().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces/%s/pods",
			clusterName, namespace))

	if labelSelector != "" {
		req = req.Param("labelSelector", labelSelector)
	}

	result := req.Do(ctx)

	var podList corev1.PodList
	if err := result.Into(&podList); err != nil {
		return nil, fmt.Errorf("failed to list pods from cluster %s: %w", clusterName, err)
	}

	logger.Info("Successfully listed pods from member cluster",
		"cluster", clusterName, "namespace", namespace, "count", len(podList.Items))

	return &podList, nil
}

// TestClusterConnection tests connectivity to a member cluster using Karmada aggregated API
func (m *MemberClusterClient) TestClusterConnection(ctx context.Context, clusterName string) error {
	logger := log.FromContext(ctx)

	// Try to list namespaces as a connectivity test
	restClient := m.karmadaClient.RESTClient()

	result := restClient.Get().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces", clusterName)).
		Param("limit", "1").
		Do(ctx)

	if err := result.Error(); err != nil {
		return fmt.Errorf("failed to connect to cluster %s via Karmada proxy: %w", clusterName, err)
	}

	logger.Info("Successfully tested connection to member cluster", "cluster", clusterName)
	return nil
}

// EnsureNamespace ensures a namespace exists on the member cluster, creating it if necessary
func (m *MemberClusterClient) EnsureNamespace(ctx context.Context, clusterName, namespace string) error {
	logger := log.FromContext(ctx)

	// Check if namespace exists
	restClient := m.karmadaClient.RESTClient()

	result := restClient.Get().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces/%s", clusterName, namespace)).
		Do(ctx)

	if result.Error() == nil {
		logger.Info("Namespace already exists on member cluster", "cluster", clusterName, "namespace", namespace)
		return nil
	}

	// Create namespace if it doesn't exist
	logger.Info("Creating namespace on member cluster", "cluster", clusterName, "namespace", namespace)

	namespaceObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Labels: map[string]string{
				"created-by": "stateful-migration-operator",
				"cluster":    clusterName,
			},
		},
	}

	createResult := restClient.Post().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/api/v1/namespaces", clusterName)).
		Body(namespaceObj).
		Do(ctx)

	if err := createResult.Error(); err != nil {
		return fmt.Errorf("failed to create namespace %s on cluster %s: %w", namespace, clusterName, err)
	}

	logger.Info("Successfully created namespace on member cluster", "cluster", clusterName, "namespace", namespace)
	return nil
}

// EnsureCRD ensures the CheckpointBackup CRD exists on the member cluster
func (m *MemberClusterClient) EnsureCRD(ctx context.Context, clusterName string) error {
	logger := log.FromContext(ctx)

	// Check if CheckpointBackup CRD exists
	restClient := m.karmadaClient.RESTClient()

	result := restClient.Get().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/apis/apiextensions.k8s.io/v1/customresourcedefinitions/checkpointbackups.migration.dcnlab.com", clusterName)).
		Do(ctx)

	if result.Error() == nil {
		logger.Info("CheckpointBackup CRD already exists on member cluster", "cluster", clusterName)
		return nil
	}

	logger.Info("CheckpointBackup CRD not found, installing on member cluster", "cluster", clusterName)

	// Get the CRD definition - try multiple sources
	crdData, err := m.getCRDDefinition()
	if err != nil {
		return fmt.Errorf("failed to get CheckpointBackup CRD definition: %w", err)
	}

	// Apply the CRD to the member cluster
	createResult := restClient.Post().
		AbsPath(fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy/apis/apiextensions.k8s.io/v1/customresourcedefinitions", clusterName)).
		SetHeader("Content-Type", "application/yaml").
		Body([]byte(crdData)).
		Do(ctx)

	if err := createResult.Error(); err != nil {
		return fmt.Errorf("failed to install CheckpointBackup CRD on cluster %s: %w", clusterName, err)
	}

	logger.Info("Successfully installed CheckpointBackup CRD on member cluster", "cluster", clusterName)
	return nil
}

// getCRDDefinition gets the CheckpointBackup CRD definition from various sources
func (m *MemberClusterClient) getCRDDefinition() (string, error) {
	// Try different sources for the CRD definition

	// 1. Try mounted file (for when CRDs are mounted as ConfigMap or volume)
	mountedPaths := []string{
		"/etc/crds/migration.dcnlab.com_checkpointbackups.yaml",
		"/app/crds/migration.dcnlab.com_checkpointbackups.yaml",
		"config/crd/bases/migration.dcnlab.com_checkpointbackups.yaml",
	}

	for _, path := range mountedPaths {
		if data, err := os.ReadFile(path); err == nil {
			return string(data), nil
		}
	}

	// 2. Use embedded CRD definition as fallback
	return CheckpointBackupCRDYAML, nil
}
