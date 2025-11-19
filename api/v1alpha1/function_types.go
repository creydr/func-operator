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

package v1alpha1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Function is the Schema for the functions API.
type Function struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FunctionSpec   `json:"spec,omitempty"`
	Status FunctionStatus `json:"status,omitempty"`
}

// FunctionSpec defines the desired state of Function.
type FunctionSpec struct {
	Source   FunctionSpecSource   `json:"source,omitempty"`
	Registry FunctionSpecRegistry `json:"registry,omitempty"`
}

type FunctionSpecSource struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	RepositoryURL string `json:"repositoryUrl"`
}

type FunctionSpecRegistry struct {
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Path string `json:"path"`

	Insecure bool `json:"insecure,omitempty"`

	AuthSecretRef *v1.LocalObjectReference `json:"authSecretRef,omitempty"`
}

// FunctionStatus defines the observed state of Function.
type FunctionStatus struct {
	CliVersion string `json:"cliVersion"`

	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// FunctionList contains a list of Function.
type FunctionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Function `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Function{}, &FunctionList{})
}
