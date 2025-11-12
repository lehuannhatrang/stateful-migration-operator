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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	migrationv1 "github.com/lehuannhatrang/stateful-migration-operator/api/v1"
	webhookpkg "github.com/lehuannhatrang/stateful-migration-operator/internal/webhook"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(migrationv1.AddToScheme(scheme))
}

func main() {
	var (
		webhookPort          int
		certDir              string
		healthProbeAddr      string
		metricsAddr          string
		enableLeaderElection bool
		leaderElectionID     string
	)

	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port for the admission webhook server")
	flag.StringVar(&certDir, "cert-dir", "/tmp/k8s-webhook-server/serving-certs", "Directory containing TLS certificates")
	flag.StringVar(&healthProbeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for webhook controller manager")
	flag.StringVar(&leaderElectionID, "leader-election-id", "webhook-leader-election", "Leader election ID")

	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctx := ctrl.SetupSignalHandler()

	config, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "Unable to get kubeconfig")
		os.Exit(1)
	}

	// Create webhook manager
	mgr, err := manager.New(config, manager.Options{
		Scheme: scheme,
		WebhookServer: webhook.NewServer(webhook.Options{
			Port:    webhookPort,
			CertDir: certDir,
		}),
		HealthProbeBindAddress: healthProbeAddr,
		// Metrics configuration removed as it's not available in this version
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              leaderElectionID,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Unable to create webhook manager")
		os.Exit(1)
	}

	// Setup health checks
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to set up ready check")
		os.Exit(1)
	}

	// Setup webhook
	setupLog.Info("Setting up webhook server")

	// Create pod mutator
	podMutator := webhookpkg.SetupPodMutator(mgr.GetClient())

	// Register webhook
	mgr.GetWebhookServer().Register("/mutate-v1-pod", &webhook.Admission{Handler: podMutator})

	setupLog.Info("Starting webhook server", "port", webhookPort, "certDir", certDir)

	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Problem running webhook server")
		os.Exit(1)
	}
}

// setupTLSConfig configures TLS settings for the webhook server
func setupTLSConfig(certDir string) (*tls.Config, error) {
	certPath := filepath.Join(certDir, "tls.crt")
	keyPath := filepath.Join(certDir, "tls.key")

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load key pair: %w", err)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// gracefulShutdown handles graceful shutdown of the webhook server
func gracefulShutdown(server *http.Server) {
	// Setup signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Wait for signal
	sig := <-sigChan
	setupLog.Info("Received shutdown signal", "signal", sig)

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown server
	if err := server.Shutdown(ctx); err != nil {
		setupLog.Error(err, "Failed to gracefully shutdown webhook server")
	} else {
		setupLog.Info("Webhook server shutdown successfully")
	}
}
