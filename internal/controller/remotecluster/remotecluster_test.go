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
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource/fake"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	v1alpha1 "github.com/stuttgart-things/provider-kubeconfig/apis/kubeconfig/v1alpha1"
)

// testKubeconfig is a minimal valid kubeconfig with a bearer token for ArgoCD tests.
var testKubeconfig = []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://10.31.101.8:6443
    certificate-authority-data: dGVzdC1jYS1kYXRh
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: admin
  name: test-cluster
current-context: test-cluster
users:
- name: admin
  user:
    token: test-bearer-token
`)

// --- helpers ---

func newRemoteCluster(name, ns, path string) *v1alpha1.RemoteCluster {
	return &v1alpha1.RemoteCluster{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.RemoteClusterSpec{
			ForProvider: v1alpha1.RemoteClusterParameters{
				Source:          v1alpha1.RemoteClusterSource{Path: path},
				SecretNamespace: ns,
			},
		},
	}
}

func newRemoteClusterWithProviderConfigs(name, ns, path string, pcs []v1alpha1.ProviderConfigRef) *v1alpha1.RemoteCluster {
	cr := newRemoteCluster(name, ns, path)
	cr.Spec.ForProvider.ProviderConfigs = pcs
	return cr
}

// --- mock client ---

type mockClient struct {
	client.Client
	MockGet    func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error
	MockCreate func(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
	MockUpdate func(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
	MockDelete func(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
	MockList   func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

func (m *mockClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if m.MockGet != nil {
		return m.MockGet(ctx, key, obj, opts...)
	}
	return nil
}

func (m *mockClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if m.MockCreate != nil {
		return m.MockCreate(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	if m.MockUpdate != nil {
		return m.MockUpdate(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	if m.MockDelete != nil {
		return m.MockDelete(ctx, obj, opts...)
	}
	return nil
}

func (m *mockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if m.MockList != nil {
		return m.MockList(ctx, list, opts...)
	}
	return nil
}

// --- unit tests for helpers ---

func TestSecretName(t *testing.T) {
	if got := secretName("dev-cluster"); got != "kubeconfig-dev-cluster" {
		t.Errorf("secretName: want %q, got %q", "kubeconfig-dev-cluster", got)
	}
}

func TestContentHash(t *testing.T) {
	data := []byte("hello world")
	h1 := contentHash(data)
	h2 := contentHash(data)
	if h1 != h2 {
		t.Error("same data should produce same hash")
	}
	h3 := contentHash([]byte("different"))
	if h1 == h3 {
		t.Error("different data should produce different hash")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex should be 64 chars, got %d", len(h1))
	}
}

func TestBuildDownstreamProviderConfig(t *testing.T) {
	cases := map[string]struct {
		meta          providerConfigMeta
		pcName        string
		wantGVK       schema.GroupVersionKind
		wantNamespace string
	}{
		"ProviderKubernetesV1": {
			meta:    providerConfigGVKs["provider-kubernetes"]["v1"],
			pcName:  "my-k8s",
			wantGVK: schema.GroupVersionKind{Group: "kubernetes.crossplane.io", Version: "v1alpha1", Kind: "ProviderConfig"},
		},
		"ProviderHelmV1": {
			meta:    providerConfigGVKs["provider-helm"]["v1"],
			pcName:  "my-helm",
			wantGVK: schema.GroupVersionKind{Group: "helm.crossplane.io", Version: "v1beta1", Kind: "ProviderConfig"},
		},
		"ProviderKubernetesV2": {
			meta:          providerConfigGVKs["provider-kubernetes"]["v2"],
			pcName:        "my-k8s",
			wantGVK:       schema.GroupVersionKind{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Kind: "ProviderConfig"},
			wantNamespace: defaultSecretNamespace,
		},
		"ProviderHelmV2": {
			meta:          providerConfigGVKs["provider-helm"]["v2"],
			pcName:        "my-helm",
			wantGVK:       schema.GroupVersionKind{Group: "helm.m.crossplane.io", Version: "v1beta1", Kind: "ProviderConfig"},
			wantNamespace: defaultSecretNamespace,
		},
		"ProviderKubernetesV2Cluster": {
			meta:    providerConfigGVKs["provider-kubernetes"]["v2-cluster"],
			pcName:  "my-k8s",
			wantGVK: schema.GroupVersionKind{Group: "kubernetes.m.crossplane.io", Version: "v1alpha1", Kind: "ClusterProviderConfig"},
		},
		"ProviderHelmV2Cluster": {
			meta:    providerConfigGVKs["provider-helm"]["v2-cluster"],
			pcName:  "my-helm",
			wantGVK: schema.GroupVersionKind{Group: "helm.m.crossplane.io", Version: "v1beta1", Kind: "ClusterProviderConfig"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u, err := buildDownstreamProviderConfig(tc.meta, tc.pcName, "kubeconfig-dev", defaultSecretNamespace, "dev")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantGVK, u.GroupVersionKind()); diff != "" {
				t.Errorf("GVK: -want, +got:\n%s", diff)
			}
			if u.GetName() != tc.pcName {
				t.Errorf("name: want %q, got %q", tc.pcName, u.GetName())
			}
			if u.GetNamespace() != tc.wantNamespace {
				t.Errorf("namespace: want %q, got %q", tc.wantNamespace, u.GetNamespace())
			}
			if u.GetLabels()[labelRemoteCluster] != "dev" {
				t.Errorf("label %s: want %q, got %q", labelRemoteCluster, "dev", u.GetLabels()[labelRemoteCluster])
			}

			creds, _, _ := unstructured.NestedMap(u.Object, "spec", "credentials")
			if creds["source"] != "Secret" {
				t.Errorf("credentials.source: want %q, got %v", "Secret", creds["source"])
			}
			secretRef, _ := creds["secretRef"].(map[string]interface{})
			if secretRef["name"] != "kubeconfig-dev" {
				t.Errorf("secretRef.name: want %q, got %v", "kubeconfig-dev", secretRef["name"])
			}
			if secretRef["namespace"] != defaultSecretNamespace {
				t.Errorf("secretRef.namespace: want %q, got %v", defaultSecretNamespace, secretRef["namespace"])
			}
			if secretRef["key"] != "kubeconfig" {
				t.Errorf("secretRef.key: want %q, got %v", "kubeconfig", secretRef["key"])
			}
		})
	}
}

func TestLookupProviderConfigMeta(t *testing.T) {
	cases := map[string]struct {
		providerType string
		apiVer       string
		wantErr      bool
	}{
		"ValidV1":           {providerType: "provider-kubernetes", apiVer: "v1"},
		"ValidV2":           {providerType: "provider-helm", apiVer: "v2"},
		"ValidV2Cluster":    {providerType: "provider-kubernetes", apiVer: "v2-cluster"},
		"UnsupportedType":   {providerType: "provider-unknown", apiVer: "v1", wantErr: true},
		"UnsupportedAPIVer": {providerType: "provider-kubernetes", apiVer: "v3", wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := lookupProviderConfigMeta(tc.providerType, tc.apiVer)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveAPIVersions(t *testing.T) {
	cases := map[string]struct {
		pcRef v1alpha1.ProviderConfigRef
		want  []string
	}{
		"DefaultsToV1": {
			pcRef: v1alpha1.ProviderConfigRef{Name: "x", Type: "provider-kubernetes"},
			want:  []string{"v1"},
		},
		"ExplicitV2": {
			pcRef: v1alpha1.ProviderConfigRef{Name: "x", Type: "provider-kubernetes", APIVersions: []string{"v2"}},
			want:  []string{"v2"},
		},
		"BothVersions": {
			pcRef: v1alpha1.ProviderConfigRef{Name: "x", Type: "provider-kubernetes", APIVersions: []string{"v1", "v2"}},
			want:  []string{"v1", "v2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveAPIVersions(tc.pcRef)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("-want, +got:\n%s", diff)
			}
		})
	}
}

// --- Observe tests ---

func TestObserveNotRemoteCluster(t *testing.T) {
	e := &external{kube: &mockClient{}}
	_, err := e.Observe(context.Background(), &fake.Managed{})
	if diff := cmp.Diff(errors.New(errNotRemoteCluster), err, test.EquateErrors()); diff != "" {
		t.Errorf("-want error, +got error:\n%s", diff)
	}
}

func TestObserveSecretNotFound(t *testing.T) {
	mc := &mockClient{
		MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
			return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
		},
	}

	cr := newRemoteCluster("dev", "default", "clusters/dev.yaml")
	e := &external{kube: mc}
	got, err := e.Observe(context.Background(), cr)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ResourceExists {
		t.Error("expected ResourceExists=false")
	}
}

func TestObserveDefaultNamespace(t *testing.T) {
	var capturedKey types.NamespacedName
	mc := &mockClient{
		MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
			capturedKey = key
			return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
		},
	}

	cr := newRemoteCluster("dev", "", "clusters/dev.yaml")
	e := &external{kube: mc}
	_, _ = e.Observe(context.Background(), cr)

	if capturedKey.Namespace != defaultSecretNamespace {
		t.Errorf("expected default namespace %q, got %q", defaultSecretNamespace, capturedKey.Namespace)
	}
}

// --- Delete tests ---

func TestDeleteNotRemoteCluster(t *testing.T) {
	e := &external{kube: &mockClient{}}
	_, err := e.Delete(context.Background(), &fake.Managed{})
	if diff := cmp.Diff(errors.New(errNotRemoteCluster), err, test.EquateErrors()); diff != "" {
		t.Errorf("-want error, +got error:\n%s", diff)
	}
}

func TestDeleteSuccess(t *testing.T) {
	mc := &mockClient{
		MockDelete: func(_ context.Context, _ client.Object, _ ...client.DeleteOption) error {
			return nil
		},
		MockList: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
			return nil
		},
	}

	cr := newRemoteCluster("dev", "default", "clusters/dev.yaml")
	e := &external{kube: mc}
	_, err := e.Delete(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteSecretAlreadyGone(t *testing.T) {
	mc := &mockClient{
		MockDelete: func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
			return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, obj.GetName())
		},
		MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
			return nil
		},
	}

	cr := newRemoteCluster("dev", "default", "clusters/dev.yaml")
	e := &external{kube: mc}
	_, err := e.Delete(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeleteCleansUpDownstreamProviderConfigs(t *testing.T) {
	var deletedNames []string
	mc := &mockClient{
		MockDelete: func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
			deletedNames = append(deletedNames, obj.GetName())
			return nil
		},
		MockList: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
			if l, ok := list.(*unstructured.UnstructuredList); ok {
				item := unstructured.Unstructured{}
				item.SetName("stale-pc")
				l.Items = []unstructured.Unstructured{item}
			}
			return nil
		},
	}

	cr := newRemoteCluster("dev", "default", "clusters/dev.yaml")
	e := &external{kube: mc}
	_, err := e.Delete(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have deleted the Secret + stale ProviderConfigs from each GVK type
	found := false
	for _, n := range deletedNames {
		if n == "stale-pc" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected stale-pc to be deleted, deleted: %v", deletedNames)
	}
}

// --- Update tests ---

func TestUpdateNotRemoteCluster(t *testing.T) {
	e := &external{kube: &mockClient{}}
	_, err := e.Update(context.Background(), &fake.Managed{})
	if diff := cmp.Diff(errors.New(errNotRemoteCluster), err, test.EquateErrors()); diff != "" {
		t.Errorf("-want error, +got error:\n%s", diff)
	}
}

// --- Create tests ---

func TestCreateNotRemoteCluster(t *testing.T) {
	e := &external{kube: &mockClient{}}
	_, err := e.Create(context.Background(), &fake.Managed{})
	if diff := cmp.Diff(errors.New(errNotRemoteCluster), err, test.EquateErrors()); diff != "" {
		t.Errorf("-want error, +got error:\n%s", diff)
	}
}

// --- downstreamProviderConfigsUpToDate tests ---

func TestDownstreamProviderConfigsUpToDate(t *testing.T) {
	cases := map[string]struct {
		pcs    []v1alpha1.ProviderConfigRef
		getErr error
		want   bool
	}{
		"AllExistV1": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
			},
			getErr: nil,
			want:   true,
		},
		"AllExistV2": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v2"}},
			},
			getErr: nil,
			want:   true,
		},
		"AllExistBothVersions": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1", "v2"}},
			},
			getErr: nil,
			want:   true,
		},
		"DefaultsToV1": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "my-k8s", Type: "provider-kubernetes"},
			},
			getErr: nil,
			want:   true,
		},
		"MissingOne": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
			},
			getErr: kerrors.NewNotFound(schema.GroupResource{}, "my-k8s"),
			want:   false,
		},
		"UnsupportedType": {
			pcs: []v1alpha1.ProviderConfigRef{
				{Name: "bad", Type: "provider-unknown"},
			},
			want: false,
		},
		"NoneDesired": {
			pcs:  nil,
			want: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			mc := &mockClient{
				MockGet: func(_ context.Context, _ types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
					if tc.getErr != nil {
						return tc.getErr
					}
					return nil
				},
			}
			cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", tc.pcs)
			e := &external{kube: mc}
			got := e.downstreamProviderConfigsUpToDate(context.Background(), cr)
			if got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

// --- ensureDownstreamProviderConfigs tests ---

func TestEnsureDownstreamProviderConfigs(t *testing.T) {
	t.Run("CreatesMissingV1", func(t *testing.T) {
		var created []string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				return nil
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				created = append(created, obj.GetName())
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
			{Name: "my-helm", Type: "provider-helm", APIVersions: []string{"v1"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(created) != 2 {
			t.Errorf("expected 2 creates, got %d: %v", len(created), created)
		}
	})

	t.Run("CreatesBothV1AndV2", func(t *testing.T) {
		var createdGVKs []schema.GroupVersionKind
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				return nil
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				if u, ok := obj.(*unstructured.Unstructured); ok {
					createdGVKs = append(createdGVKs, u.GroupVersionKind())
				}
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1", "v2"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(createdGVKs) != 2 {
			t.Fatalf("expected 2 creates (v1+v2), got %d: %v", len(createdGVKs), createdGVKs)
		}
		groups := map[string]bool{}
		for _, gvk := range createdGVKs {
			groups[gvk.Group] = true
		}
		if !groups["kubernetes.crossplane.io"] {
			t.Error("expected v1 group kubernetes.crossplane.io")
		}
		if !groups["kubernetes.m.crossplane.io"] {
			t.Error("expected v2 group kubernetes.m.crossplane.io")
		}
	})

	t.Run("CreatesAllThreeVersions", func(t *testing.T) {
		var createdGVKs []schema.GroupVersionKind
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				return nil
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				if u, ok := obj.(*unstructured.Unstructured); ok {
					createdGVKs = append(createdGVKs, u.GroupVersionKind())
				}
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1", "v2", "v2-cluster"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(createdGVKs) != 3 {
			t.Fatalf("expected 3 creates (v1+v2+v2-cluster), got %d: %v", len(createdGVKs), createdGVKs)
		}
		kinds := map[string]bool{}
		for _, gvk := range createdGVKs {
			kinds[gvk.Group+"/"+gvk.Kind] = true
		}
		if !kinds["kubernetes.crossplane.io/ProviderConfig"] {
			t.Error("expected v1 ProviderConfig")
		}
		if !kinds["kubernetes.m.crossplane.io/ProviderConfig"] {
			t.Error("expected v2 ProviderConfig")
		}
		if !kinds["kubernetes.m.crossplane.io/ClusterProviderConfig"] {
			t.Error("expected v2 ClusterProviderConfig")
		}
	})

	t.Run("V2SetsNamespace", func(t *testing.T) {
		var createdNS string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				return nil
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				createdNS = obj.GetNamespace()
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v2"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if createdNS != defaultSecretNamespace {
			t.Errorf("v2 namespace: want %q, got %q", defaultSecretNamespace, createdNS)
		}
	})

	t.Run("DefaultsToV1", func(t *testing.T) {
		var createdGVKs []schema.GroupVersionKind
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				if _, ok := obj.(*unstructured.Unstructured); ok {
					return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
				}
				return nil
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				if u, ok := obj.(*unstructured.Unstructured); ok {
					createdGVKs = append(createdGVKs, u.GroupVersionKind())
				}
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		// No APIVersions set — should default to v1
		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes"},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(createdGVKs) != 1 {
			t.Fatalf("expected 1 create, got %d", len(createdGVKs))
		}
		if createdGVKs[0].Group != "kubernetes.crossplane.io" {
			t.Errorf("expected v1 group, got %q", createdGVKs[0].Group)
		}
	})

	t.Run("SkipsExisting", func(t *testing.T) {
		var created []string
		mc := &mockClient{
			MockGet: func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return nil // already exists
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				created = append(created, obj.GetName())
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
			MockUpdate: func(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(created) != 0 {
			t.Errorf("expected 0 creates for existing, got %d: %v", len(created), created)
		}
	})

	t.Run("DeletesStale", func(t *testing.T) {
		var deleted []string
		mc := &mockClient{
			MockGet: func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return nil
			},
			MockList: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				if l, ok := list.(*unstructured.UnstructuredList); ok {
					stale := unstructured.Unstructured{}
					stale.SetName("stale-old-pc")
					l.Items = []unstructured.Unstructured{stale}
				}
				return nil
			},
			MockDelete: func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
				deleted = append(deleted, obj.GetName())
				return nil
			},
			MockUpdate: func(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		found := false
		for _, n := range deleted {
			if n == "stale-old-pc" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected stale-old-pc to be deleted, deleted: %v", deleted)
		}
	})

	t.Run("CreatesArgoCDClusterSecret", func(t *testing.T) {
		var createdSecrets []string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				createdSecrets = append(createdSecrets, obj.GetName())
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		found := false
		for _, n := range createdSecrets {
			if n == "argocd-dev" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected argocd-dev secret to be created, created: %v", createdSecrets)
		}
	})

	t.Run("MixedProviderConfigsAndArgoCD", func(t *testing.T) {
		var createdNames []string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return kerrors.NewNotFound(schema.GroupResource{}, key.Name)
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				createdNames = append(createdNames, obj.GetName())
				return nil
			},
			MockList: func(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "my-k8s", Type: "provider-kubernetes", APIVersions: []string{"v1"}},
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		err := e.ensureDownstreamProviderConfigs(context.Background(), cr, "kubeconfig-dev", defaultSecretNamespace, testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		nameSet := map[string]bool{}
		for _, n := range createdNames {
			nameSet[n] = true
		}
		if !nameSet["my-k8s"] {
			t.Error("expected my-k8s ProviderConfig to be created")
		}
		if !nameSet["argocd-dev"] {
			t.Error("expected argocd-dev ArgoCD secret to be created")
		}
	})
}

// --- ObserveSetsAvailableCondition (requires mock that doesn't trigger cloneAndReadFile) ---
// Note: Full Observe integration with git is tested via e2e. Here we test the
// condition-setting path by checking the Observe path where Secret is not found
// (which returns early before cloneAndReadFile).

func TestObserveSetsAvailableConditionRequiresSecret(t *testing.T) {
	// When Secret is not found, Available should NOT be set
	mc := &mockClient{
		MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
			return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
		},
	}

	cr := newRemoteCluster("dev", "default", "clusters/dev.yaml")
	e := &external{kube: mc}
	obs, err := e.Observe(context.Background(), cr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.ResourceExists {
		t.Error("expected ResourceExists=false")
	}
	// Ready condition should not be set when resource doesn't exist
	cond := cr.GetCondition(xpv2.TypeReady)
	if cond.Status == corev1.ConditionTrue {
		t.Error("Ready should not be True when Secret doesn't exist")
	}
}

// --- buildArgoCDClusterSecret tests ---

func TestBuildArgoCDClusterSecret(t *testing.T) {
	t.Run("ValidKubeconfig", func(t *testing.T) {
		secret, err := buildArgoCDClusterSecret("argocd-dev", "argocd", "dev-cluster", testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if secret.Name != "argocd-dev" {
			t.Errorf("name: want %q, got %q", "argocd-dev", secret.Name)
		}
		if secret.Namespace != "argocd" {
			t.Errorf("namespace: want %q, got %q", "argocd", secret.Namespace)
		}
		if secret.Labels[labelArgoCDSecretType] != "cluster" {
			t.Errorf("label %s: want %q, got %q", labelArgoCDSecretType, "cluster", secret.Labels[labelArgoCDSecretType])
		}
		if secret.Labels[labelManagedBy] != "provider-kubeconfig" {
			t.Errorf("label %s: want %q, got %q", labelManagedBy, "provider-kubeconfig", secret.Labels[labelManagedBy])
		}
		if secret.Labels[labelRemoteCluster] != "dev-cluster" {
			t.Errorf("label %s: want %q, got %q", labelRemoteCluster, "dev-cluster", secret.Labels[labelRemoteCluster])
		}
		if secret.StringData["name"] != "dev-cluster" {
			t.Errorf("stringData.name: want %q, got %q", "dev-cluster", secret.StringData["name"])
		}
		if secret.StringData["server"] != "https://10.31.101.8:6443" {
			t.Errorf("stringData.server: want %q, got %q", "https://10.31.101.8:6443", secret.StringData["server"])
		}

		// Parse the config JSON and verify bearer token
		var cfg argoCDClusterConfig
		if err := json.Unmarshal([]byte(secret.StringData["config"]), &cfg); err != nil {
			t.Fatalf("cannot parse config JSON: %v", err)
		}
		if cfg.BearerToken != "test-bearer-token" {
			t.Errorf("bearerToken: want %q, got %q", "test-bearer-token", cfg.BearerToken)
		}
		if cfg.TLSClientConfig.CAData == "" {
			t.Error("expected caData to be populated")
		}
	})

	t.Run("InvalidKubeconfig", func(t *testing.T) {
		_, err := buildArgoCDClusterSecret("argocd-dev", "argocd", "dev", []byte("not-a-kubeconfig"))
		if err == nil {
			t.Fatal("expected error for invalid kubeconfig")
		}
	})

	t.Run("InsecureSkipsTLSVerify", func(t *testing.T) {
		kc := []byte(`apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://10.0.0.1:6443
    insecure-skip-tls-verify: true
  name: insecure-cluster
contexts:
- context:
    cluster: insecure-cluster
    user: admin
  name: insecure-cluster
current-context: insecure-cluster
users:
- name: admin
  user:
    token: my-token
`)
		secret, err := buildArgoCDClusterSecret("argocd-insecure", "argocd", "insecure", kc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var cfg argoCDClusterConfig
		if err := json.Unmarshal([]byte(secret.StringData["config"]), &cfg); err != nil {
			t.Fatalf("cannot parse config JSON: %v", err)
		}
		if !cfg.TLSClientConfig.Insecure {
			t.Error("expected insecure=true")
		}
		if cfg.TLSClientConfig.CAData != "" {
			t.Error("expected empty caData when insecure")
		}
	})
}

// --- ensureArgoCDClusterSecret tests ---

func TestEnsureArgoCDClusterSecret(t *testing.T) {
	t.Run("CreatesWhenNotFound", func(t *testing.T) {
		var createdName string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			},
			MockCreate: func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
				createdName = obj.GetName()
				return nil
			},
		}

		pc := v1alpha1.ProviderConfigRef{Name: "argocd-dev", Type: "argocd-cluster-secret"}
		e := &external{kube: mc}
		err := e.ensureArgoCDClusterSecret(context.Background(), pc, "dev-cluster", testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if createdName != "argocd-dev" {
			t.Errorf("expected create for %q, got %q", "argocd-dev", createdName)
		}
	})

	t.Run("UpdatesWhenExists", func(t *testing.T) {
		var updated bool
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, obj client.Object, _ ...client.GetOption) error {
				s := obj.(*corev1.Secret)
				s.Name = key.Name
				s.Namespace = key.Namespace
				return nil
			},
			MockUpdate: func(_ context.Context, _ client.Object, _ ...client.UpdateOption) error {
				updated = true
				return nil
			},
		}

		pc := v1alpha1.ProviderConfigRef{Name: "argocd-dev", Type: "argocd-cluster-secret", Namespace: "argocd"}
		e := &external{kube: mc}
		err := e.ensureArgoCDClusterSecret(context.Background(), pc, "dev-cluster", testKubeconfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !updated {
			t.Error("expected update for existing secret")
		}
	})

	t.Run("DefaultsToArgoCDNamespace", func(t *testing.T) {
		var gotNamespace string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				gotNamespace = key.Namespace
				return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			},
			MockCreate: func(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
				return nil
			},
		}

		pc := v1alpha1.ProviderConfigRef{Name: "argocd-dev", Type: "argocd-cluster-secret"}
		e := &external{kube: mc}
		_ = e.ensureArgoCDClusterSecret(context.Background(), pc, "dev-cluster", testKubeconfig)

		if gotNamespace != defaultArgoCDNamespace {
			t.Errorf("expected namespace %q, got %q", defaultArgoCDNamespace, gotNamespace)
		}
	})

	t.Run("UsesCustomNamespace", func(t *testing.T) {
		var gotNamespace string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				gotNamespace = key.Namespace
				return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			},
			MockCreate: func(_ context.Context, _ client.Object, _ ...client.CreateOption) error {
				return nil
			},
		}

		pc := v1alpha1.ProviderConfigRef{Name: "argocd-dev", Type: "argocd-cluster-secret", Namespace: "custom-argocd"}
		e := &external{kube: mc}
		_ = e.ensureArgoCDClusterSecret(context.Background(), pc, "dev-cluster", testKubeconfig)

		if gotNamespace != "custom-argocd" {
			t.Errorf("expected namespace %q, got %q", "custom-argocd", gotNamespace)
		}
	})
}

// --- deleteStaleArgoCDSecrets tests ---

func TestDeleteStaleArgoCDSecrets(t *testing.T) {
	t.Run("DeletesOrphaned", func(t *testing.T) {
		var deleted []string
		mc := &mockClient{
			MockList: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				sl := list.(*corev1.SecretList)
				sl.Items = []corev1.Secret{
					{ObjectMeta: metav1.ObjectMeta{Name: "orphaned-argocd"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "still-desired"}},
				}
				return nil
			},
			MockDelete: func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
				deleted = append(deleted, obj.GetName())
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "still-desired", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		err := e.deleteStaleArgoCDSecrets(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(deleted) != 1 || deleted[0] != "orphaned-argocd" {
			t.Errorf("expected only orphaned-argocd deleted, got: %v", deleted)
		}
	})

	t.Run("NoDeleteWhenAllDesired", func(t *testing.T) {
		var deleted []string
		mc := &mockClient{
			MockList: func(_ context.Context, list client.ObjectList, _ ...client.ListOption) error {
				sl := list.(*corev1.SecretList)
				sl.Items = []corev1.Secret{
					{ObjectMeta: metav1.ObjectMeta{Name: "argocd-dev"}},
				}
				return nil
			},
			MockDelete: func(_ context.Context, obj client.Object, _ ...client.DeleteOption) error {
				deleted = append(deleted, obj.GetName())
				return nil
			},
		}

		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		err := e.deleteStaleArgoCDSecrets(context.Background(), cr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(deleted) != 0 {
			t.Errorf("expected no deletes, got: %v", deleted)
		}
	})
}

// --- downstreamProviderConfigsUpToDate ArgoCD tests ---

func TestDownstreamProviderConfigsUpToDateArgoCD(t *testing.T) {
	t.Run("ArgoCDSecretExists", func(t *testing.T) {
		mc := &mockClient{
			MockGet: func(_ context.Context, _ types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return nil
			},
		}
		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		if !e.downstreamProviderConfigsUpToDate(context.Background(), cr) {
			t.Error("expected up-to-date when ArgoCD secret exists")
		}
	})

	t.Run("ArgoCDSecretMissing", func(t *testing.T) {
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				return kerrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, key.Name)
			},
		}
		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		if e.downstreamProviderConfigsUpToDate(context.Background(), cr) {
			t.Error("expected not up-to-date when ArgoCD secret is missing")
		}
	})

	t.Run("ArgoCDDefaultNamespace", func(t *testing.T) {
		var gotNamespace string
		mc := &mockClient{
			MockGet: func(_ context.Context, key types.NamespacedName, _ client.Object, _ ...client.GetOption) error {
				gotNamespace = key.Namespace
				return nil
			},
		}
		cr := newRemoteClusterWithProviderConfigs("dev", "default", "clusters/dev.yaml", []v1alpha1.ProviderConfigRef{
			{Name: "argocd-dev", Type: "argocd-cluster-secret"},
		})
		e := &external{kube: mc}
		e.downstreamProviderConfigsUpToDate(context.Background(), cr)

		if gotNamespace != defaultArgoCDNamespace {
			t.Errorf("expected namespace %q, got %q", defaultArgoCDNamespace, gotNamespace)
		}
	})
}

// Silence unused import warnings - these are used by mock methods
var _ = runtime.Object(nil)
