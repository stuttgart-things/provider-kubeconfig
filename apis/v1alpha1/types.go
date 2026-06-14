package v1alpha1

import (
	xpv1 "github.com/crossplane/crossplane/apis/v2/core/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretRef references a Kubernetes secret by name and namespace.
type SecretRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// GitConfig holds Git repository connection details.
type GitConfig struct {
	// URL of the Git repository.
	URL string `json:"url"`
	// Branch to use. Defaults to main.
	// +kubebuilder:default=main
	Branch string `json:"branch,omitempty"`
	// Revision pins the checkout to a specific commit SHA or tag. When set,
	// the branch tip is ignored and the kubeconfig is read from this exact
	// revision, making reconciles deterministic and enabling rollback by
	// changing the field rather than force-pushing the branch.
	// +optional
	Revision string `json:"revision,omitempty"`
	// SecretRef references a secret with Git credentials (token or SSH key).
	// +optional
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

// DecryptionConfig holds SOPS/age decryption settings.
type DecryptionConfig struct {
	// Provider is the decryption provider. Either sops or age.
	// +kubebuilder:validation:Enum=sops;age
	Provider string `json:"provider"`
	// SecretRef references a secret containing the decryption key.
	SecretRef SecretRef `json:"secretRef"`
}

// VaultKubernetesAuth holds Kubernetes auth method settings.
type VaultKubernetesAuth struct {
	// Role is the Vault role for Kubernetes auth.
	Role string `json:"role"`
	// MountPath is the Vault auth mount path.
	// +kubebuilder:default=kubernetes
	MountPath string `json:"mountPath,omitempty"`
}

// VaultAppRoleAuth holds AppRole auth method settings.
type VaultAppRoleAuth struct {
	// MountPath is the Vault auth mount path.
	// +kubebuilder:default=approle
	MountPath string `json:"mountPath,omitempty"`
	// RoleID is the AppRole role ID.
	RoleID string `json:"roleId"`
	// SecretRef references a Secret containing a "secret-id" key.
	SecretRef SecretRef `json:"secretRef"`
}

// VaultAuthConfig holds authentication configuration for Vault.
type VaultAuthConfig struct {
	// Method is the authentication method.
	// +kubebuilder:validation:Enum=kubernetes;approle
	Method string `json:"method"`
	// Kubernetes holds Kubernetes auth config.
	// +optional
	Kubernetes *VaultKubernetesAuth `json:"kubernetes,omitempty"`
	// AppRole holds AppRole auth config.
	// +optional
	AppRole *VaultAppRoleAuth `json:"appRole,omitempty"`
}

// VaultConfig holds Vault connection and auth settings.
type VaultConfig struct {
	// Address is the Vault server address (e.g. https://vault.example.com).
	Address string `json:"address"`
	// Namespace is the Vault namespace (enterprise feature).
	// +optional
	Namespace string `json:"namespace,omitempty"`
	// MountPath is the KVv2 secrets engine mount path.
	// +kubebuilder:default=secret
	MountPath string `json:"mountPath,omitempty"`
	// Auth holds the authentication configuration.
	Auth VaultAuthConfig `json:"auth"`
}

// ProviderConfigSpec defines the desired state of a ProviderConfig.
type ProviderConfigSpec struct {
	// Git holds the Git repository configuration. Required when source is git.
	// +optional
	Git *GitConfig `json:"git,omitempty"`
	// Decryption holds the SOPS/age decryption configuration. Required when source is git.
	// +optional
	Decryption *DecryptionConfig `json:"decryption,omitempty"`
	// Vault holds Vault KVv2 connection configuration. Required when source is vault.
	// +optional
	Vault *VaultConfig `json:"vault,omitempty"`
}

// A ProviderConfigStatus defines the status of a ProviderConfig.
type ProviderConfigStatus struct {
	xpv1.ProviderConfigStatus `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="GIT-URL",type="string",JSONPath=".spec.git.url",priority=1
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,provider,kubeconfig}
type ProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProviderConfigSpec   `json:"spec"`
	Status            ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfig `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="CONFIG-NAME",type="string",JSONPath=".providerConfigRef.name"
// +kubebuilder:printcolumn:name="RESOURCE-KIND",type="string",JSONPath=".resourceRef.kind"
// +kubebuilder:printcolumn:name="RESOURCE-NAME",type="string",JSONPath=".resourceRef.name"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,provider,kubeconfig}
type ProviderConfigUsage struct {
	metav1.TypeMeta               `json:",inline"`
	metav1.ObjectMeta             `json:"metadata,omitempty"`
	xpv1.TypedProviderConfigUsage `json:",inline"`
}

// +kubebuilder:object:root=true
type ProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProviderConfigUsage `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="GIT-URL",type="string",JSONPath=".spec.git.url",priority=1
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,kubeconfig}
type ClusterProviderConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ProviderConfigSpec   `json:"spec"`
	Status            ProviderConfigStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type ClusterProviderConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProviderConfig `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="CONFIG-NAME",type="string",JSONPath=".providerConfigRef.name"
// +kubebuilder:printcolumn:name="RESOURCE-KIND",type="string",JSONPath=".resourceRef.kind"
// +kubebuilder:printcolumn:name="RESOURCE-NAME",type="string",JSONPath=".resourceRef.name"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,provider,kubeconfig}
type ClusterProviderConfigUsage struct {
	metav1.TypeMeta               `json:",inline"`
	metav1.ObjectMeta             `json:"metadata,omitempty"`
	xpv1.TypedProviderConfigUsage `json:",inline"`
}

// +kubebuilder:object:root=true
type ClusterProviderConfigUsageList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProviderConfigUsage `json:"items"`
}
