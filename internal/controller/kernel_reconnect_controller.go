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
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	KernelReconnectAnnotation   = "migration.dcnlab.com/kernel-reconnect-signaled"
	KernelComponentLabel        = "component"
	KernelComponentValue        = "kernel"
	CRIORestoreAnnotationPrefix = "checkpoint-restore.crio.io/"
	KernelPIDFile               = "/tmp/.eg_kernel_launcher.pid"
	// KernelReconnectAddrFile is the override file the launcher reads (with
	// highest priority) to discover the current JEG response address after a
	// CRIU restore. The launcher process was restored by CRIU, so its
	// /proc/self/environ, /proc/1/environ and os.environ all still hold the
	// stale response address captured at checkpoint time. A freshly-exec'd
	// process (this controller's exec), on the other hand, sees the env the
	// container runtime currently injects — i.e. the *new* RESPONSE_ADDRESS.
	// We bridge that fresh value into this file so the restored launcher can
	// read it.
	KernelReconnectAddrFile = "/tmp/.eg_reconnect_response_addr"
)

// KernelReconnectReconciler watches for restored Jupyter kernel pods and sends
// SIGUSR1 to trigger the modified launch_ipykernel.py to re-send connection
// info to Jupyter Enterprise Gateway.
type KernelReconnectReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	NodeName   string
	Clientset  kubernetes.Interface
	RestConfig *rest.Config
}

// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

func (r *KernelReconnectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pod corev1.Pod
	if err := r.Get(ctx, req.NamespacedName, &pod); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if pod.Spec.NodeName != r.NodeName {
		return ctrl.Result{}, nil
	}

	if pod.Status.Phase != corev1.PodRunning {
		return ctrl.Result{}, nil
	}

	if !isRestoredKernelPod(&pod) {
		return ctrl.Result{}, nil
	}

	if pod.Annotations[KernelReconnectAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	log.Info("Detected restored kernel pod, sending reconnect signal",
		"pod", pod.Name, "namespace", pod.Namespace)

	if err := r.sendReconnectSignal(ctx, &pod); err != nil {
		log.Error(err, "Failed to send reconnect signal to kernel pod, will retry",
			"pod", pod.Name)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[KernelReconnectAnnotation] = "true"
	if err := r.Update(ctx, &pod); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		log.Error(err, "Failed to annotate pod after signaling", "pod", pod.Name)
		return ctrl.Result{}, err
	}

	log.Info("Successfully sent kernel reconnect signal",
		"pod", pod.Name, "namespace", pod.Namespace)
	return ctrl.Result{}, nil
}

// isRestoredKernelPod returns true when the pod has label component=kernel
// AND at least one annotation with the checkpoint-restore.crio.io/ prefix.
func isRestoredKernelPod(pod *corev1.Pod) bool {
	if pod.Labels[KernelComponentLabel] != KernelComponentValue {
		return false
	}
	for key := range pod.Annotations {
		if strings.HasPrefix(key, CRIORestoreAnnotationPrefix) {
			return true
		}
	}
	return false
}

// sendReconnectSignal execs into the kernel container and sends SIGUSR1 to the
// launcher process whose PID is stored in KernelPIDFile.
func (r *KernelReconnectReconciler) sendReconnectSignal(ctx context.Context, pod *corev1.Pod) error {
	containerName := ""
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	}

	// Bridge the *current* response address into the override file before
	// signaling. This exec runs as a fresh process, so $RESPONSE_ADDRESS
	// reflects the env the runtime injects now (the new JEG pod), not the
	// stale value frozen inside the CRIU-restored launcher. The launcher
	// reads KernelReconnectAddrFile with the highest priority during resend.
	// If $RESPONSE_ADDRESS is unset we leave any existing file untouched and
	// just signal, preserving previous behaviour.
	cmd := []string{"sh", "-c", fmt.Sprintf(
		"if [ -n \"$RESPONSE_ADDRESS\" ]; then printf '%%s' \"$RESPONSE_ADDRESS\" > %s; fi; "+
			"kill -USR1 $(cat %s)",
		KernelReconnectAddrFile, KernelPIDFile)}

	execReq := r.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(pod.Name).
		Namespace(pod.Namespace).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   cmd,
			Stdout:    true,
			Stderr:    true,
		}, clientgoscheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(r.RestConfig, "POST", execReq.URL())
	if err != nil {
		return fmt.Errorf("failed to create SPDY executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	if err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	}); err != nil {
		return fmt.Errorf("exec failed: %w (stdout: %s, stderr: %s)",
			err, stdout.String(), stderr.String())
	}

	return nil
}

// SetupWithManager registers the controller with the manager.
func (r *KernelReconnectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.NodeName = os.Getenv("NODE_NAME")
	if r.NodeName == "" {
		return fmt.Errorf("NODE_NAME environment variable is required")
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Pod{}, builder.WithPredicates(
			predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[KernelComponentLabel] == KernelComponentValue
			}),
		)).
		Named("kernelreconnect").
		Complete(r)
}
