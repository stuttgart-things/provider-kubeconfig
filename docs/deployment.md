# Deployment

## Container Image

The provider image is built as a multi-stage Docker image (Go 1.24 builder + distroless runtime) and pushed to GHCR:

```
ghcr.io/stuttgart-things/provider-kubeconfig:<version>
ghcr.io/stuttgart-things/provider-kubeconfig:latest
```

Each [GitHub release](https://github.com/stuttgart-things/provider-kubeconfig/releases) publishes a semver-tagged image (e.g., `v0.2.1`).

## Crossplane xpkg

The Crossplane package (xpkg) embeds the runtime image, CRDs, and package metadata. It is pushed to:

```
ghcr.io/stuttgart-things/provider-kubeconfig-xpkg:<version>
ghcr.io/stuttgart-things/provider-kubeconfig-xpkg:latest
```

### Install via Crossplane

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubeconfig
spec:
  package: ghcr.io/stuttgart-things/provider-kubeconfig-xpkg:v0.2.1  # or :latest
```

### Verify

```bash
# Check the provider is installed and healthy
kubectl get providers provider-kubeconfig

# Check RemoteCluster resources
kubectl get remotecluster

# Check the created kubeconfig Secret
kubectl get secret -n crossplane-system -l app.kubernetes.io/managed-by=provider-kubeconfig
```

## Local Development

```bash
# Create a kind cluster, install CRDs, and start the provider
make dev

# Or manually
kubectl apply -R -f package/crds
go run cmd/provider/main.go --debug

# Clean up
make dev-clean
```

## Encrypting Kubeconfigs

```bash
# Generate an age key pair
age-keygen -o age.key

# Encrypt
sops encrypt --age age1xxx... kubeconfig.yaml > kubeconfig.enc.yaml

# Store the key in Kubernetes
kubectl create secret generic age-key \
  --namespace crossplane-system \
  --from-file=key=age.key
```

## Provider Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` / `-d` | `false` | Enable debug logging |
| `--leader-election` / `-l` | `false` | Enable leader election for HA |
| `--poll` | `1m` | How often to check each resource for drift |
| `--sync` / `-s` | `1h` | Controller manager sync period |
| `--max-reconcile-rate` | `10` | Max reconciliations per second |
