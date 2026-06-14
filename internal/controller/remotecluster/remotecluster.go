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

package remotecluster

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/ratelimiter"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	v1alpha1 "github.com/stuttgart-things/provider-kubeconfig/apis/kubeconfig/v1alpha1"
	apisv1alpha1 "github.com/stuttgart-things/provider-kubeconfig/apis/v1alpha1"
	clusterpkg "github.com/stuttgart-things/provider-kubeconfig/internal/cluster"
	decryptpkg "github.com/stuttgart-things/provider-kubeconfig/internal/decrypt"
	gitpkg "github.com/stuttgart-things/provider-kubeconfig/internal/git"
	providermetrics "github.com/stuttgart-things/provider-kubeconfig/internal/metrics"
	vaultpkg "github.com/stuttgart-things/provider-kubeconfig/internal/vault"
)

// tracerName identifies the provider's spans in OpenTelemetry traces.
const tracerName = "github.com/stuttgart-things/provider-kubeconfig"

const (
	errNotRemoteCluster  = "managed resource is not a RemoteCluster custom resource"
	errTrackPCUsage      = "cannot track ProviderConfig usage"
	errGetPC             = "cannot get ProviderConfig"
	errGetCPC            = "cannot get ClusterProviderConfig"
	errGetGitSecret      = "cannot get Git auth secret"
	errGetDecryptSecret  = "cannot get decryption key secret"
	errCloneRepo         = "cannot clone/pull git repository"
	errReadFile          = "cannot read file from git repository"
	errDecryptFile       = "cannot decrypt file"
	errCreateSecret      = "cannot create kubeconfig Secret"
	errGetSecret         = "cannot get kubeconfig Secret"
	errUpdateSecret      = "cannot update kubeconfig Secret"
	errDeleteSecret      = "cannot delete kubeconfig Secret"
	errCreateProviderCfg = "cannot create downstream ProviderConfig"
	errDeleteProviderCfg = "cannot delete downstream ProviderConfig"
	errListProviderCfg   = "cannot list downstream ProviderConfigs"

	annotationContentHash  = "kubeconfig.stuttgart-things.com/content-hash"
	labelManagedBy         = "app.kubernetes.io/managed-by"
	labelRemoteCluster     = "remotecluster.kubeconfig.stuttgart-things.com/name"
	labelArgoCDSecretType  = "argocd.argoproj.io/secret-type"
	defaultSecretNamespace = "crossplane-system"
	defaultArgoCDNamespace = "argocd"

	errVaultRead          = "cannot read kubeconfig from Vault"
	errVaultAuth          = "cannot authenticate to Vault"
	errGetVaultSecret     = "cannot get Vault AppRole secret"
	errBuildArgoCDSecret  = "cannot build ArgoCD cluster secret"
	errCreateArgoCDSecret = "cannot create ArgoCD cluster secret"
	errDeleteArgoCDSecret = "cannot delete ArgoCD cluster secret"
	errParseKubeconfig    = "cannot parse kubeconfig for ArgoCD secret"
)

// providerConfigMeta holds GVK and scope info for a downstream ProviderConfig.
type providerConfigMeta struct {
	GVK        schema.GroupVersionKind
	Namespaced bool
}

// providerConfigGVKs maps (provider-type, api-version-label) to GVK metadata.
// "v1"         = ProviderConfig on *.crossplane.io (cluster-scoped)
// "v2"         = ProviderConfig on *.m.crossplane.io (namespaced)
// "v2-cluster" = ClusterProviderConfig on *.m.crossplane.io (cluster-scoped)
var providerConfigGVKs = map[string]map[string]providerConfigMeta{
	"provider-kubernetes": {
		"v1":         {GVK: schema.GroupVersionKind{Group: "kubernetes.crossplane.io", Version: "v1alpha1", Kind: "ProviderConfig"}, Namespaced: false},
		"v2":         {GVK: schema.GroupVersionKind{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Kind: "ProviderConfig"}, Namespaced: true},
		"v2-cluster": {GVK: schema.GroupVersionKind{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Kind: "ClusterProviderConfig"}, Namespaced: false},
	},
	"provider-helm": {
		"v1":         {GVK: schema.GroupVersionKind{Group: "helm.crossplane.io", Version: "v1beta1", Kind: "ProviderConfig"}, Namespaced: false},
		"v2":         {GVK: schema.GroupVersionKind{Group: "helm.m.crossplane.io", Version: "v1beta1", Kind: "ProviderConfig"}, Namespaced: true},
		"v2-cluster": {GVK: schema.GroupVersionKind{Group: "helm.m.crossplane.io", Version: "v1beta1", Kind: "ClusterProviderConfig"}, Namespaced: false},
	},
}

// resolveAPIVersions returns the API versions for a ProviderConfigRef, defaulting to ["v1"].
func resolveAPIVersions(pcRef v1alpha1.ProviderConfigRef) []string {
	if len(pcRef.APIVersions) == 0 {
		return []string{"v1"}
	}
	return pcRef.APIVersions
}

// allProviderConfigMetas returns all providerConfigMeta entries across all types and versions.
func allProviderConfigMetas() []providerConfigMeta {
	var all []providerConfigMeta
	for _, versions := range providerConfigGVKs {
		for _, meta := range versions {
			all = append(all, meta)
		}
	}
	return all
}

// SetupGated adds a controller that reconciles RemoteCluster managed resources with safe-start support.
func SetupGated(mgr ctrl.Manager, o controller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			panic(errors.Wrap(err, "cannot setup RemoteCluster controller"))
		}
	}, v1alpha1.RemoteClusterGroupVersionKind)
	return nil
}

func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.RemoteClusterGroupKind)

	opts := []managed.ReconcilerOption{
		managed.WithExternalConnector(&connector{
			kube:  mgr.GetClient(),
			usage: resource.NewProviderConfigUsageTracker(mgr.GetClient(), &apisv1alpha1.ClusterProviderConfigUsage{}),
		}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))), //nolint:staticcheck // crossplane runtime requires old event API
	}

	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		opts = append(opts, managed.WithManagementPolicies())
	}

	if o.Features.Enabled(feature.EnableAlphaChangeLogs) {
		opts = append(opts, managed.WithChangeLogger(o.ChangeLogOptions.ChangeLogger))
	}

	if o.MetricOptions != nil {
		opts = append(opts, managed.WithMetricRecorder(o.MetricOptions.MRMetrics))
	}

	if o.MetricOptions != nil && o.MetricOptions.MRStateMetrics != nil {
		stateMetricsRecorder := statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics, &v1alpha1.RemoteClusterList{}, o.MetricOptions.PollStateMetricInterval,
		)
		if err := mgr.Add(stateMetricsRecorder); err != nil {
			return errors.Wrap(err, "cannot register MR state metrics recorder for kind v1alpha1.RemoteClusterList")
		}
	}

	r := managed.NewReconciler(mgr, resource.ManagedKind(v1alpha1.RemoteClusterGroupVersionKind), opts...)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		WithOptions(o.ForControllerRuntime()).
		WithEventFilter(resource.DesiredStateChanged()).
		For(&v1alpha1.RemoteCluster{}).
		Complete(ratelimiter.NewReconciler(name, r, o.GlobalRateLimiter))
}

type connector struct {
	kube  client.Client
	usage *resource.ProviderConfigUsageTracker
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	log := ctrl.LoggerFrom(ctx)

	cr, ok := mg.(*v1alpha1.RemoteCluster)
	if !ok {
		return nil, errors.New(errNotRemoteCluster)
	}

	if err := c.usage.Track(ctx, cr); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	spec, err := c.resolveProviderConfigSpec(ctx, cr)
	if err != nil {
		log.Info("Failed to resolve ProviderConfig", "error", err)
		return nil, err
	}

	if spec.Vault != nil && spec.Git != nil {
		return nil, errors.New("providerConfig must specify either git or vault, not both")
	}

	// Vault source path
	if spec.Vault != nil {
		log.V(1).Info("Resolved ProviderConfig (vault)", "address", spec.Vault.Address, "authMethod", spec.Vault.Auth.Method)
		vc, err := c.resolveVaultClient(ctx, spec)
		if err != nil {
			log.Info("Failed to create Vault client", "error", err)
			return nil, errors.Wrap(err, errVaultAuth)
		}
		return &external{kube: c.kube, providerSpec: *spec, vaultClient: vc}, nil
	}

	// Git source path
	if spec.Git == nil {
		return nil, errors.New("providerConfig must specify git or vault configuration")
	}
	return c.connectGit(ctx, spec)
}

func (c *connector) connectGit(ctx context.Context, spec *apisv1alpha1.ProviderConfigSpec) (managed.ExternalClient, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Resolved ProviderConfig (git)", "gitURL", spec.Git.URL, "branch", spec.Git.Branch)

	gitToken, err := c.resolveGitToken(ctx, spec)
	if err != nil {
		log.Info("Failed to resolve Git token", "error", err)
		return nil, err
	}
	if gitToken != "" && spec.Git.SecretRef != nil {
		log.V(1).Info("Git token resolved", "secretRef", spec.Git.SecretRef.Name)
	}

	ageKey, err := c.resolveAgeKey(ctx, spec)
	if err != nil {
		log.Info("Failed to resolve decryption key", "error", err)
		return nil, err
	}
	if ageKey == "" && spec.Decryption != nil {
		log.Info("Decryption key is empty — kubeconfig will not be decrypted", "secretRef", spec.Decryption.SecretRef.Name)
	}

	return &external{kube: c.kube, providerSpec: *spec, gitToken: gitToken, ageKey: ageKey}, nil
}

func (c *connector) resolveProviderConfigSpec(ctx context.Context, cr *v1alpha1.RemoteCluster) (*apisv1alpha1.ProviderConfigSpec, error) {
	ref := cr.GetProviderConfigReference()
	if ref == nil {
		return nil, errors.New("providerConfigRef is not set")
	}

	switch ref.Kind {
	case "ProviderConfig":
		pc := &apisv1alpha1.ProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, pc); err != nil {
			return nil, errors.Wrap(err, errGetPC)
		}
		return &pc.Spec, nil
	case "ClusterProviderConfig":
		cpc := &apisv1alpha1.ClusterProviderConfig{}
		if err := c.kube.Get(ctx, types.NamespacedName{Name: ref.Name}, cpc); err != nil {
			return nil, errors.Wrap(err, errGetCPC)
		}
		return &cpc.Spec, nil
	default:
		return nil, errors.Errorf("unsupported provider config kind: %s", ref.Kind)
	}
}

func (c *connector) resolveVaultClient(ctx context.Context, spec *apisv1alpha1.ProviderConfigSpec) (*vaultpkg.Client, error) {
	vc := spec.Vault

	var authFn func(*vaultapi.Client) error
	switch vc.Auth.Method {
	case "kubernetes":
		if vc.Auth.Kubernetes == nil {
			return nil, errors.New("vault auth method is kubernetes but kubernetes config is not set")
		}
		authFn = vaultpkg.AuthKubernetes(vc.Auth.Kubernetes.Role, vc.Auth.Kubernetes.MountPath)
	case "approle":
		if vc.Auth.AppRole == nil {
			return nil, errors.New("vault auth method is approle but appRole config is not set")
		}
		// Resolve the secret-id from the referenced Secret
		secret := &corev1.Secret{}
		if err := c.kube.Get(ctx, types.NamespacedName{
			Name:      vc.Auth.AppRole.SecretRef.Name,
			Namespace: vc.Auth.AppRole.SecretRef.Namespace,
		}, secret); err != nil {
			return nil, errors.Wrap(err, errGetVaultSecret)
		}
		secretID := string(secret.Data["secret-id"])
		authFn = vaultpkg.AuthAppRole(vc.Auth.AppRole.RoleID, secretID, vc.Auth.AppRole.MountPath)
	default:
		return nil, errors.Errorf("unsupported vault auth method: %s", vc.Auth.Method)
	}

	return vaultpkg.New(ctx, vc.Address, vc.Namespace, vc.MountPath, authFn)
}

func (c *connector) resolveGitToken(ctx context.Context, spec *apisv1alpha1.ProviderConfigSpec) (string, error) {
	if spec.Git == nil || spec.Git.SecretRef == nil {
		return "", nil
	}
	secret := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{
		Name:      spec.Git.SecretRef.Name,
		Namespace: spec.Git.SecretRef.Namespace,
	}, secret); err != nil {
		return "", errors.Wrap(err, errGetGitSecret)
	}
	return string(secret.Data["token"]), nil
}

func (c *connector) resolveAgeKey(ctx context.Context, spec *apisv1alpha1.ProviderConfigSpec) (string, error) {
	if spec.Decryption == nil {
		return "", nil
	}
	decryptRef := spec.Decryption.SecretRef
	secret := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{
		Name:      decryptRef.Name,
		Namespace: decryptRef.Namespace,
	}, secret); err != nil {
		return "", errors.Wrap(err, errGetDecryptSecret)
	}
	return string(secret.Data["key"]), nil
}

type external struct {
	kube         client.Client
	providerSpec apisv1alpha1.ProviderConfigSpec
	gitToken     string
	ageKey       string
	vaultClient  *vaultpkg.Client
}

// secretName returns a deterministic Secret name for the RemoteCluster.
func secretName(crName string) string {
	return fmt.Sprintf("kubeconfig-%s", crName)
}

// contentHash returns the hex-encoded SHA-256 hash of data.
func contentHash(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// cloneAndReadFile clones/pulls the Git repo, reads the file at source.path,
// isUpToDate checks if the kubeconfig content has changed (drift detection).
func (c *external) isUpToDate(ctx context.Context, cr *v1alpha1.RemoteCluster, secret *corev1.Secret) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	source := cr.Spec.ForProvider.Source

	if source.Type == "vault" || c.vaultClient != nil {
		_, version, err := c.readFromVault(ctx, source.Path, source.Key)
		if err != nil {
			return false, err
		}
		if version != cr.Status.AtProvider.VaultSecretVersion {
			log.Info("Vault version drift detected", "cluster", cr.GetName(), "storedVersion", cr.Status.AtProvider.VaultSecretVersion, "currentVersion", version)
			return false, nil
		}
		return true, nil
	}

	content, err := c.cloneAndReadFile(ctx, source.Path)
	if err != nil {
		log.Info("Failed to clone/read git file during observe", "path", source.Path, "error", err)
		return false, err
	}
	currentHash := contentHash(content)
	storedHash := secret.GetAnnotations()[annotationContentHash]
	if currentHash != storedHash {
		log.Info("Content drift detected", "cluster", cr.GetName(), "storedHash", storedHash, "currentHash", currentHash)
		return false, nil
	}
	return true, nil
}

// readFromVault reads a kubeconfig from Vault KVv2.
func (c *external) readFromVault(ctx context.Context, path, key string) ([]byte, int, error) {
	log := ctrl.LoggerFrom(ctx)

	if key == "" {
		key = "kubeconfig"
	}

	data, version, err := c.vaultClient.ReadKVv2(ctx, path)
	if err != nil {
		log.Info("Vault KVv2 read failed", "path", path, "error", err)
		return nil, 0, errors.Wrap(err, errVaultRead)
	}

	val, ok := data[key]
	if !ok {
		return nil, 0, errors.Errorf("key %q not found in Vault secret at %q", key, path)
	}
	content, ok := val.(string)
	if !ok {
		return nil, 0, errors.Errorf("Vault key %q is not a string", key)
	}

	log.V(1).Info("Read kubeconfig from Vault", "path", path, "key", key, "version", version)
	return []byte(content), version, nil
}

func (c *external) cloneAndReadFile(ctx context.Context, filePath string) ([]byte, error) {
	log := ctrl.LoggerFrom(ctx)
	tracer := otel.Tracer(tracerName)

	repoURL := c.providerSpec.Git.URL
	branch := c.providerSpec.Git.Branch
	revision := c.providerSpec.Git.Revision
	repo := gitpkg.NewRepo(repoURL, branch, revision, c.gitToken)

	// --- git fetch (clone/pull/revision) ---
	fetchCtx, span := tracer.Start(ctx, "git.EnsureCloned", trace.WithAttributes(
		attribute.String("git.repo", repoURL),
		attribute.String("git.branch", branch),
		attribute.String("git.revision", revision),
	))
	start := time.Now()
	_, op, err := repo.EnsureCloned(fetchCtx)
	span.SetAttributes(attribute.String("git.operation", string(op)))
	providermetrics.GitFetchDuration.WithLabelValues(repoURL, branch, string(op), result(err)).Observe(time.Since(start).Seconds())
	providermetrics.GitCacheOps.WithLabelValues(repoURL, branch, string(op)).Inc()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "git fetch failed")
		span.End()
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageGit).Inc()
		log.Info("Git clone/pull failed", "url", repoURL, "branch", branch, "revision", revision, "error", err)
		return nil, errors.Wrapf(err, "%s (url=%q branch=%q revision=%q)", errCloneRepo, repoURL, branch, revision)
	}
	span.End()
	log.V(1).Info("Git repo ready", "url", repoURL, "branch", branch, "revision", revision, "operation", op)

	// --- read file ---
	_, readSpan := tracer.Start(ctx, "git.ReadFile", trace.WithAttributes(attribute.String("git.path", filePath)))
	content, err := repo.ReadFile(filePath)
	if err != nil {
		readSpan.RecordError(err)
		readSpan.SetStatus(codes.Error, "read file failed")
		readSpan.End()
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageGit).Inc()
		log.Info("Failed to read file from git repo", "path", filePath, "error", err)
		return nil, errors.Wrapf(err, "%s (url=%q path=%q)", errReadFile, repoURL, filePath)
	}
	readSpan.End()

	// --- decrypt (if an age key is available) ---
	if c.ageKey != "" {
		format := decryptpkg.FormatFromPath(filePath)
		_, decSpan := tracer.Start(ctx, "sops.Decrypt", trace.WithAttributes(
			attribute.String("sops.format", format),
			attribute.String("git.path", filePath),
		))
		start := time.Now()
		decrypted, err := decryptpkg.SOPSDecrypt(content, filePath, c.ageKey)
		providermetrics.SOPSDecryptDuration.WithLabelValues(format, result(err)).Observe(time.Since(start).Seconds())
		if err != nil {
			decSpan.RecordError(err)
			decSpan.SetStatus(codes.Error, "decrypt failed")
			decSpan.End()
			providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageDecrypt).Inc()
			log.Info("SOPS decryption failed", "path", filePath, "error", err)
			return nil, errors.Wrapf(err, "%s (url=%q path=%q)", errDecryptFile, repoURL, filePath)
		}
		decSpan.End()
		log.V(1).Info("Decrypted kubeconfig", "path", filePath)
		return decrypted, nil
	}

	return content, nil
}

// result maps an error to a Prometheus result label value.
func result(err error) string {
	if err != nil {
		return providermetrics.ResultError
	}
	return providermetrics.ResultSuccess
}

// buildDownstreamProviderConfig builds an unstructured downstream ProviderConfig
// for provider-kubernetes or provider-helm using the resolved providerConfigMeta.
func buildDownstreamProviderConfig(meta providerConfigMeta, pcName, secretName, secretNamespace, crName string) (*unstructured.Unstructured, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(meta.GVK)
	u.SetName(pcName)
	u.SetLabels(map[string]string{
		labelManagedBy:     "provider-kubeconfig",
		labelRemoteCluster: crName,
	})
	if meta.Namespaced {
		u.SetNamespace(secretNamespace)
	}

	if err := unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"source": "Secret",
		"secretRef": map[string]interface{}{
			"name":      secretName,
			"namespace": secretNamespace,
			"key":       "kubeconfig",
		},
	}, "spec", "credentials"); err != nil {
		return nil, errors.Wrap(err, "cannot set credentials in downstream ProviderConfig")
	}

	return u, nil
}

// argoCDClusterConfig is the JSON config block for an ArgoCD cluster secret.
type argoCDClusterConfig struct {
	BearerToken     string          `json:"bearerToken"` //nolint:gosec // ArgoCD cluster secret schema requires this field name
	TLSClientConfig argoCDTLSConfig `json:"tlsClientConfig"`
}

// argoCDTLSConfig holds TLS settings for the ArgoCD cluster config.
type argoCDTLSConfig struct {
	Insecure bool   `json:"insecure"`
	CAData   string `json:"caData,omitempty"`
}

// buildArgoCDClusterSecret creates a Kubernetes Secret in the ArgoCD cluster secret format
// by extracting the server URL and bearer token from the kubeconfig.
func buildArgoCDClusterSecret(name, namespace, crName string, kubeconfig []byte) (*corev1.Secret, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return nil, errors.Wrap(err, errParseKubeconfig)
	}

	argoConfig := argoCDClusterConfig{
		BearerToken: cfg.BearerToken,
		TLSClientConfig: argoCDTLSConfig{
			Insecure: cfg.Insecure,
		},
	}

	// Include CA data if present and not insecure
	if len(cfg.CAData) > 0 && !cfg.Insecure {
		argoConfig.TLSClientConfig.CAData = string(cfg.CAData)
	}

	configJSON, err := json.Marshal(argoConfig) //nolint:gosec // G117: BearerToken field name required by ArgoCD cluster secret schema
	if err != nil {
		return nil, errors.Wrap(err, "cannot marshal ArgoCD cluster config")
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				labelManagedBy:        "provider-kubeconfig",
				labelRemoteCluster:    crName,
				labelArgoCDSecretType: "cluster",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"name":   crName,
			"server": cfg.Host,
			"config": string(configJSON),
		},
	}, nil
}

// lookupProviderConfigMeta resolves the providerConfigMeta for a given type and API version label.
func lookupProviderConfigMeta(providerType, apiVer string) (providerConfigMeta, error) {
	versions, ok := providerConfigGVKs[providerType]
	if !ok {
		return providerConfigMeta{}, errors.Errorf("unsupported downstream provider type: %s", providerType)
	}
	meta, ok := versions[apiVer]
	if !ok {
		return providerConfigMeta{}, errors.Errorf("unsupported API version %q for provider type %s", apiVer, providerType)
	}
	return meta, nil
}

// desiredKey uniquely identifies a desired downstream ProviderConfig by name and GVK.
type desiredKey struct {
	Name string
	GVK  schema.GroupVersionKind
}

// ensureOneProviderConfig creates a single downstream ProviderConfig if it doesn't exist.
func (c *external) ensureOneProviderConfig(ctx context.Context, meta providerConfigMeta, pcName, sName, sNamespace, crName, apiVer string) error {
	u, err := buildDownstreamProviderConfig(meta, pcName, sName, sNamespace, crName)
	if err != nil {
		return err
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(meta.GVK)
	key := types.NamespacedName{Name: pcName}
	if meta.Namespaced {
		key.Namespace = sNamespace
	}
	err = c.kube.Get(ctx, key, existing)
	if kerrors.IsNotFound(err) {
		if err := c.kube.Create(ctx, u); err != nil {
			return errors.Wrapf(err, "%s %q (apiVersion %s)", errCreateProviderCfg, pcName, apiVer)
		}
		return nil
	}
	return errors.Wrapf(err, "cannot check downstream ProviderConfig %q", pcName)
}

// ensureDownstreamProviderConfigs creates any missing downstream ProviderConfigs
// and deletes stale ones that are no longer in the spec.
func (c *external) ensureDownstreamProviderConfigs(ctx context.Context, cr *v1alpha1.RemoteCluster, sName, sNamespace string, kubeconfig []byte) error {
	desired := make(map[desiredKey]bool)

	for _, pc := range cr.Spec.ForProvider.ProviderConfigs {
		if pc.Type == "argocd-cluster-secret" {
			if err := c.ensureArgoCDClusterSecret(ctx, pc, cr.GetName(), kubeconfig); err != nil {
				return err
			}
			continue
		}

		for _, apiVer := range resolveAPIVersions(pc) {
			meta, err := lookupProviderConfigMeta(pc.Type, apiVer)
			if err != nil {
				return err
			}
			desired[desiredKey{Name: pc.Name, GVK: meta.GVK}] = true

			if err := c.ensureOneProviderConfig(ctx, meta, pc.Name, sName, sNamespace, cr.GetName(), apiVer); err != nil {
				return err
			}
		}
	}

	// Delete stale ProviderConfigs that have our label but are no longer desired
	for _, meta := range allProviderConfigMetas() {
		if err := c.deleteStaleProviderConfigs(ctx, meta, cr.GetName(), sNamespace, desired); err != nil {
			return err
		}
	}

	// Delete stale ArgoCD cluster secrets
	if err := c.deleteStaleArgoCDSecrets(ctx, cr); err != nil {
		return err
	}

	return nil
}

// ensureArgoCDClusterSecret creates an ArgoCD cluster secret if it doesn't exist,
// or updates it if the kubeconfig has changed.
func (c *external) ensureArgoCDClusterSecret(ctx context.Context, pc v1alpha1.ProviderConfigRef, crName string, kubeconfig []byte) error {
	ns := pc.Namespace
	if ns == "" {
		ns = defaultArgoCDNamespace
	}

	desired, err := buildArgoCDClusterSecret(pc.Name, ns, crName, kubeconfig)
	if err != nil {
		return errors.Wrap(err, errBuildArgoCDSecret)
	}

	existing := &corev1.Secret{}
	err = c.kube.Get(ctx, types.NamespacedName{Name: pc.Name, Namespace: ns}, existing)
	if kerrors.IsNotFound(err) {
		if err := c.kube.Create(ctx, desired); err != nil {
			return errors.Wrap(err, errCreateArgoCDSecret)
		}
		return nil
	}
	if err != nil {
		return errors.Wrapf(err, "cannot check ArgoCD cluster secret %q", pc.Name)
	}

	// Update if content changed
	existing.StringData = desired.StringData
	existing.Labels = desired.Labels
	if err := c.kube.Update(ctx, existing); err != nil {
		return errors.Wrapf(err, "cannot update ArgoCD cluster secret %q", pc.Name)
	}
	return nil
}

// deleteStaleArgoCDSecrets deletes ArgoCD cluster secrets owned by this CR
// that are no longer in the spec.
func (c *external) deleteStaleArgoCDSecrets(ctx context.Context, cr *v1alpha1.RemoteCluster) error {
	// Build set of desired ArgoCD secret names
	desired := make(map[string]bool)
	for _, pc := range cr.Spec.ForProvider.ProviderConfigs {
		if pc.Type == "argocd-cluster-secret" {
			desired[pc.Name] = true
		}
	}

	// List all secrets with our labels
	secretList := &corev1.SecretList{}
	selector := labels.SelectorFromSet(labels.Set{
		labelManagedBy:        "provider-kubeconfig",
		labelRemoteCluster:    cr.GetName(),
		labelArgoCDSecretType: "cluster",
	})
	if err := c.kube.List(ctx, secretList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return errors.Wrap(err, "cannot list ArgoCD cluster secrets")
	}

	for i := range secretList.Items {
		if !desired[secretList.Items[i].Name] {
			if err := c.kube.Delete(ctx, &secretList.Items[i]); err != nil && !kerrors.IsNotFound(err) {
				return errors.Wrapf(err, "%s %q", errDeleteArgoCDSecret, secretList.Items[i].Name)
			}
		}
	}
	return nil
}

// deleteStaleProviderConfigs deletes ProviderConfigs with the ownership label
// that are no longer in the desired set.
func (c *external) deleteStaleProviderConfigs(ctx context.Context, meta providerConfigMeta, crName, namespace string, desired map[desiredKey]bool) error {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(meta.GVK)
	selector := labels.SelectorFromSet(labels.Set{
		labelRemoteCluster: crName,
	})
	opts := &client.ListOptions{LabelSelector: selector}
	if meta.Namespaced {
		opts.Namespace = namespace
	}
	if err := c.kube.List(ctx, list, opts); err != nil {
		if !kerrors.IsNotFound(err) {
			return errors.Wrap(err, errListProviderCfg)
		}
		return nil
	}
	for i := range list.Items {
		item := &list.Items[i]
		if !desired[desiredKey{Name: item.GetName(), GVK: meta.GVK}] {
			if err := c.kube.Delete(ctx, item); err != nil && !kerrors.IsNotFound(err) {
				return errors.Wrapf(err, "%s %q", errDeleteProviderCfg, item.GetName())
			}
		}
	}
	return nil
}

// deleteAllDownstreamProviderConfigs deletes all ProviderConfigs and ArgoCD secrets owned by this CR.
func (c *external) deleteAllDownstreamProviderConfigs(ctx context.Context, crName, namespace string) error {
	for _, meta := range allProviderConfigMetas() {
		if err := c.deleteStaleProviderConfigs(ctx, meta, crName, namespace, nil); err != nil {
			return err
		}
	}

	// Delete all ArgoCD cluster secrets owned by this CR
	secretList := &corev1.SecretList{}
	selector := labels.SelectorFromSet(labels.Set{
		labelManagedBy:        "provider-kubeconfig",
		labelRemoteCluster:    crName,
		labelArgoCDSecretType: "cluster",
	})
	if err := c.kube.List(ctx, secretList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return errors.Wrap(err, "cannot list ArgoCD cluster secrets for deletion")
	}
	for i := range secretList.Items {
		if err := c.kube.Delete(ctx, &secretList.Items[i]); err != nil && !kerrors.IsNotFound(err) {
			return errors.Wrapf(err, "%s %q", errDeleteArgoCDSecret, secretList.Items[i].Name)
		}
	}

	return nil
}

// downstreamProviderConfigsUpToDate checks that all desired downstream ProviderConfigs and ArgoCD secrets exist.
func (c *external) downstreamProviderConfigsUpToDate(ctx context.Context, cr *v1alpha1.RemoteCluster) bool {
	ns := cr.Spec.ForProvider.SecretNamespace
	if ns == "" {
		ns = defaultSecretNamespace
	}
	for _, pc := range cr.Spec.ForProvider.ProviderConfigs {
		if pc.Type == "argocd-cluster-secret" {
			argoNS := pc.Namespace
			if argoNS == "" {
				argoNS = defaultArgoCDNamespace
			}
			s := &corev1.Secret{}
			if err := c.kube.Get(ctx, types.NamespacedName{Name: pc.Name, Namespace: argoNS}, s); err != nil {
				return false
			}
			continue
		}

		for _, apiVer := range resolveAPIVersions(pc) {
			meta, err := lookupProviderConfigMeta(pc.Type, apiVer)
			if err != nil {
				return false
			}
			u := &unstructured.Unstructured{}
			u.SetGroupVersionKind(meta.GVK)
			key := types.NamespacedName{Name: pc.Name}
			if meta.Namespaced {
				key.Namespace = ns
			}
			if err := c.kube.Get(ctx, key, u); err != nil {
				return false
			}
		}
	}
	return true
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	log := ctrl.LoggerFrom(ctx)

	cr, ok := mg.(*v1alpha1.RemoteCluster)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRemoteCluster)
	}

	ns := cr.Spec.ForProvider.SecretNamespace
	if ns == "" {
		ns = defaultSecretNamespace
	}
	name := secretName(cr.GetName())

	secret := &corev1.Secret{}
	err := c.kube.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret)
	if kerrors.IsNotFound(err) {
		log.V(1).Info("Kubeconfig secret not found, will create", "secret", name, "namespace", ns)
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errGetSecret)
	}

	// Detect drift
	upToDate, err := c.isUpToDate(ctx, cr, secret)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	// Check that all downstream ProviderConfigs exist
	if upToDate && !c.downstreamProviderConfigsUpToDate(ctx, cr) {
		log.Info("Downstream ProviderConfigs out of date", "cluster", cr.GetName())
		upToDate = false
	}

	cr.SetConditions(xpv2.Available())
	obs := v1alpha1.RemoteClusterObservation{
		ClusterName: cr.GetName(),
		SecretRef:   name,
	}

	// Best-effort: gather remote cluster info from the kubeconfig
	if info, err := clusterpkg.Gather(ctx, secret.Data["kubeconfig"]); err == nil {
		obs.ServerVersion = info.ServerVersion
		obs.APIEndpoint = info.APIEndpoint
		obs.PodCIDR = info.PodCIDR
		obs.ServiceCIDR = info.ServiceCIDR
		obs.NodeCIDRs = info.NodeCIDRs
		obs.NodeCount = info.NodeCount
		obs.InternalNetworkKey = info.InternalNetworkKey
		obs.ClusterType = info.ClusterType
		obs.KindClusterName = info.KindClusterName
		log.V(1).Info("Gathered cluster info", "cluster", cr.GetName(), "version", info.ServerVersion, "type", info.ClusterType, "kindName", info.KindClusterName, "nodes", info.NodeCount)
	} else {
		log.V(1).Info("Could not gather cluster info (target may be unreachable)", "cluster", cr.GetName(), "error", err)
	}

	cr.Status.AtProvider = obs

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate,
		ConnectionDetails: managed.ConnectionDetails{
			"kubeconfig": secret.Data["kubeconfig"],
		},
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	log := ctrl.LoggerFrom(ctx)

	cr, ok := mg.(*v1alpha1.RemoteCluster)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRemoteCluster)
	}

	ns := cr.Spec.ForProvider.SecretNamespace
	if ns == "" {
		ns = defaultSecretNamespace
	}
	name := secretName(cr.GetName())

	// Read the kubeconfig from the configured source
	source := cr.Spec.ForProvider.Source
	log.Info("Creating kubeconfig secret", "cluster", cr.GetName(), "path", source.Path, "sourceType", source.Type, "secret", name)

	var (
		content      []byte
		vaultVersion int
		err          error
	)
	if source.Type == "vault" || c.vaultClient != nil {
		content, vaultVersion, err = c.readFromVault(ctx, source.Path, source.Key)
	} else {
		content, err = c.cloneAndReadFile(ctx, source.Path)
	}
	if err != nil {
		log.Info("Failed to read kubeconfig", "path", source.Path, "error", err)
		return managed.ExternalCreation{}, err
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				labelManagedBy:     "provider-kubeconfig",
				labelRemoteCluster: cr.GetName(),
			},
			Annotations: map[string]string{
				annotationContentHash: contentHash(content),
			},
		},
		Data: map[string][]byte{
			"kubeconfig": content,
		},
	}

	if err := c.kube.Create(ctx, secret); err != nil {
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageSecret).Inc()
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateSecret)
	}
	log.Info("Created kubeconfig secret", "cluster", cr.GetName(), "secret", name, "namespace", ns)

	// Create downstream ProviderConfigs and ArgoCD secrets
	if err := c.ensureDownstreamProviderConfigs(ctx, cr, name, ns, content); err != nil {
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageDownstream).Inc()
		log.Info("Failed to create downstream ProviderConfigs", "cluster", cr.GetName(), "error", err)
		return managed.ExternalCreation{}, err
	}
	log.Info("Created downstream ProviderConfigs", "cluster", cr.GetName(), "count", len(cr.Spec.ForProvider.ProviderConfigs))

	cr.Status.AtProvider = v1alpha1.RemoteClusterObservation{
		ClusterName:        cr.GetName(),
		SecretRef:          name,
		VaultSecretVersion: vaultVersion,
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{
			"kubeconfig": content,
		},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.RemoteCluster)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRemoteCluster)
	}

	ns := cr.Spec.ForProvider.SecretNamespace
	if ns == "" {
		ns = defaultSecretNamespace
	}
	name := secretName(cr.GetName())

	// Read the kubeconfig from the configured source
	source := cr.Spec.ForProvider.Source
	var (
		content      []byte
		vaultVersion int
		err          error
	)
	if source.Type == "vault" || c.vaultClient != nil {
		content, vaultVersion, err = c.readFromVault(ctx, source.Path, source.Key)
	} else {
		content, err = c.cloneAndReadFile(ctx, source.Path)
	}
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// Fetch and update the existing Secret
	secret := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret); err != nil {
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageSecret).Inc()
		return managed.ExternalUpdate{}, errors.Wrap(err, errGetSecret)
	}

	secret.Data["kubeconfig"] = content
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations[annotationContentHash] = contentHash(content)
	cr.Status.AtProvider.VaultSecretVersion = vaultVersion

	if err := c.kube.Update(ctx, secret); err != nil {
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageSecret).Inc()
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateSecret)
	}

	// Reconcile downstream ProviderConfigs and ArgoCD secrets (create missing, delete stale)
	if err := c.ensureDownstreamProviderConfigs(ctx, cr, name, ns, content); err != nil {
		providermetrics.ReconcileErrors.WithLabelValues(providermetrics.StageDownstream).Inc()
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{
			"kubeconfig": content,
		},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	log := ctrl.LoggerFrom(ctx)

	cr, ok := mg.(*v1alpha1.RemoteCluster)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotRemoteCluster)
	}

	ns := cr.Spec.ForProvider.SecretNamespace
	if ns == "" {
		ns = defaultSecretNamespace
	}
	name := secretName(cr.GetName())

	log.Info("Deleting RemoteCluster resources", "cluster", cr.GetName(), "secret", name)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
	}
	if err := c.kube.Delete(ctx, secret); err != nil && !kerrors.IsNotFound(err) {
		return managed.ExternalDelete{}, errors.Wrap(err, errDeleteSecret)
	}

	// Delete all downstream ProviderConfigs owned by this CR
	if err := c.deleteAllDownstreamProviderConfigs(ctx, cr.GetName(), ns); err != nil {
		log.Info("Failed to delete downstream ProviderConfigs", "cluster", cr.GetName(), "error", err)
		return managed.ExternalDelete{}, err
	}
	log.Info("Deleted RemoteCluster resources", "cluster", cr.GetName())

	return managed.ExternalDelete{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}
