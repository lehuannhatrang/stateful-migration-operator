/*
Copyright 2025 Le huan and Jeong SeungJun

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

        appsv1 "k8s.io/api/apps/v1"
        corev1 "k8s.io/api/core/v1"
        apierrors "k8s.io/apimachinery/pkg/api/errors"
        "k8s.io/apimachinery/pkg/labels"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/client-go/rest"
        "sigs.k8s.io/controller-runtime/pkg/log"
)

// MemberClusterClient: Karmada Aggregated API 프록시로 멤버 클러스터에 접근
type MemberClusterClient struct {
        karmadaClient *KarmadaClient
        restConfig    *rest.Config // (직접 사용 안 함: m.karmadaClient.RESTClient() 사용)
}

func NewMemberClusterClient(karmadaClient *KarmadaClient) (*MemberClusterClient, error) {
        if karmadaClient == nil {
                return nil, fmt.Errorf("karmadaClient is nil")
        }
        return &MemberClusterClient{
                karmadaClient: karmadaClient,
                restConfig:    nil,
        }, nil
}

// (참고) 별도 RESTConfig가 필요하면 구현
func getKarmadaRESTConfig() (*rest.Config, error) {
        return nil, fmt.Errorf("not implemented - using Karmada client REST config instead")
}

// -------- 내부 헬퍼 --------

func (m *MemberClusterClient) rc() rest.Interface {
        return m.karmadaClient.RESTClient()
}

func clusterProxyBase(cluster string) string {
        // Karmada Aggregated API 프록시 루트
        return fmt.Sprintf("/apis/cluster.karmada.io/v1alpha1/clusters/%s/proxy", cluster)
}

// -------- Pods --------

func (m *MemberClusterClient) GetPodFromCluster(ctx context.Context, clusterName, namespace, podName string) (*corev1.Pod, error) {
        logger := log.FromContext(ctx)
        var pod corev1.Pod
        res := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", namespace, podName)).
                Do(ctx)
        if err := res.Into(&pod); err != nil {
                return nil, fmt.Errorf("get pod %s/%s from cluster %s: %w", namespace, podName, clusterName, err)
        }
        logger.Info("Retrieved pod from member cluster", "cluster", clusterName, "namespace", namespace, "pod", podName)
        return &pod, nil
}

func (m *MemberClusterClient) UpdatePodInCluster(ctx context.Context, clusterName string, pod *corev1.Pod) error {
        logger := log.FromContext(ctx)
        if pod == nil {
                return fmt.Errorf("pod is nil")
        }
        res := m.rc().Put().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/api/v1/namespaces/%s/pods/%s", pod.Namespace, pod.Name)).
                Body(pod).
                Do(ctx)
        if err := res.Error(); err != nil {
                return fmt.Errorf("update pod %s/%s on cluster %s: %w", pod.Namespace, pod.Name, clusterName, err)
        }
        logger.Info("Updated pod on member cluster", "cluster", clusterName, "namespace", pod.Namespace, "pod", pod.Name)
        return nil
}

func (m *MemberClusterClient) ListPodsFromCluster(ctx context.Context, clusterName, namespace, labelSelector string) (*corev1.PodList, error) {
        logger := log.FromContext(ctx)
        req := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/api/v1/namespaces/%s/pods", namespace))
        if labelSelector != "" {
                req = req.Param("labelSelector", labelSelector)
        }
        var list corev1.PodList
        if err := req.Do(ctx).Into(&list); err != nil {
                return nil, fmt.Errorf("list pods from cluster %s/%s: %w", clusterName, namespace, err)
        }
        logger.Info("Listed pods from member cluster", "cluster", clusterName, "namespace", namespace, "count", len(list.Items))
        return &list, nil
}

func (m *MemberClusterClient) ListPodsBySelector(ctx context.Context, clusterName, namespace string, sel labels.Selector) ([]corev1.Pod, error) {
        pl, err := m.ListPodsFromCluster(ctx, clusterName, namespace, sel.String())
        if err != nil {
                return nil, err
        }
        return pl.Items, nil
}

// -------- Namespaces --------

func (m *MemberClusterClient) EnsureNamespace(ctx context.Context, clusterName, namespace string) error {
        logger := log.FromContext(ctx)
        get := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/api/v1/namespaces/%s", namespace)).
                Do(ctx)
        // 존재하면 OK, 404만 생성
        if err := get.Error(); err == nil {
                logger.Info("Namespace already exists on member cluster", "cluster", clusterName, "namespace", namespace)
                return nil
        } else if !apierrors.IsNotFound(err) {
                return fmt.Errorf("check namespace %s on %s: %w", namespace, clusterName, err)
        }

        ns := &corev1.Namespace{
                ObjectMeta: metav1.ObjectMeta{
                        Name: namespace,
                        Labels: map[string]string{
                                "created-by": "stateful-migration-operator",
                                "cluster":    clusterName,
                        },
                },
        }
        create := m.rc().Post().
                AbsPath(clusterProxyBase(clusterName) + "/api/v1/namespaces").
                Body(ns).
                Do(ctx)
        if err := create.Error(); err != nil {
                return fmt.Errorf("create namespace %s on %s: %w", namespace, clusterName, err)
        }
        logger.Info("Created namespace on member cluster", "cluster", clusterName, "namespace", namespace)
        return nil
}

// -------- CRD(CheckpointBackup) --------

func (m *MemberClusterClient) EnsureCRD(ctx context.Context, clusterName string) error {
        logger := log.FromContext(ctx)
        // 존재 확인
        get := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/checkpointbackups.migration.dcnlab.com").
                Do(ctx)
        if err := get.Error(); err == nil {
                logger.Info("CheckpointBackup CRD already exists on member cluster", "cluster", clusterName)
                return nil
        } else if !apierrors.IsNotFound(err) {
                return fmt.Errorf("check CRD on %s: %w", clusterName, err)
        }

        // 정의 로딩
        crdYAML, err := m.getCRDDefinition()
        if err != nil {
                return fmt.Errorf("load CRD definition: %w", err)
        }

        // 적용 (YAML 허용, 일부 환경은 JSON만 허용하지만 Kubernetes apiserver는 YAML OK)
        post := m.rc().Post().
                AbsPath(clusterProxyBase(clusterName) + "/apis/apiextensions.k8s.io/v1/customresourcedefinitions").
                SetHeader("Content-Type", "application/yaml").
                Body([]byte(crdYAML)).
                Do(ctx)
        if err := post.Error(); err != nil {
                return fmt.Errorf("install CRD on %s: %w", clusterName, err)
        }
        logger.Info("Installed CheckpointBackup CRD on member cluster", "cluster", clusterName)
        return nil
}

func (m *MemberClusterClient) getCRDDefinition() (string, error) {
        paths := []string{
                "/etc/crds/migration.dcnlab.com_checkpointbackups.yaml",
                "/app/crds/migration.dcnlab.com_checkpointbackups.yaml",
                "config/crd/bases/migration.dcnlab.com_checkpointbackups.yaml",
        }
        for _, p := range paths {
                if data, err := os.ReadFile(p); err == nil {
                        return string(data), nil
                }
        }
        // 프로젝트 내 임베디드 상수 사용
        return CheckpointBackupCRDYAML, nil
}

// -------- StatefulSet --------

func (m *MemberClusterClient) GetStatefulSetFromCluster(ctx context.Context, clusterName, namespace, stsName string) (*appsv1.StatefulSet, error) {
        logger := log.FromContext(ctx)
        var sts appsv1.StatefulSet
        res := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/apis/apps/v1/namespaces/%s/statefulsets/%s", namespace, stsName)).
                Do(ctx)
        if err := res.Into(&sts); err != nil {
                return nil, fmt.Errorf("get statefulset %s/%s from %s: %w", namespace, stsName, clusterName, err)
        }
        logger.Info("Retrieved StatefulSet", "cluster", clusterName, "namespace", namespace, "statefulset", stsName)
        return &sts, nil
}

func (m *MemberClusterClient) UpdateStatefulSetInCluster(ctx context.Context, clusterName string, sts *appsv1.StatefulSet) error {
        logger := log.FromContext(ctx)
        if sts == nil {
                return fmt.Errorf("statefulset is nil")
        }
        res := m.rc().Put().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/apis/apps/v1/namespaces/%s/statefulsets/%s", sts.Namespace, sts.Name)).
                Body(sts).
                Do(ctx)
        if err := res.Error(); err != nil {
                return fmt.Errorf("update statefulset %s/%s on %s: %w", sts.Namespace, sts.Name, clusterName, err)
        }
        logger.Info("Updated StatefulSet", "cluster", clusterName, "namespace", sts.Namespace, "statefulset", sts.Name)
        return nil
}

// -------- Deployment --------

func (m *MemberClusterClient) GetDeploymentFromCluster(ctx context.Context, clusterName, namespace, deployName string) (*appsv1.Deployment, error) {
        logger := log.FromContext(ctx)
        var dep appsv1.Deployment
        res := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", namespace, deployName)).
                Do(ctx)
        if err := res.Into(&dep); err != nil {
                return nil, fmt.Errorf("get deployment %s/%s from %s: %w", namespace, deployName, clusterName, err)
        }
        logger.Info("Retrieved Deployment", "cluster", clusterName, "namespace", namespace, "deployment", deployName)
        return &dep, nil
}

func (m *MemberClusterClient) UpdateDeploymentInCluster(ctx context.Context, clusterName string, dep *appsv1.Deployment) error {
        logger := log.FromContext(ctx)
        if dep == nil {
                return fmt.Errorf("deployment is nil")
        }
        res := m.rc().Put().
                AbsPath(clusterProxyBase(clusterName) + fmt.Sprintf("/apis/apps/v1/namespaces/%s/deployments/%s", dep.Namespace, dep.Name)).
                Body(dep).
                Do(ctx)
        if err := res.Error(); err != nil {
                return fmt.Errorf("update deployment %s/%s on %s: %w", dep.Namespace, dep.Name, clusterName, err)
        }
        logger.Info("Updated Deployment", "cluster", clusterName, "namespace", dep.Namespace, "deployment", dep.Name)
        return nil
}

// -------- Connectivity Test --------

func (m *MemberClusterClient) TestClusterConnection(ctx context.Context, clusterName string) error {
        logger := log.FromContext(ctx)
        // 네임스페이스 1개 조회로 연결 확인
        res := m.rc().Get().
                AbsPath(clusterProxyBase(clusterName) + "/api/v1/namespaces").
                Param("limit", "1").
                Do(ctx)
        if err := res.Error(); err != nil {
                return fmt.Errorf("connect to cluster %s via karmada proxy: %w", clusterName, err)
        }
        logger.Info("Member cluster connectivity OK", "cluster", clusterName)
        return nil
}

