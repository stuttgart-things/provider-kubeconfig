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
package v1alpha1

import (
	"reflect"

	xpv1 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RemoteClusterSource defines where to find the kubeconfig.
type RemoteClusterSource struct {
	// Type selects the kubeconfig source. Defaults to "git".
	// +kubebuilder:default=git
	// +kubebuilder:validation:Enum=git;vault
	Type string `json:"type,omitempty"`
	// Path is the file path in Git, or the Vault KVv2 secret path.
	Path string `json:"path"`
	// Key is the key within the Vault KVv2 secret data that holds the kubeconfig.
	// Only used when type is "vault". Defaults to "kubeconfig".
	// +kubebuilder:default=kubeconfig
	// +optional
	Key string `json:"key,omitempty"`
}

// ProviderConfigRef defines a downstream ProviderConfig to create.
type ProviderConfigRef struct {
	// Name of the ProviderConfig to create.
	Name string `json:"name"`
	// Type of the downstream provider. e.g. provider-kubernetes, provider-helm, argocd-cluster-secret
	// +kubebuilder:validation:Enum=provider-kubernetes;provider-helm;argocd-cluster-secret
	Type string `json:"type"`
	// APIVersions specifies which downstream ProviderConfig API versions to create.
	// "v1" creates ProviderConfig on *.crossplane.io (cluster-scoped).
	// "v2" creates ProviderConfig on *.m.crossplane.io (namespaced).
	// "v2-cluster" creates ClusterProviderConfig on *.m.crossplane.io (cluster-scoped).
	// Defaults to ["v1"] if omitted for backwards compatibility.
	// Not used for argocd-cluster-secret type.
	// +optional
	// +kubebuilder:default={"v1"}
	APIVersions []string `json:"apiVersions,omitempty"`
	// Namespace is the target namespace for the created resource.
	// Used by argocd-cluster-secret to specify the ArgoCD namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// RemoteClusterParameters are the configurable fields of a RemoteCluster.
type RemoteClusterParameters struct {
	// Source defines the path to the encrypted kubeconfig in the Git repo.
	Source RemoteClusterSource `json:"source"`

	// SecretNamespace is the namespace where the decrypted kubeconfig Secret will be created.
	// +kubebuilder:default=crossplane-system
	SecretNamespace string `json:"secretNamespace,omitempty"`

	// ProviderConfigs defines downstream ProviderConfigs to create from this kubeconfig.
	// +optional
	ProviderConfigs []ProviderConfigRef `json:"providerConfigs,omitempty"`
}

// RemoteClusterObservation are the observable fields of a RemoteCluster.
type RemoteClusterObservation struct {
	// ClusterName is the name of the remote cluster.
	ClusterName string `json:"clusterName,omitempty"`
	// ServerVersion is the Kubernetes version of the remote cluster.
	ServerVersion string `json:"serverVersion,omitempty"`
	// APIEndpoint is the API server endpoint of the remote cluster.
	APIEndpoint string `json:"apiEndpoint,omitempty"`
	// PodCIDR is the pod network CIDR of the remote cluster.
	PodCIDR string `json:"podCIDR,omitempty"`
	// ServiceCIDR is the service network CIDR of the remote cluster.
	ServiceCIDR string `json:"serviceCIDR,omitempty"`
	// NodeCIDRs contains the CIDRs assigned to each node.
	NodeCIDRs []string `json:"nodeCIDRs,omitempty"`
	// NodeCount is the number of nodes in the remote cluster.
	NodeCount int `json:"nodeCount,omitempty"`
	// InternalNetworkKey is the first 3 octets of the node InternalIP network
	// (e.g. "10.31.102" or "172.18.0"), used as network key for IP reservations.
	InternalNetworkKey string `json:"internalNetworkKey,omitempty"`
	// ClusterType is the detected Kubernetes distribution (kind, k3s, rke2, k8s).
	ClusterType string `json:"clusterType,omitempty"`
	// KindClusterName is the actual kind cluster name extracted from node names
	// (e.g. "dev" from "dev-control-plane"). Only set for kind clusters.
	// +optional
	KindClusterName string `json:"kindClusterName,omitempty"`
	// VaultSecretVersion is the KVv2 metadata version, used for drift detection.
	// +optional
	VaultSecretVersion int `json:"vaultSecretVersion,omitempty"`
	// SecretRef is the name of the Secret containing the decrypted kubeconfig.
	SecretRef string `json:"secretRef,omitempty"`
}

// A RemoteClusterSpec defines the desired state of a RemoteCluster.
type RemoteClusterSpec struct {
	xpv1.ManagedResourceSpec `json:",inline"`
	ForProvider              RemoteClusterParameters `json:"forProvider"`
}

// A RemoteClusterStatus represents the observed state of a RemoteCluster.
type RemoteClusterStatus struct {
	xpv1.ManagedResourceStatus `json:",inline"`
	AtProvider                 RemoteClusterObservation `json:"atProvider,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="CLUSTER",type="string",JSONPath=".status.atProvider.clusterName"
// +kubebuilder:printcolumn:name="VERSION",type="string",JSONPath=".status.atProvider.serverVersion"
// +kubebuilder:printcolumn:name="TYPE",type="string",JSONPath=".status.atProvider.clusterType"
// +kubebuilder:printcolumn:name="NETWORK",type="string",JSONPath=".status.atProvider.internalNetworkKey",priority=1
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,kubeconfig}
// A RemoteCluster reads a SOPS-encrypted kubeconfig from Git, decrypts it,
// and bootstraps Secrets and ProviderConfigs for the remote cluster.
type RemoteCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RemoteClusterSpec   `json:"spec"`
	Status            RemoteClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// RemoteClusterList contains a list of RemoteCluster.
type RemoteClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemoteCluster `json:"items"`
}

// RemoteCluster type metadata.
var (
	RemoteClusterKind             = reflect.TypeOf(RemoteCluster{}).Name()
	RemoteClusterGroupKind        = schema.GroupKind{Group: Group, Kind: RemoteClusterKind}.String()
	RemoteClusterKindAPIVersion   = RemoteClusterKind + "." + SchemeGroupVersion.String()
	RemoteClusterGroupVersionKind = SchemeGroupVersion.WithKind(RemoteClusterKind)
)

func init() {
	SchemeBuilder.Register(&RemoteCluster{}, &RemoteClusterList{})
}
