/*
Copyright 2025 The Crossplane Authors.

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

// Package metrics defines the provider's custom Prometheus collectors and
// registers them with the controller-runtime registry, so they are exposed on
// the manager's existing /metrics endpoint.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Result label values.
const (
	ResultSuccess = "success"
	ResultError   = "error"
)

// Stage label values for ReconcileErrors.
const (
	StageGit        = "git"
	StageDecrypt    = "decrypt"
	StageSecret     = "secret"
	StageDownstream = "downstream"
)

var (
	// GitFetchDuration records how long git clone/pull/revision operations take.
	GitFetchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "provider_kubeconfig_git_fetch_duration_seconds",
		Help:    "Duration of git clone/pull/revision operations in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"repo", "branch", "operation", "result"})

	// GitCacheOps counts git source operations by type, distinguishing a fresh
	// clone from a cache-hit pull or a pinned-revision checkout.
	GitCacheOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "provider_kubeconfig_git_cache_total",
		Help: "Count of git source operations by type (clone, pull, revision).",
	}, []string{"repo", "branch", "operation"})

	// SOPSDecryptDuration records how long SOPS decryption takes.
	SOPSDecryptDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "provider_kubeconfig_sops_decrypt_duration_seconds",
		Help:    "Duration of SOPS decrypt operations in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"format", "result"})

	// ReconcileErrors counts reconcile errors by the stage that produced them.
	ReconcileErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "provider_kubeconfig_reconcile_errors_total",
		Help: "Count of reconcile errors by stage (git, decrypt, secret, downstream).",
	}, []string{"stage"})
)

func init() {
	metrics.Registry.MustRegister(GitFetchDuration, GitCacheOps, SOPSDecryptDuration, ReconcileErrors)
}
