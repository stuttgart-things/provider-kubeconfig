# provider-kubeconfig

[![CI - Build & Test](https://github.com/stuttgart-things/provider-kubeconfig/actions/workflows/build-test.yaml/badge.svg)](https://github.com/stuttgart-things/provider-kubeconfig/actions/workflows/build-test.yaml)
[![Build, Push & Scan Container Image](https://github.com/stuttgart-things/provider-kubeconfig/actions/workflows/build-scan-image.yaml/badge.svg)](https://github.com/stuttgart-things/provider-kubeconfig/actions/workflows/build-scan-image.yaml)
[![Latest Release](https://img.shields.io/github/v/release/stuttgart-things/provider-kubeconfig)](https://github.com/stuttgart-things/provider-kubeconfig/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/stuttgart-things/provider-kubeconfig)](https://goreportcard.com/report/github.com/stuttgart-things/provider-kubeconfig)
[![Go Version](https://img.shields.io/github/go-mod/go-version/stuttgart-things/provider-kubeconfig)](go.mod)
[![License](https://img.shields.io/github/license/stuttgart-things/provider-kubeconfig)](LICENSE)

`provider-kubeconfig` is a [Crossplane](https://crossplane.io/) Provider that
manages remote Kubernetes cluster kubeconfigs. It reads kubeconfig files from
**Git repositories** (SOPS-encrypted) or **HashiCorp Vault** (KVv2), and
bootstraps Secrets and downstream ProviderConfigs for the remote clusters.

## Features

- **Dual source support** — read kubeconfigs from Git+SOPS or Vault KVv2
- **Git-based kubeconfig management** — clones/pulls a Git repo and reads encrypted kubeconfig files
- **SOPS/age decryption** — decrypts kubeconfigs encrypted with [SOPS](https://github.com/getsops/sops) using [age](https://age-encryption.org/) keys
- **Vault KVv2 integration** — reads kubeconfigs from Vault with Kubernetes auth or AppRole auth
- **Drift detection** — content hash comparison for Git, KVv2 version tracking for Vault
- **Downstream ProviderConfigs** — automatically creates `provider-kubernetes` and `provider-helm` ProviderConfig/ClusterProviderConfig resources referencing the kubeconfig Secret
- **ArgoCD cluster secrets** — optionally creates ArgoCD-compatible cluster secrets
- **Cluster type detection** — auto-detects Kubernetes distribution (`kind`, `k3s`, `rke2`, `k8s`) from server version and node metadata
- **Remote cluster status** — gathers metadata from the remote cluster (version, type, API endpoint, node count, CIDRs, internal network key) and exposes it in `status.atProvider`
- **Stale git cache recovery** — automatically re-clones when pull fails with stale objects
- **Structured logging** — logs key events (git clone, decryption, vault read, secret creation, downstream provisioning) for easier debugging
- **RBAC self-bootstrap** — automatically creates/updates the ClusterRole and ClusterRoleBinding for downstream ProviderConfig management on startup

## Custom Resource Types

| Kind | Scope | Description |
|------|-------|-------------|
| `ProviderConfig` | Namespaced | Git/Vault + decryption settings (namespaced) |
| `ClusterProviderConfig` | Cluster | Git/Vault + decryption settings (cluster-scoped) |
| `RemoteCluster` | Cluster | Managed resource — reads kubeconfig, creates Secret + downstream ProviderConfigs |

## Quick Start

### 1. Install the Provider

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubeconfig
spec:
  package: ghcr.io/stuttgart-things/provider-kubeconfig-xpkg:v0.11.0
```

---

## Source: Git + SOPS

### Secrets

Create the age decryption key Secret (the key field must be named `key`):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: age-key
  namespace: crossplane-system
stringData:
  key: AGE-SECRET-KEY-1XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX
```

For private Git repos, create a token Secret (the field must be named `token`):

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: git-credentials
  namespace: crossplane-system
stringData:
  token: ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

### ClusterProviderConfig (public repo)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  git:
    url: https://github.com/my-org/my-public-repo.git
    branch: main
    # revision: v1.2.3   # optional: pin to a commit SHA or tag for
    #                    # deterministic, rollback-friendly reconciles.
    #                    # When set, the branch tip is ignored.
  decryption:
    provider: sops
    secretRef:
      name: age-key
      namespace: crossplane-system
```

### ClusterProviderConfig (private repo)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: my-private-repo
spec:
  git:
    url: https://github.com/my-org/my-private-repo.git
    branch: main
    secretRef:
      name: git-credentials
      namespace: crossplane-system
  decryption:
    provider: sops
    secretRef:
      name: age-key-private
      namespace: crossplane-system
```

### RemoteCluster (Git source)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: RemoteCluster
metadata:
  name: my-cluster
spec:
  forProvider:
    source:
      path: clusters/my-cluster/kubeconfig.enc.yaml
    secretNamespace: crossplane-system
    providerConfigs:
      - name: my-cluster-kubernetes
        type: provider-kubernetes
        apiVersions: [v2-cluster]
      - name: my-cluster-helm
        type: provider-helm
        apiVersions: [v2-cluster]
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
```

> **Note:** Each repo/key combination needs its own ClusterProviderConfig. Multiple RemoteClusters can reference the same ClusterProviderConfig.

---

## Source: Vault KVv2

### ClusterProviderConfig (Kubernetes auth)

Zero-config authentication — uses the provider pod's service account token:

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: vault-k8s
spec:
  vault:
    address: https://vault.example.com
    auth:
      method: kubernetes
      kubernetes:
        role: provider-kubeconfig
```

### ClusterProviderConfig (AppRole auth)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: vault-approle
spec:
  vault:
    address: https://vault.example.com
    auth:
      method: approle
      appRole:
        roleId: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
        secretRef:
          name: vault-approle-secret
          namespace: crossplane-system
---
apiVersion: v1
kind: Secret
metadata:
  name: vault-approle-secret
  namespace: crossplane-system
stringData:
  secret-id: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

### ClusterProviderConfig (Vault Enterprise namespace)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: vault-enterprise
spec:
  vault:
    address: https://vault.example.com
    namespace: my-team
    mountPath: kv            # non-default KVv2 mount path
    auth:
      method: kubernetes
      kubernetes:
        role: provider-kubeconfig
        mountPath: kubernetes  # auth mount path
```

### RemoteCluster (Vault source)

```yaml
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: RemoteCluster
metadata:
  name: my-cluster
spec:
  forProvider:
    source:
      type: vault
      path: clusters/my-cluster       # KVv2 secret path
      key: kubeconfig                  # key within the secret (default: kubeconfig)
    secretNamespace: crossplane-system
    providerConfigs:
      - name: my-cluster-kubernetes
        type: provider-kubernetes
        apiVersions: [v2-cluster]
      - name: my-cluster-helm
        type: provider-helm
        apiVersions: [v2-cluster]
  providerConfigRef:
    name: vault-k8s
    kind: ClusterProviderConfig
```

### Storing a kubeconfig in Vault

```shell
# Write kubeconfig to Vault KVv2
vault kv put secret/clusters/my-cluster kubeconfig=@kubeconfig.yaml

# Verify
vault kv get -field=kubeconfig secret/clusters/my-cluster
```

### Vault Drift Detection

For Vault sources, the provider tracks the KVv2 metadata version instead of content hashes. When you update the secret in Vault (creating a new version), the provider detects the version change and updates the Kubernetes Secret automatically.

---

## Verify

```shell
$ kubectl get remotecluster
NAME         READY   SYNCED   CLUSTER      VERSION        TYPE   AGE
my-cluster   True    True     my-cluster   v1.35.1+k3s1   k3s    5m

# Wide output shows NETWORK column
$ kubectl get remotecluster -o wide
NAME         READY   SYNCED   CLUSTER      VERSION        TYPE   NETWORK      AGE
my-cluster   True    True     my-cluster   v1.35.1+k3s1   k3s    10.31.102    5m
```

## Use the Kubeconfig

Extract the decrypted kubeconfig to your local machine:

```shell
kubectl get secret kubeconfig-my-cluster \
  -n crossplane-system -o jsonpath='{.data.kubeconfig}' | base64 -d > ~/.kube/my-cluster

kubectl --kubeconfig ~/.kube/my-cluster get nodes
```

## API Version Labels

The `apiVersions` field on `providerConfigs` controls which downstream ProviderConfig types are created:

| Label | API Group | Kind | Scope |
|-------|-----------|------|-------|
| `v1` | `*.crossplane.io` | `ProviderConfig` | Cluster |
| `v2` | `*.m.crossplane.io` | `ProviderConfig` | Namespaced |
| `v2-cluster` | `*.m.crossplane.io` | `ClusterProviderConfig` | Cluster |

Use `v2-cluster` for Crossplane v2+ setups.

## Cluster Type Detection

The provider auto-detects the Kubernetes distribution and writes it to `status.atProvider.clusterType`:

| Distribution | Detection Method |
|-------------|-----------------|
| `k3s` | Server version contains `+k3s` |
| `rke2` | Server version contains `+rke2` |
| `kind` | Node name ends with `-control-plane` or `-worker`, and providerID is empty or starts with `kind://` |
| `k8s` | Default fallback |

## Encrypting a Kubeconfig with SOPS/age

```shell
# Generate an age key pair
age-keygen -o age.key
# Public key: age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx

# Encrypt the kubeconfig
sops encrypt --age age1xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx \
  kubeconfig.yaml > kubeconfig.enc.yaml

# Store the secret key in Kubernetes (key field must be named "key")
kubectl create secret generic age-key \
  --namespace crossplane-system \
  --from-literal=key="$(cat age.key | grep AGE-SECRET-KEY)"
```

## RBAC

The provider bootstraps its own RBAC on startup to manage downstream ProviderConfigs. It creates:

- **ClusterRole** `provider-kubeconfig-downstream` — permissions for `providerconfigs` and `clusterproviderconfigs` in `kubernetes.m.crossplane.io`, `helm.m.crossplane.io`, and the legacy `*.crossplane.io` APIs
- **ClusterRoleBinding** `provider-kubeconfig-downstream` — binds to the provider's service account (auto-detected from the pod)

This is automatic — no manual RBAC setup required. On provider upgrades (new pod/SA name), the bootstrap appends the new SA to the binding.

## Git Cache

Git sources are cloned into a per-repo cache directory. The cache root is created with `0700` permissions so cached repo contents (kubeconfigs, `.git`) are not readable by other users sharing the pod. Least-recently-used directories are evicted once the entry count exceeds a cap, bounding disk usage on long-running pods.

| Env var | Default | Description |
|---------|---------|-------------|
| `PROVIDER_KUBECONFIG_CACHE_DIR` | `$XDG_CACHE_HOME/provider-kubeconfig` (else `$TMPDIR/provider-kubeconfig`) | Cache root. Point at a dedicated writable volume (e.g. an `emptyDir`) to keep clones off shared `/tmp`. |
| `PROVIDER_KUBECONFIG_CACHE_MAX_ENTRIES` | `32` | Max cached repo directories retained before LRU eviction. |

## Building

### Prerequisites

- Go 1.23+
- Docker
- Make

### Build the Provider

```shell
# Initialize the build submodule (first time only)
make submodules

# Generate CRDs, deepcopy, and run linters
make reviewable

# Build the provider binary and Docker image
make build
```

### Local Development

```shell
# Create a kind cluster, install CRDs, and start the provider
make dev

# Clean up
make dev-clean
```

### Running Tests

```shell
go test ./internal/... -v -count=1
```

## Project Structure

```
apis/
  v1alpha1/                  # ProviderConfig, ClusterProviderConfig and usage types
  kubeconfig/
    v1alpha1/                # RemoteCluster managed resource type
internal/
  cluster/                   # Remote cluster info gathering (version, type, nodes, CIDRs)
  controller/
    config/                  # ProviderConfig controller
    remotecluster/           # RemoteCluster reconciler
    kubeconfig.go            # Controller registration (SetupGated)
  decrypt/                   # SOPS/age decryption
  git/                       # Git clone/pull with caching and stale recovery
  rbac/                      # RBAC self-bootstrap for downstream access
  vault/                     # Vault KVv2 client with Kubernetes/AppRole auth
package/
  crds/                      # Generated CRDs
  crossplane.yaml            # Crossplane package metadata
```

## Provider Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` / `-d` | `false` | Enable debug logging (shows V(1) verbose logs) |
| `--leader-election` / `-l` | `false` | Enable leader election for HA |
| `--poll` | `1m` | How often to check each resource for drift |
| `--sync` / `-s` | `1h` | Controller manager sync period |
| `--max-reconcile-rate` | `10` | Max reconciliations per second |

## Links

- [Crossplane Provider Development Guide](https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md)
- [SOPS](https://github.com/getsops/sops)
- [age encryption](https://age-encryption.org/)
- [HashiCorp Vault](https://www.vaultproject.io/)
