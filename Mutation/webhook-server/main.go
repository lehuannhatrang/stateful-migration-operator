// Copyright 2025 Jeong Seungjun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.


// main.go
// CheckpointRestore-aware mutating webhook server
// - HTTPS on :8443 using /tls/tls.crt and /tls/tls.key
// - Health endpoints: /healthz, /readyz (HTTPS)
// - Looks up CheckpointRestore (GVR from env) and, if matched by podName or podGenerateName prefix,
//   replaces container (and initContainer) images using spec.containers[].image (or fallback spec.image)

package main

import (
        "context"
        "encoding/json"
        "fmt"
        "io"
        "net/http"
        "os"
        "path/filepath"
        "strings"

        admissionv1 "k8s.io/api/admission/v1"
        corev1 "k8s.io/api/core/v1"
        metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
        "k8s.io/apimachinery/pkg/runtime/schema"
        "k8s.io/client-go/dynamic"
        "k8s.io/client-go/rest"
        "k8s.io/client-go/tools/clientcmd"
)

var (
        // Override via env if needed:
        //   CHECKPOINT_RESTORE_GVR_GROUP (e.g., "migration.dcnlab.com")
        //   CHECKPOINT_RESTORE_GVR_VERSION (e.g., "v1")
        //   CHECKPOINT_RESTORE_GVR_RESOURCE (e.g., "checkpointrestores")
        crGroup    = getenvDefault("CHECKPOINT_RESTORE_GVR_GROUP", "migration.dcnlab.com")
        crVersion  = getenvDefault("CHECKPOINT_RESTORE_GVR_VERSION", "v1")
        crResource = getenvDefault("CHECKPOINT_RESTORE_GVR_RESOURCE", "checkpointrestores")
)

func getenvDefault(k, d string) string {
        if v := os.Getenv(k); v != "" {
                return v
        }
        return d
}

func main() {
        // Health endpoints for probes (avoid 404 causing restarts)
        http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusOK)
                _, _ = io.WriteString(w, "ok")
        })
        http.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
                w.WriteHeader(http.StatusOK)
                _, _ = io.WriteString(w, "ok")
        })

        http.HandleFunc("/mutate", handleMutate)

        fmt.Println("Starting webhook server on :8443")
        if err := http.ListenAndServeTLS(":8443", "/tls/tls.crt", "/tls/tls.key", nil); err != nil {
                panic(err)
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
        if review.Request.Kind.Kind != "Pod" || review.Request.Kind.Version != "v1" || review.Request.Operation != admissionv1.Create {
                writeResponse(w, review, nil)
                return
        }

        var pod corev1.Pod
        if err := json.Unmarshal(review.Request.Object.Raw, &pod); err != nil {
                http.Error(w, "could not parse pod object", http.StatusBadRequest)
                return
        }
        ns := review.Request.Namespace
        fmt.Printf("💡 Pod CREATE admission: ns=%q name=%q generateName=%q containers=%d\n",
                ns, pod.Name, pod.GenerateName, len(pod.Spec.Containers))

        // Kube config (in-cluster first, fall back to local for dev)
        cfg, err := rest.InClusterConfig()
        if err != nil {
                kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
                cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
                if err != nil {
                        http.Error(w, "cannot get kubeconfig", http.StatusInternalServerError)
                        return
                }
        }

        dc, err := dynamic.NewForConfig(cfg)
        if err != nil {
                http.Error(w, "failed to create dynamic client", http.StatusInternalServerError)
                return
        }

        gvr := schema.GroupVersionResource{Group: crGroup, Version: crVersion, Resource: crResource}
        crList, err := dc.Resource(gvr).Namespace(ns).List(context.TODO(), metav1.ListOptions{})
        if err != nil {
                // RBAC 403 or CRD mismatch will surface here
                fmt.Printf("⚠️  List %s.%s/%s failed: %v\n", crResource, crGroup, crVersion, err)
                writeResponse(w, review, nil)
                return
        }

        targetName := pod.Name
        genPrefix := pod.GenerateName // may be empty; prefix match if present

        // Build container-name -> image map from matching CheckpointRestore
        imageMap := make(map[string]string)
        var defaultImage string

        for _, it := range crList.Items {
                spec, ok := it.Object["spec"].(map[string]interface{})
                if !ok {
                        continue
                }
                specPodName, _ := spec["podName"].(string)
                specGenName, _ := spec["podGenerateName"].(string)

                nameMatch := (targetName != "" && specPodName == targetName) ||
                        (genPrefix != "" && specGenName != "" && strings.HasPrefix(specGenName, genPrefix)) ||
                        (genPrefix != "" && specPodName != "" && strings.HasPrefix(specPodName, genPrefix))

                if !nameMatch {
                        continue
                }

                // Prefer spec.containers[]
                if raw, ok := spec["containers"].([]interface{}); ok {
                        for _, c := range raw {
                                m, ok := c.(map[string]interface{})
                                if !ok {
                                        continue
                                }
                                cname, _ := m["name"].(string)
                                cimg, _ := m["image"].(string)
                                if cimg == "" {
                                        continue
                                }
                                if cname != "" {
                                        imageMap[cname] = cimg
                                }
                                if defaultImage == "" {
                                        defaultImage = cimg
                                }
                        }
                }
                // Backward-compat: spec.image (single string)
                if defaultImage == "" {
                        if img, ok := spec["image"].(string); ok && img != "" {
                                defaultImage = img
                        }
                }

                fmt.Printf("✅ Matched CR %q → images=%v default=%q\n", it.GetName(), imageMap, defaultImage)
                break
        }

        if len(imageMap) == 0 && defaultImage == "" {
                fmt.Println("❌ No matching CheckpointRestore or no image specified → skipping mutation")
                writeResponse(w, review, nil)
                return
        }

        // Build JSONPatch: match by container name; fallback to defaultImage
        var patches []map[string]interface{}

        for i := range pod.Spec.Containers {
                want := imageMap[pod.Spec.Containers[i].Name]
                if want == "" {
                        want = defaultImage
                }
                if want == "" || pod.Spec.Containers[i].Image == want {
                        continue
                }
                patches = append(patches, map[string]interface{}{
                        "op":    "replace",
                        "path":  fmt.Sprintf("/spec/containers/%d/image", i),
                        "value": want,
                })
        }

        // Optionally apply to initContainers as well (same policy)
        for i := range pod.Spec.InitContainers {
                want := imageMap[pod.Spec.InitContainers[i].Name]
                if want == "" {
                        want = defaultImage
                }
                if want == "" || pod.Spec.InitContainers[i].Image == want {
                        continue
                }
                patches = append(patches, map[string]interface{}{
                        "op":    "replace",
                        "path":  fmt.Sprintf("/spec/initContainers/%d/image", i),
                        "value": want,
                })
        }

        if len(patches) == 0 {
                fmt.Println("ℹ️  Nothing to patch (images already as desired) → allowing without patch")
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

func writeResponse(w http.ResponseWriter, ar admissionv1.AdmissionReview, patch []byte) {
        resp := admissionv1.AdmissionReview{
                TypeMeta: metav1.TypeMeta{
                        APIVersion: "admission.k8s.io/v1",
                        Kind:       "AdmissionReview",
                },
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
