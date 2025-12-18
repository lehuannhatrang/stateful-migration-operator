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
	"strings"
	"time"
	"math/rand"
	"strconv"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	apischema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/util/retry"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"



	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1" //링크에서 가져옴
)

const (
	// 폴링 주기
	RestoreCheckInterval = 5 * time.Second
	// 라벨 키: SM 단위 전파정책/Restore 매칭
	LabelKeySM = "migration.dcnlab.com/sm"
	
	// ✅ CheckpointBackup 예시에 맞춘 라벨 키들
	LabelKeySMAlt          = "stateful-migration"  // e.g., nginx-statefulmigration
	LabelKeyBackupCluster  = "target-cluster"      // e.g., member1 (백업이 생성된 클러스터)
	LabelKeyBackupPod      = "target-pod"          // e.g., web-0

	// ✅ 추가: MigrationRestore 진행 상태 어노테이션 키
	AnnoRestorePhase     = "migration.dcnlab.com/restore-phase"         // working|succeeded|failed
	AnnoRestoreStartedAt = "migration.dcnlab.com/restore-started-at"    // RFC3339
	RestoreSoftTimeout   = 20 * time.Minute
)

// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations,verbs=get;list;watch
// 참고: Karmada API 접근은 별도 kubeconfig를 쓰므로 이 파일의 RBAC 주석이 Karmada 권한을 보장하지는 않습니다.
// (Karmada 쪽 권한은 그 kubeconfig의 권한에 따릅니다.)

type MigrationRestoreReconciler struct {
	// 관리(운영) 클러스터 API client (여기서 StatefulMigration CR들을 unstructured로 조회)
	client.Client
	Scheme *runtime.Scheme

	// Karmada control-plane client (RB/PP/Backup/Restore 접근) - 우리 타입으로 통일
	KarmadaClient *KarmadaClient

	// 멤버 클러스터 클라언트
	MemberClusterClient *MemberClusterClient
}

func (r *MigrationRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// 폴링 워커가 직접 처리하므로, 누군가 호출해도 안전하게 noop
	return ctrl.Result{}, nil
}

// 워커는 RB를 "먼저" 보고(suspension 감지) → 해당 SM을 찾아 → 바로 처리
// r *MigrationRestoreReconciler -> 메서드(해당 객체를 통해서만 호출한다는 것)
// mgr.Add -> 내부에서 호출
// mgr ctrl.Manager -> 매개변수, error -> return 값
func (r *MigrationRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		lg := ctrl.Log.WithName("rb-poller")
		rand.Seed(time.Now().UnixNano())

		for {
			// 1) 컨텍스트 종료 처리(절대 지우지 말것)
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			
			// 2) KarmadaClient 준비 확인
			if r.KarmadaClient == nil {
				lg.V(4).Info("Karmada client not ready")
				// wait with jitter
				if !sleepWithJitterCtx(ctx, RestoreCheckInterval, 0.2) {
                    return nil
                }
				continue
			}

			// 3) MemberClusterClient 준비 확인
			if r.MemberClusterClient == nil {
				mc, err := NewMemberClusterClient(r.KarmadaClient)
				if err != nil {
					lg.Error(err, "failed to init MemberClusterClient")
					// 실패하면 잠깐 쉬었다가 다시 시도
					if !sleepWithJitterCtx(ctx, RestoreCheckInterval, 0.2) {
						return nil
					}
					continue
				}
				r.MemberClusterClient = mc
				lg.Info("Successfully initialized MemberClusterClient")
			}			

			// 4) Karmada에서 RB 전수 조사
			rbList := &unstructured.UnstructuredList{}
			rbList.SetGroupVersionKind(apischema.GroupVersionKind{
				Group:   "work.karmada.io",
				Version: "v1alpha2",
				Kind:    "ResourceBindingList",
			})

			if err := r.KarmadaClient.List(ctx, rbList); err != nil {
				lg.Error(err, "list ResourceBindings failed")
                // 에러 시에도 잠깐 쉬어 백투백 오류/스핀 방지
                if !sleepWithJitterCtx(ctx, RestoreCheckInterval, 0.2) {
                    return nil
				}
				continue				
			}

			for i := range rbList.Items {
				rb := &rbList.Items[i]
				if !isRBSuspendedU(rb) {
					continue
				}
				// StatefulSet만 대상으로
				kind, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "kind")
				if !strings.EqualFold(kind, "StatefulSet") {
					continue
				}
                // ✅ 완료/실패 RB는 빠르게 스킵하여 불필요한 처리 방지
                if phase := getRBAnnotation(rb, AnnoRestorePhase); phase == "succeeded" || phase == "failed" {
                    continue
                }

				// 5) 원본 Ref
				resNS, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "namespace")
				resName, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "name")
				resAPIV, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "apiVersion")
				resKind, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "kind")

				// 6) 관리 클러스터에서 관련 SM 탐색
				smList := &unstructured.UnstructuredList{}
				smList.SetGroupVersionKind(apischema.GroupVersionKind{
					Group:   "migration.dcnlab.com",
					Version: "v1",
					Kind:    "StatefulMigrationList",
				})
				if err := r.Client.List(ctx, smList, &client.ListOptions{Namespace: resNS}); err != nil {
					lg.Error(err, "list StatefulMigrations failed", "ns", resNS)
					continue
				}

				// --- 여기부터: 여러 SM이 매칭되면 첫 개만 처리 ---
				matched := []*unstructured.Unstructured{}
				for j := range smList.Items {
						sm := &smList.Items[j]
						refNS, _, _ := unstructured.NestedString(sm.Object, "spec", "resourceRef", "namespace")
						refName, _, _ := unstructured.NestedString(sm.Object, "spec", "resourceRef", "name")
						refAPIV, _, _ := unstructured.NestedString(sm.Object, "spec", "resourceRef", "apiVersion")
						refKind, _, _ := unstructured.NestedString(sm.Object, "spec", "resourceRef", "kind")
						if refNS == resNS && refName == resName && strings.EqualFold(refKind, resKind) && refAPIV == resAPIV {
						matched = append(matched, sm)
						}
				}
				if len(matched) == 0 {
						continue
				}
				if len(matched) > 1 {
						lg.Info("Multiple StatefulMigrations matched; taking the first", "count", len(matched))
				}
				if err := r.handleSuspendedRBForSM(ctx, rb, matched[0]); err != nil {
						lg.Error(err, "handleSuspendedRBForSM failed", "rb", namespacedNameU(rb), "sm", namespacedNameU(matched[0]))
				}
			}
            // ✅ 라운드 종료 후 지터 슬립(핵심)
            if !sleepWithJitterCtx(ctx, RestoreCheckInterval, 0.2) {
                return nil
            }			
		}
	}))
}

func (r *MigrationRestoreReconciler) handleSuspendedRBForSM(ctx context.Context, rb *unstructured.Unstructured, sm *unstructured.Unstructured) error {
	lg := log.FromContext(ctx)

	if r.KarmadaClient == nil {
		return fmt.Errorf("Karmada client not initialized")
	}

	// 이미 완료된 경우는 빠르게 종료
	if getRBAnnotation(rb, AnnoRestorePhase) == "succeeded" {
		return nil
	}

	// RB 타깃 클러스터 목록 (한 번만 구하고 재사용)
	rbClusters, err := getRBClusterNamesU(rb)
	if err != nil {
		return fmt.Errorf("parse RB clusters: %w", err)
	}

	// 진행 표식: 처음 진입 시에만 working + 시작시각 기록
	curr := getRBAnnotation(rb, AnnoRestorePhase)
	if curr != "working" && curr != "succeeded" && curr != "failed" {
		_ = r.patchRBAnnotationsU(ctx, rb, map[string]string{
			AnnoRestorePhase:     "working",
			AnnoRestoreStartedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	// SM의 SourceClusters
	srcClusters, _, _ := nestedStringSlice(sm.Object, "spec", "sourceClusters")

	// 원본 Ref (RB에서 가져옴)  ← ✅ 누락된 값 보강
	resNS, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "namespace")
	resName, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "name")
	resAPIV, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "apiVersion")
	resKind, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "kind")

	// 1) 델타 계산
	problematic := diffClusters(srcClusters, rbClusters)   // 소스였지만 이번 RB에 없는 클러스터(장애/제외)
	rescheduled := diffClusters(rbClusters, srcClusters)   // 이번에 새로 들어온 클러스터(재스케줄링)

	// 2) 이번에 살아 있어야 하는 곳은 RB가 말해주는 쪽
	targetClusters := rbClusters
	if len(targetClusters) == 0 {
		sc, found, _ := unstructured.NestedSlice(rb.Object, "spec", "clusters")
    	lg.Info("RB has no target clusters; skip",
        "rb", namespacedNameU(rb),
        "spec.clusters.found", found,
        "spec.clusters.raw", sc,
        "restorePhase", getRBAnnotation(rb, AnnoRestorePhase),
    	)
		return nil
	}

	// 3) "복원할 백업"은 'problematic' 범위만 (라벨 target-cluster 기반)
	backups, err := r.listRelatedBackupsForClustersU(ctx, resAPIV, resKind, resNS, resName, problematic)
	if err != nil {
		return fmt.Errorf("list backups: %w", err)
	}
	if len(backups) == 0 {
		lg.Info("No related CheckpointBackups for problematic clusters; waiting", "problematic", problematic)
		return nil
	}

	// 4) Restore 전파 PP 보장 - 백업 NS 사용  ← ✅ bkNS 세팅
	bkNS := backups[0].GetNamespace()
	if err := r.ensurePropagationPolicyU(ctx, sm.GetName(), bkNS, targetClusters); err != nil {
		return fmt.Errorf("ensure PP: %w", err)
	}

	// 5) Restore 생성 및 바인딩 확인  ← ✅ readyAll 계산 복원
	// 단일 failover 시나리오 가정: targetClusters[0] 이 실제로 복원되는 클러스터
	readyAll := true
	if len(targetClusters) == 0 {
		lg.Info("No target clusters for restore; skipping", "rb", namespacedNameU(rb))
		readyAll = false
		return nil // 혹은 continue / 에러 처리, 설계에 맞게
	}
	targetCluster := targetClusters[0]

    // ✅ (중요) backup 루프 밖에서 한 번만 "현재 사용 중 ordinal의 다음 값"을 계산
    //   - backup마다 별도로 계산하면, 아직 Pod가 생성되기 전 타이밍에서 동일한 nextIdx가 반복되어
    //     web-1, web-1 ... 같은 충돌/순서 꼬임이 발생할 수 있음.
    baseIdx, err := r.nextPodIndexForStatefulSet(ctx, targetCluster, resNS, resName)
    if err != nil {
        return fmt.Errorf("calc base pod index for %s/%s on cluster %s: %w", resNS, resName, targetCluster, err)
    }

    // ✅ 안정적인 순서 보장: backup 이름 기준 정렬(필요시 생성 시각 등으로 변경 가능)
    sort.Slice(backups, func(i, j int) bool { return backups[i].GetName() < backups[j].GetName() })

    nextIdx := baseIdx
    for i := range backups {
        bk := &backups[i]
        podName := fmt.Sprintf("%s-%d", resName, nextIdx)
        nextIdx++

        restore, created, err := r.ensureRestoreFromBackupU(
            ctx,
            sm.GetName(),   // smName
            targetCluster,  // 복원할 멤버 클러스터 이름 (예: "member2")
            resNS,          // StatefulSet Namespace
            resName,        // StatefulSet Name
            podName,        // ✅ targetCluster 에서 새로 만들 Pod 이름(순차 할당)
            bk,             // CheckpointBackup
        )
		if err != nil {
			return fmt.Errorf("ensure restore for %s on cluster %s: %w", bk.GetName(), targetCluster, err)
		}
		if created {
			lg.Info("Created CheckpointRestore",
				"restore", restore.GetName(),
				"backup", bk.GetName(),
				"cluster", targetCluster)
		}

		ok, err := r.isRestoreBoundU(ctx, restore, targetClusters)
		if err != nil {
			return fmt.Errorf("check restore RB: %w", err)
		}
		if !ok {
			readyAll = false
			lg.Info("Restore not bound yet",
				"restore", restore.GetName(),
				"wantClusters", targetClusters)
		}
	}
	
	if readyAll {
		// 6) 완료 표시
		if getRBAnnotation(rb, AnnoRestorePhase) != "succeeded" {
			if err := r.patchRBAnnotationsU(ctx, rb, map[string]string{AnnoRestorePhase: "succeeded"}); err != nil {
				return fmt.Errorf("annotate RB succeeded: %w", err)
			}
			lg.Info("Marked restore succeeded on RB annotation", "rb", namespacedNameU(rb))
		}

		// 8) SM 소스 갱신 규칙
		var newSources []string
		if len(rescheduled) == 0 {
			newSources = intersectClusters(srcClusters, rbClusters) // (= src ∩ rb) 이미 반영되어 있다면 문제만 제거
		} else {
			newSources = rbClusters // 첫 반영이면 RB로 치환
		}

		if err := r.updateSMSourceClusters(ctx, sm, newSources); err != nil {
			lg.Error(err, "update SM.spec.sourceClusters failed; will retry", "sm", namespacedNameU(sm))
		} else {
			lg.Info("Updated SM.spec.sourceClusters",
				"sm", namespacedNameU(sm),
				"old", srcClusters, "rb", rbClusters, "new", newSources,
				"problematic", problematic, "rescheduled", rescheduled)
		}
	} else {
		// 아직 모두 준비되지 않았다면 soft-timeout 감시(로그 또는 실패 어노테이션)
		if started := getRBAnnotation(rb, AnnoRestoreStartedAt); started != "" {
			if t, err := time.Parse(time.RFC3339, started); err == nil && time.Since(t) > RestoreSoftTimeout {
				lg.Info("Restore soft-timeout reached; still working", "rb", namespacedNameU(rb))
				// 필요 시 실패 표식까지 남기려면 아래 주석 해제
				// _ = r.patchRBAnnotationsU(ctx, rb, map[string]string{AnnoRestorePhase: "failed"})
			}
		}
	}

	return nil
}

func (r *MigrationRestoreReconciler) listRelatedBackupsU(ctx context.Context, apiVersion, kind, ns, name string) ([]unstructured.Unstructured, error) {
	if r.KarmadaClient == nil {
		return nil, fmt.Errorf("Karmada client not initialized")
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(apischema.GroupVersionKind{
		Group:   "migration.dcnlab.com",
		Version: "v1",
		Kind:    "CheckpointBackupList",
	})
	if err := r.KarmadaClient.List(ctx, list, &client.ListOptions{Namespace: ns}); err != nil {
		return nil, err
	}
	out := make([]unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		it := list.Items[i]
		refAPIV, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "apiVersion")
		refKind, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "kind")
		refNS, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "namespace")
		refName, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "name")
		if refAPIV == apiVersion && strings.EqualFold(refKind, kind) && refNS == ns && refName == name {
			out = append(out, it)
		}
	}
	return out, nil
}

func (r *MigrationRestoreReconciler) ensureRestoreFromBackupU(
	ctx context.Context, 
	smName string, 
	targetCluster string,
	stsNamespace string,
	stsName string,
	podName string,
	backup *unstructured.Unstructured,
	) (*unstructured.Unstructured, bool, error) {
	if r.KarmadaClient == nil {
		return nil, false, fmt.Errorf("Karmada client not initialized")
	}
	ns := backup.GetNamespace()
	bkName := backup.GetName()
	restoreName := fmt.Sprintf("%s-restore", bkName)

	// ---- 1) 이미 같은 이름의 CheckpointRestore 가 있으면 라벨만 보정 후 재사용 ----
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(apischema.GroupVersionKind{
		Group:   "migration.dcnlab.com",
		Version: "v1",
		Kind:    "CheckpointRestore",
	})
	if err := r.KarmadaClient.Get(ctx, types.NamespacedName{
		Namespace: ns, 
		Name: restoreName,
	}, existing); err == nil {
		labels := existing.GetLabels()
		if labels == nil {
			labels = map[string]string{}
		}
		if labels[LabelKeySM] != smName {
			labels[LabelKeySM] = smName
			existing.SetLabels(labels)
			if err := r.KarmadaClient.Update(ctx, existing); err != nil {
				return nil, false, fmt.Errorf("patch restore labels: %w", err)
			}
		}
		return existing, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get restore: %w", err)
	}
	
	// ---- 2) 새 CheckpointRestore 생성 ----
	restore := &unstructured.Unstructured{}
	restore.SetGroupVersionKind(apischema.GroupVersionKind{
		Group:   "migration.dcnlab.com",
		Version: "v1",
		Kind:    "CheckpointRestore",
	})
	restore.SetNamespace(ns)
	restore.SetName(restoreName)
	restore.SetLabels(map[string]string{
		LabelKeySM:                     smName,
		"migration.dcnlab.com/restore": "true",
		"migration.dcnlab.com/backup":  bkName,
	})

	// Backup 에 있는 컨테이너 정보는 그대로 사용 (checkpoint image 등)
	containers, _, _ := unstructured.NestedSlice(backup.Object, "spec", "containers")

	// ✅ CheckpointRestore spec 은 "새로 생성될 Pod 정보"로만 구성
	//   - backupRef: 어떤 백업을 쓸지
	//   - podName:   targetCluster 에서 새로 만들 Pod 이름
	//   - containers: checkpoint image 정보
	_ = unstructured.SetNestedField(restore.Object,
		map[string]interface{}{"name": bkName},
		"spec", "backupRef")
	_ = unstructured.SetNestedField(restore.Object,
		podName,
		"spec", "podName")
	_ = unstructured.SetNestedSlice(restore.Object,
		containers,
		"spec", "containers")

	if err := r.KarmadaClient.Create(ctx, restore); err != nil {
		return nil, false, fmt.Errorf("create restore: %w", err)
	}
	return restore, true, nil
}

func (r *MigrationRestoreReconciler) ensurePropagationPolicyU(ctx context.Context, smName, ns string, clusterNames []string) error {
	if r.KarmadaClient == nil {
		return fmt.Errorf("Karmada client not initialized")
	}
	if len(clusterNames) == 0 {
		return fmt.Errorf("no target clusters")
	}
	polName := fmt.Sprintf("%s-restore-policy", smName)

	pol := &unstructured.Unstructured{}
	pol.SetGroupVersionKind(apischema.GroupVersionKind{
		Group:   "policy.karmada.io",
		Version: "v1alpha1",
		Kind:    "PropagationPolicy",
	})
	err := r.KarmadaClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: polName}, pol)

	selector := map[string]interface{}{
		"apiVersion": migrationv1.GroupVersion.String(), // 하드코딩 대신 실제 G/V 사용
		"kind":       "CheckpointRestore",
		"labelSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{LabelKeySM: smName},
		},
	}
	placement := map[string]interface{}{
		"clusterAffinity": map[string]interface{}{
			"clusterNames": stringSliceIface(clusterNames),
		},
	}

	switch {
	case err == nil:
		need := false
		rs, _, _ := unstructured.NestedSlice(pol.Object, "spec", "resourceSelectors")
		if len(rs) != 1 || !resourceSelectorEqual(rs[0], selector) {
			_ = unstructured.SetNestedSlice(pol.Object, []interface{}{selector}, "spec", "resourceSelectors")
			need = true
		}
		curr, _, _ := unstructured.NestedStringSlice(pol.Object, "spec", "placement", "clusterAffinity", "clusterNames")
		if !stringSetsEqual(curr, clusterNames) {
			_ = unstructured.SetNestedField(pol.Object, stringSliceIface(clusterNames), "spec", "placement", "clusterAffinity", "clusterNames")
			need = true
		}
		if need {
			return r.KarmadaClient.Update(ctx, pol)
		}
		return nil

	case apierrors.IsNotFound(err):
		newPol := &unstructured.Unstructured{}
		newPol.SetGroupVersionKind(pol.GroupVersionKind())
		newPol.SetNamespace(ns)
		newPol.SetName(polName)
		_ = unstructured.SetNestedSlice(newPol.Object, []interface{}{selector}, "spec", "resourceSelectors")
		_ = unstructured.SetNestedField(newPol.Object, placement, "spec", "placement")
		return r.KarmadaClient.Create(ctx, newPol)

	default:
		return fmt.Errorf("get PP: %w", err)
	}
}

func (r *MigrationRestoreReconciler) isRestoreBoundU(ctx context.Context, restore *unstructured.Unstructured, targetClusters []string) (bool, error) {
	if r.KarmadaClient == nil {
		return false, fmt.Errorf("Karmada client not initialized")
	}
	rbList := &unstructured.UnstructuredList{}
	rbList.SetGroupVersionKind(apischema.GroupVersionKind{
		Group:   "work.karmada.io",
		Version: "v1alpha2",
		Kind:    "ResourceBindingList",
	})
	if err := r.KarmadaClient.List(ctx, rbList, &client.ListOptions{Namespace: restore.GetNamespace()}); err != nil {
		return false, err
	}
	wantAPIV := migrationv1.GroupVersion.String()
	wantKind := "CheckpointRestore"
	wantNS := restore.GetNamespace()
	wantName := restore.GetName()

	for i := range rbList.Items {
		rb := &rbList.Items[i]
		apiV, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "apiVersion")
		kd, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "kind")
		ns, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "namespace")
		name, _, _ := unstructured.NestedString(rb.Object, "spec", "resource", "name")
		if apiV == wantAPIV && strings.EqualFold(kd, wantKind) && ns == wantNS && name == wantName {
			curr, _ := getRBClusterNamesU(rb)
			return stringSetsEqual(curr, targetClusters), nil
		}
	}
	return false, nil
}

/* ------------------------------ 유틸 ------------------------------ */

func isRBSuspendedU(rb *unstructured.Unstructured) bool {
	disp, found, _ := unstructured.NestedBool(rb.Object, "spec", "suspension", "suspension", "dispatching")
	if !found {
		disp2, _, _ := unstructured.NestedBool(rb.Object, "spec", "suspension", "dispatching")
		return disp2
	}
	return disp
}

func intersectClusters(a, b []string) []string {
    set := make(map[string]struct{}, len(a))
    for _, s := range a { set[s] = struct{}{} }
    out := make([]string, 0, len(a))
    for _, s := range b {
        if _, ok := set[s]; ok {
            out = append(out, s)
        }
    }
    return out
}

// ✅ 추가: RB 어노테이션 patch 유틸 (KarmadaClient 사용)
func (r *MigrationRestoreReconciler) patchRBAnnotationsU(ctx context.Context, rb *unstructured.Unstructured, adds map[string]string) error {
	if r.KarmadaClient == nil {
		return fmt.Errorf("Karmada client not initialized")
	}
	key := types.NamespacedName{Namespace: rb.GetNamespace(), Name: rb.GetName()}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &unstructured.Unstructured{}
		fresh.SetGroupVersionKind(rb.GroupVersionKind())
		if err := r.KarmadaClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		ann := fresh.GetAnnotations()
		if ann == nil {
			ann = map[string]string{}
		}
		changed := false
		for k, v := range adds {
			if ann[k] != v {
				ann[k] = v
				changed = true
			}
		}
		if !changed {
			return nil
		}
		fresh.SetAnnotations(ann)
		return r.KarmadaClient.Update(ctx, fresh)
	})
}

func getRBClusterNamesU(rb *unstructured.Unstructured) ([]string, error) {
	clusters, found, err := unstructured.NestedSlice(rb.Object, "spec", "clusters")
	if err != nil {
		return nil, err
	}
	if !found {
		return []string{}, nil
	}
	out := make([]string, 0, len(clusters))
	for _, it := range clusters {
		if m, ok := it.(map[string]interface{}); ok {
			if name, ok := m["name"].(string); ok {
				out = append(out, name)
			}
		}
	}
	return out, nil
}

func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		if m[s] == 0 {
			return false
		}
		m[s]--
		if m[s] == 0 {
			delete(m, s)
		}
	}
	return len(m) == 0
}

func diffClusters(all, exclude []string) []string {
	ex := map[string]struct{}{}
	for _, s := range exclude {
		ex[s] = struct{}{}
	}
	out := make([]string, 0, len(all))
	for _, a := range all {
		if _, skip := ex[a]; !skip {
			out = append(out, a)
		}
	}
	return out
}

func stringSliceIface(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

func resourceSelectorEqual(a interface{}, want map[string]interface{}) bool {
	ma, ok := a.(map[string]interface{})
	if !ok {
		return false
	}
	if ma["apiVersion"] != want["apiVersion"] || ma["kind"] != want["kind"] {
		return false
	}
	lsa, _, _ := unstructured.NestedMap(ma, "labelSelector")
	lsb, _, _ := unstructured.NestedMap(want, "labelSelector")
	return fmt.Sprintf("%v", lsa) == fmt.Sprintf("%v", lsb)
}

func nestedStringSlice(obj map[string]interface{}, fields ...string) ([]string, bool, error) {
	raw, found, err := unstructured.NestedSlice(obj, fields...)
	if err != nil || !found {
		return nil, found, err
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out, true, nil
}

func namespacedNameU(u *unstructured.Unstructured) string {
	return fmt.Sprintf("%s/%s", u.GetNamespace(), u.GetName())
}


// 유틸 추가
func getRBAnnotation(u *unstructured.Unstructured, key string) string {
    if ann := u.GetAnnotations(); ann != nil {
        return ann[key]
    }
    return ""
}

// withJitter returns d adjusted by ±pct (0.0~1.0)
func withJitter(d time.Duration, pct float64) time.Duration {
    if pct <= 0 {
        return d
    }
    maxJ := int64(float64(d) * pct)
    if maxJ <= 0 {
        return d
    }
    delta := rand.Int63n(2*maxJ+1) - maxJ // [-maxJ, +maxJ]
    return d + time.Duration(delta)
}

// sleepWithJitterCtx sleeps with jitter but stops early if ctx is done.
func sleepWithJitterCtx(ctx context.Context, base time.Duration, pct float64) bool {
    dur := withJitter(base, pct)
    t := time.NewTimer(dur)
    defer t.Stop()
    select {
    case <-ctx.Done():
        return false
    case <-t.C:
        return true
    }
}

// CheckpointBackup 예시 스키마를 따름:
// - metadata.labels.stateful-migration: <smName>
// - metadata.labels.target-cluster:    <clusterName>
// - metadata.labels.target-pod:        <podName> (옵션)
//
// 워크로드 ref 일치 + target-cluster ∈ clusters 인 Backup만 반환.
func (r *MigrationRestoreReconciler) listRelatedBackupsForClustersU(
    ctx context.Context,
    apiVersion, kind, ns, name string,
    clusters []string,
) ([]unstructured.Unstructured, error) {
    if r.KarmadaClient == nil {
        return nil, fmt.Errorf("Karmada client not initialized")
    }
    set := map[string]struct{}{}
    for _, c := range clusters { set[c] = struct{}{} }

    list := &unstructured.UnstructuredList{}
    list.SetGroupVersionKind(apischema.GroupVersionKind{
        Group:   "migration.dcnlab.com",
        Version: "v1",
        Kind:    "CheckpointBackupList",
    })
    if err := r.KarmadaClient.List(ctx, list, &client.ListOptions{Namespace: ns}); err != nil {
        return nil, err
    }

    out := make([]unstructured.Unstructured, 0, len(list.Items))
    for i := range list.Items {
        it := list.Items[i]
        // 워크로드 ref 일치
        refAPIV, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "apiVersion")
        refKind, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "kind")
        refNS,   _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "namespace")
        refName, _, _ := unstructured.NestedString(it.Object, "spec", "resourceRef", "name")
        if !(refAPIV == apiVersion && strings.EqualFold(refKind, kind) && refNS == ns && refName == name) {
            continue
        }

        // 라벨: target-cluster 로 필터 (없으면 안전하게 제외)
        lbl := it.GetLabels()
        if lbl == nil { continue }
        sc := lbl[LabelKeyBackupCluster] // "target-cluster"
        if sc == "" { continue }
        if _, ok := set[sc]; !ok { continue }

        out = append(out, it)
    }
    return out, nil
}

// updateSMSourceClusters:
// 관리(운영) 클러스터의 StatefulMigration(spec.sourceClusters)을 newSources로 치환하고,
// metadata.labels["target-cluster"]도 함께 갱신/삭제한다.
// - newSources 길이가 1이면 해당 값을 라벨로 설정
// - 0개 또는 2개 이상이면 라벨 제거
func (r *MigrationRestoreReconciler) updateSMSourceClusters(
	ctx context.Context,
	sm *unstructured.Unstructured,
	newSources []string,
) error {
	key := types.NamespacedName{Namespace: sm.GetNamespace(), Name: sm.GetName()}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &unstructured.Unstructured{}
		// StatefulMigration GVK 그대로
		fresh.SetGroupVersionKind(sm.GroupVersionKind())

		if err := r.Client.Get(ctx, key, fresh); err != nil {
			return err
		}

		// 1) spec.sourceClusters 치환
		if err := unstructured.SetNestedStringSlice(fresh.Object, newSources, "spec", "sourceClusters"); err != nil {
			return fmt.Errorf("set sourceClusters: %w", err)
		}

		// 2) metadata.labels["target-cluster"] 갱신/삭제
		lbl := fresh.GetLabels()
		if lbl == nil {
			lbl = map[string]string{}
		}
		switch len(newSources) {
		case 1:
			// 예시 스키마와 동일 키 사용 (CheckPointBackup도 같은 키 사용)
			lbl[LabelKeyBackupCluster] = newSources[0] // "target-cluster"
		default:
			// 0개 또는 2개 이상이면 라벨 제거
			if _, ok := lbl[LabelKeyBackupCluster]; ok {
				delete(lbl, LabelKeyBackupCluster)
			}
		}
		fresh.SetLabels(lbl)

		return r.Client.Update(ctx, fresh)
	})
}

// targetCluster 의 namespace 에 있는 StatefulSet stsName 의 Pod 들을 보고
// 사용 중인 ordinal 의 최대값+1 을 리턴
func (r *MigrationRestoreReconciler) nextPodIndexForStatefulSet(
	ctx context.Context,
	clusterName, namespace, stsName string,
) (int, error) {
	if r.MemberClusterClient == nil {
		return 0, fmt.Errorf("MemberClusterClient not initialized")
	}

	// 1) 멤버 클러스터에서 StatefulSet 가져오기
	sts, err := r.MemberClusterClient.GetStatefulSetFromCluster(ctx, clusterName, namespace, stsName)
	if err != nil {
		return 0, fmt.Errorf("get statefulset %s/%s on cluster %s: %w", namespace, stsName, clusterName, err)
	}

	if sts.Spec.Selector == nil {
		return 0, fmt.Errorf("statefulset %s/%s has nil .spec.selector", namespace, stsName)
	}

	// 2) selector 기반으로 해당 cluster 의 Pod 목록 조회
	sel, err := metav1.LabelSelectorAsSelector(sts.Spec.Selector)
	if err != nil {
		return 0, fmt.Errorf("invalid selector on statefulset %s/%s: %w", namespace, stsName, err)
	}

	pods, err := r.MemberClusterClient.ListPodsBySelector(ctx, clusterName, namespace, sel)
	if err != nil {
		return 0, fmt.Errorf("list pods for statefulset %s/%s on cluster %s: %w", namespace, stsName, clusterName, err)
	}

	// 3) Pod 이름에서 "<stsName>-<숫자>" 의 숫자 부분만 뽑아서 max index 계산
	prefix := stsName + "-"
	maxIdx := -1

	for _, p := range pods {
		if !strings.HasPrefix(p.Name, prefix) {
			continue
		}
		parts := strings.Split(p.Name, "-")
		if len(parts) < 2 {
			continue
		}
		idxStr := parts[len(parts)-1]
		idx, err := strconv.Atoi(idxStr)
		if err != nil {
			continue
		}
		if idx > maxIdx {
			maxIdx = idx
		}
	}

	return maxIdx + 1, nil // Pod 가 없으면 0부터 시작
}