# Testing

## Kind Cluster Setup

### 1. Create a Kind cluster

```bash
cat <<'EOF' > /tmp/crossplane-test.yaml
kind: Cluster
name: crossplane-test
apiVersion: kind.x-k8s.io/v1alpha4
featureGates:
  ImageVolume: True
networking:
  apiServerAddress: '10.100.136.192'
  disableDefaultCNI: True
  kubeProxyMode: none
nodes:
  - role: control-plane
    image: kindest/node:v1.35.0
    extraPortMappings:
      - containerPort: 6443
        hostPort: 34360
        protocol: TCP
  - role: worker
    image: kindest/node:v1.35.0
EOF

kind create cluster --config /tmp/crossplane-test.yaml
kind get kubeconfig --name crossplane-test > ~/.kube/dev
yq -i '.clusters[0].cluster.server |= sub("0\.0\.0\.0", "10.100.136.192")' ~/.kube/dev
```

### 2. Encrypt kubeconfig with SOPS/age

```bash
# Get the age public key from the secret key
AGE_PUB=$(echo "AGE-SECRET-KEY-..." | age-keygen -y)

# Encrypt the kubeconfig
sops encrypt --age $AGE_PUB ~/.kube/dev > testdata/xplane-test.enc.yaml
```

### 3. Install Cilium (Crossplane will handle this via XCilium)

```bash
kubectl apply -k https://github.com/stuttgart-things/helm/infra/crds/cilium

dagger call -m github.com/stuttgart-things/dagger/helm@v0.57.0 \
  helmfile-operation \
  --helmfile-ref "git::https://github.com/stuttgart-things/helm.git@infra/cilium.yaml.gotmpl" \
  --operation apply \
  --state-values "config=kind,clusterName=dev,configureLB=false" \
  --kube-config file:///home/sthings/.kube/dev \
  --progress plain -vv
```

### 3. Install Crossplane

```bash
dagger call -m github.com/stuttgart-things/dagger/helm@v0.57.0 \
  helmfile-operation \
  --helmfile-ref "git::https://github.com/stuttgart-things/helm.git@cicd/crossplane.yaml.gotmpl" \
  --operation apply \
  --state-values "version=2.2.0" \
  --kube-config file:///home/sthings/.kube/dev \
  --progress plain -vv
```

### 4. Install CRDs and run the provider

```bash
kubectl apply -R -f package/crds
go run cmd/provider/main.go --debug
```

## End-to-End Test with Released Provider Package

After the cluster and Crossplane are ready, install the released provider xpkg and test with the included testdata.

### 1. Install the provider

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-kubeconfig
spec:
  package: ghcr.io/stuttgart-things/provider-kubeconfig-xpkg:v0.2.1
```

```bash
kubectl get providers provider-kubeconfig
# Wait until INSTALLED=True and HEALTHY=True
```

### 2. Create the ClusterProviderConfig

```bash
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: age-key
  namespace: crossplane-system
stringData:
  key: ""
---
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: ClusterProviderConfig
metadata:
  name: default
spec:
  git:
    url: https://github.com/stuttgart-things/provider-kubeconfig.git
    branch: main
  decryption:
    provider: sops
    secretRef:
      name: age-key
      namespace: crossplane-system
EOF
```

### 3. Create a RemoteCluster using testdata

The repo includes test kubeconfigs in `testdata/`:

| File | Description |
|------|-------------|
| `kubeconfig.yaml` | Unencrypted kubeconfig (works with empty age key) |
| `kubeconfig.enc.yaml` | SOPS-encrypted kubeconfig (requires matching age secret key) |

```bash
kubectl apply -f - <<'EOF'
apiVersion: kubeconfig.stuttgart-things.com/v1alpha1
kind: RemoteCluster
metadata:
  name: test-cluster
spec:
  forProvider:
    source:
      path: testdata/kubeconfig.yaml
    secretNamespace: crossplane-system
  providerConfigRef:
    name: default
    kind: ClusterProviderConfig
EOF
```

### 4. Verify

```bash
# RemoteCluster should be Ready + Synced
kubectl get remotecluster test-cluster

# Kubeconfig Secret should exist
kubectl get secret kubeconfig-test-cluster -n crossplane-system

# Inspect the decrypted kubeconfig
kubectl get secret kubeconfig-test-cluster -n crossplane-system \
  -o jsonpath='{.data.kubeconfig}' | base64 -d
```

## Unit Tests

```bash
go test ./internal/... -v -count=1
```

## Lint

```bash
golangci-lint run ./...
```
