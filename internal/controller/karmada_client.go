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
	"fmt"
	"os"
	"strings"

	karmadav1alpha1 "github.com/karmada-io/karmada/pkg/apis/policy/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// KarmadaKubeconfigPath is the path where the Karmada kubeconfig is mounted
	KarmadaKubeconfigPath = "/etc/karmada/kubeconfig"
)

// KarmadaClient wraps a client for Karmada operations
type KarmadaClient struct {
	client.Client
	restClient rest.Interface
}

// NewKarmadaClient creates a new client for Karmada operations using the mounted kubeconfig
func NewKarmadaClient() (*KarmadaClient, error) {
	logger := log.Log.WithName("karmada-client")

	// Check if Karmada kubeconfig exists
	if _, err := os.Stat(KarmadaKubeconfigPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("Karmada kubeconfig not found at %s", KarmadaKubeconfigPath)
	}

	// Load Karmada kubeconfig
	config, err := clientcmd.BuildConfigFromFlags("", KarmadaKubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load Karmada kubeconfig: %w", err)
	}

	// Create scheme with Karmada types
	karmadaScheme := runtime.NewScheme()
	if err := scheme.AddToScheme(karmadaScheme); err != nil {
		return nil, fmt.Errorf("failed to add core types to scheme: %w", err)
	}
	if err := karmadav1alpha1.AddToScheme(karmadaScheme); err != nil {
		return nil, fmt.Errorf("failed to add Karmada types to scheme: %w", err)
	}

	// Create Karmada client
	karmadaClient, err := client.New(config, client.Options{
		Scheme: karmadaScheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Karmada client: %w", err)
	}

	// Create REST client for proxy requests
	// Configure for Karmada cluster proxy API
	restConfig := *config // Copy the config
	restConfig.APIPath = "/apis"
	restConfig.GroupVersion = &schema.GroupVersion{Group: "cluster.karmada.io", Version: "v1alpha1"}
	restConfig.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	restClient, err := rest.RESTClientFor(&restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Karmada REST client: %w", err)
	}

	logger.Info("Successfully created Karmada client", "endpoint", config.Host)

	return &KarmadaClient{
		Client:     karmadaClient,
		restClient: restClient,
	}, nil
}

// CreateOrUpdatePropagationPolicy creates or updates a PropagationPolicy in Karmada
func (k *KarmadaClient) CreateOrUpdatePropagationPolicy(ctx context.Context, policy *karmadav1alpha1.PropagationPolicy) error {
	logger := log.FromContext(ctx).WithName("karmada-client")

	// Try to get existing policy
	existing := &karmadav1alpha1.PropagationPolicy{}
	err := k.Get(ctx, client.ObjectKeyFromObject(policy), existing)

	if err != nil {
		if client.IgnoreNotFound(err) == nil {
			// Policy doesn't exist, create it
			logger.Info("Creating PropagationPolicy", "name", policy.Name, "namespace", policy.Namespace)
			return k.Create(ctx, policy)
		}
		return fmt.Errorf("failed to get PropagationPolicy: %w", err)
	}

	// Policy exists, update it - preserve system-managed labels and annotations
	logger.Info("Updating PropagationPolicy", "name", policy.Name, "namespace", policy.Namespace)

	// Preserve system-generated metadata
	policy.ResourceVersion = existing.ResourceVersion

	// Preserve immutable system labels that Karmada adds automatically
	if existing.Labels != nil {
		if policy.Labels == nil {
			policy.Labels = make(map[string]string)
		}
		// Preserve any Karmada system labels (especially permanent-id)
		for key, value := range existing.Labels {
			if strings.HasPrefix(key, "propagationpolicy.karmada.io/") ||
				strings.HasPrefix(key, "karmada.io/") {
				policy.Labels[key] = value
			}
		}
	}

	return k.Update(ctx, policy)
}

// DeletePropagationPolicy deletes a PropagationPolicy from Karmada
func (k *KarmadaClient) DeletePropagationPolicy(ctx context.Context, policy *karmadav1alpha1.PropagationPolicy) error {
	logger := log.FromContext(ctx).WithName("karmada-client")

	logger.Info("Deleting PropagationPolicy", "name", policy.Name, "namespace", policy.Namespace)
	return client.IgnoreNotFound(k.Delete(ctx, policy))
}

// TestConnection tests the connection to Karmada
func (k *KarmadaClient) TestConnection(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("karmada-client")

	// Try to list PropagationPolicies to test connection
	policies := &karmadav1alpha1.PropagationPolicyList{}
	err := k.List(ctx, policies, &client.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("failed to connect to Karmada: %w", err)
	}

	logger.Info("Successfully connected to Karmada", "policies_found", len(policies.Items))
	return nil
}

// RESTClient returns the REST client for making proxy requests
func (k *KarmadaClient) RESTClient() rest.Interface {
	return k.restClient
}
