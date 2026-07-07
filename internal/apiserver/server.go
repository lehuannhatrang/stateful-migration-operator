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

package apiserver

import (
	"log/slog"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Server is the checkpoint API server.
type Server struct {
	client  client.Client
	handler http.Handler
	logger  *slog.Logger
}

// NewServer creates a new API server with the given Kubernetes client.
func NewServer(c client.Client, logger *slog.Logger) *Server {
	s := &Server{
		client: c,
		logger: logger,
	}
	s.handler = s.routes()
	return s
}

// routes sets up the HTTP routes.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	mux.HandleFunc("POST /api/v1/checkpoints", s.handleCreateCheckpoint)
	mux.HandleFunc("GET /api/v1/checkpoints", s.handleListCheckpoints)
	mux.HandleFunc("GET /api/v1/checkpoints/{namespace}/{name}", s.handleGetCheckpoint)
	mux.HandleFunc("PUT /api/v1/checkpoints/{namespace}/{name}", s.handleUpdateCheckpoint)
	mux.HandleFunc("DELETE /api/v1/checkpoints/{namespace}/{name}", s.handleDeleteCheckpoint)
	mux.HandleFunc("POST /api/v1/checkpoints/{namespace}/{name}/restore", s.handleRestoreCheckpoint)

	return s.withMiddleware(mux)
}

// withMiddleware wraps the handler with logging and CORS middleware.
func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)

		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start).String(),
			"remote", r.RemoteAddr,
		)
	})
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
