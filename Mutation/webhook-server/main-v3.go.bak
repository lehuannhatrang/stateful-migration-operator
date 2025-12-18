// Copyright 2025 Jeong Seungjun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	// CheckpointRestore CRD (migration.dcnlab.com/v1)
	checkpointRestoreGVR = schema.GroupVersionResource{
		Group:    "migration.dcnlab.com",
		Version:  "v1",
		Resource: "checkpointrestores",
	}

	dynClient dynamic.Interface
)

type jsonPatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

func main() {
	var (
		listenAddr = getenv("WEBHOOK_LISTEN_ADDR", ":8443")
		certFile   = getenv("TLS_CERT_FILE", "/tls/tls.crt")
		keyFile    = getenv("TLS_KEY_FILE", "/tls/tls.key")
		kubeconfig = getenv("KUBECONFIG", "")
	)

	// Optional flag override (handy for local dev)
	flag.StringVar(&kubeconfig, "kubeconfig", kubeconfig, "Path to a kubeconfig. If empty, in-cluster config is used.")
	flag.StringVar(&listenAddr, "listen", listenAddr, "Webhook listen address (default :8443)")
	flag.StringVar(&certFile, "tls-cert", certFile, "TLS cert file path")
	flag.StringVar(&keyFile, "tls-key", keyFile, "TLS key file path")
	flag.Parse()

	var err error
	dynClient, err = newDynamicClient(kubeconfig)
	if err != nil {
		log.Fatalf("failed to create k8s client: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/mutate", handleMutate)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}

	log.Printf("Starting webhook server on %s", listenAddr)
	// ListenAndServeTLS loads cert/key from files; ensure Secret is mounted correctly.
	if err := srv.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
		log.Fatalf("webhook server failed: %v", err)
	}
}

func handleMutate(w http.ResponseWriter, r *http.Request) {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "invalid content-type", http.StatusUnsupportedMediaType)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "could not read request", http.StatusBadRequest)
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil || review.Request == nil {
		http.Error(w, "could not parse admission review", http.StatusBadRequest)
		return
	}

	// Only handle Pod CREATE; otherwise allow without patch
	if review.Request.Kind.Kind != "Pod" ||
		review.Request.Kind.Version != "v1" ||
		review.Request.Operation != admissionv1.Create {
		writeResponse(w, review, nil)
		return
	}

	var pod corev1.Pod
	if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
		http.Error(w, "could not parse pod object", http.StatusBadRequest)
		return
	}

	log.Printf("💡 Pod CREATE admission: ns=%q name=%q generateName=%q containers=%d",
		pod.Namespace, pod.Name, pod.GenerateName, len(pod.Spec.Containers))

	// 1) Find the *single* CheckpointRestore whose spec.podName == pod.Name (STRICT MATCH)
	cr, imageMap, defaultImage, err := findRestoreForPod(r.Context(), pod.Namespace, pod.Name)
	if err != nil {
		// For safety: allow the request if webhook has errors (you can change to deny if desired)
		log.Printf("❌ error while finding CheckpointRestore: %v (allowing without mutation)", err)
		writeResponse(w, review, nil)
		return
	}

	if cr == nil {
		log.Printf("ℹ️ No CheckpointRestore matched pod %s/%s (strict spec.podName match) → skipping mutation",
			pod.Namespace, pod.Name)
		writeResponse(w, review, nil)
		return
	}

	if len(imageMap) == 0 && defaultImage == "" {
		log.Printf("❌ Matched CR %q but no image specified in spec.containers[] or spec.image → skipping mutation",
			cr.GetName())
		writeResponse(w, review, nil)
		return
	}

	log.Printf("✅ Matched CR %q → images=%v default=%q", cr.GetName(), imageMap, defaultImage)

	// 2) Build JSON patches to update pod.spec.containers[*].image
	var patches []jsonPatchOp

	for i, c := range pod.Spec.Containers {
		desired := ""
		if img, ok := imageMap[c.Name]; ok && img != "" {
			desired = img
		} else {
			desired = defaultImage
		}
		if desired == "" {
			continue
		}
		if c.Image == desired {
			continue
		}
		patches = append(patches, jsonPatchOp{
			Op:    "replace",
			Path:  fmt.Sprintf("/spec/containers/%d/image", i),
			Value: desired,
		})
	}

	if len(patches) == 0 {
		log.Printf("ℹ️ Nothing to patch for pod %s/%s (images already as desired) → allowing without patch",
			pod.Namespace, pod.Name)
		writeResponse(w, review, nil)
		return
	}

	patchBytes, err := json.Marshal(patches)
	if err != nil {
		http.Error(w, "failed to marshal patch", http.StatusInternalServerError)
		return
	}

	writeResponse(w, review, patchBytes)
}

// findRestoreForPod finds a CheckpointRestore CR in `namespace` such that spec.podName == podName.
// Returns the matched CR, a containerName->image map, and a defaultImage fallback.
func findRestoreForPod(ctx context.Context, namespace, podName string) (*unstructured.Unstructured, map[string]string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	crList, err := dynClient.
		Resource(checkpointRestoreGVR).
		Namespace(namespace).
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, "", fmt.Errorf("list checkpointrestores: %w", err)
	}

	var matched *unstructured.Unstructured

	for i := range crList.Items {
		it := crList.Items[i]

		specPodName, found, err := unstructured.NestedString(it.Object, "spec", "podName")
		if err != nil || !found {
			continue
		}

		// ✅ STRICT match only
		if specPodName == podName {
			matched = &it
			break
		}
	}

	if matched == nil {
		return nil, nil, "", nil
	}

	imageMap := make(map[string]string)
	defaultImage := ""

	// Prefer spec.containers[]: [{name,image}, ...]
	containers, found, err := unstructured.NestedSlice(matched.Object, "spec", "containers")
	if err == nil && found {
		for _, c := range containers {
			m, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			name, _, _ := unstructured.NestedString(m, "name")
			image, _, _ := unstructured.NestedString(m, "image")
			if image == "" {
				continue
			}
			if name != "" {
				imageMap[name] = image
			}
			if defaultImage == "" {
				defaultImage = image
			}
		}
	}

	// Backward-compat: spec.image string
	if defaultImage == "" {
		if img, found, _ := unstructured.NestedString(matched.Object, "spec", "image"); found && img != "" {
			defaultImage = img
		}
	}

	return matched, imageMap, defaultImage, nil
}

func writeResponse(w http.ResponseWriter, ar admissionv1.AdmissionReview, patch []byte) {
	resp := admissionv1.AdmissionReview{
		TypeMeta: ar.TypeMeta,
		Response: &admissionv1.AdmissionResponse{
			UID:     ar.Request.UID,
			Allowed: true,
		},
	}
	if patch != nil {
		pt := admissionv1.PatchTypeJSONPatch
		resp.Response.Patch = patch
		resp.Response.PatchType = &pt
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func newDynamicClient(kubeconfigPath string) (dynamic.Interface, error) {
	var cfg *rest.Config
	var err error

	if kubeconfigPath != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		cfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}

	return dynamic.NewForConfig(cfg)
}

func getenv(k, def string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return def
	}
	return v
}
