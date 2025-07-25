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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
)

// MigrationRestoreReconciler reconciles a StatefulMigration object for restore operations
type MigrationRestoreReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations,verbs=get;list;watch
// +kubebuilder:rbac:groups=migration.dcnlab.com,resources=statefulmigrations/status,verbs=get

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// This controller watches StatefulMigration resources but does nothing at the moment.
func (r *MigrationRestoreReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	// Fetch the StatefulMigration instance
	var statefulMigration migrationv1.StatefulMigration
	if err := r.Get(ctx, req.NamespacedName, &statefulMigration); err != nil {
		log.Error(err, "unable to fetch StatefulMigration")
		// we'll ignore not-found errors, since they can't be fixed by an immediate
		// requeue (we'll need to wait for a new notification), and we can get them
		// on deleted requests.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	log.Info("MigrationRestore controller received StatefulMigration", "name", statefulMigration.Name, "namespace", statefulMigration.Namespace)

	// TODO: Implement restore logic here when needed
	// This controller will handle restore operations for StatefulMigration resources
	// For now, it does nothing but log that it received the resource

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MigrationRestoreReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&migrationv1.StatefulMigration{}).
		Named("migrationrestore").
		Complete(r)
}
