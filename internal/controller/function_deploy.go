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

package controller

import (
	"context"
	"fmt"
	"os"

	"github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/internal/funccli"
	"github.com/functions-dev/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *FunctionReconciler) deploy(ctx context.Context, function *v1alpha1.Function, repo *git.Repository) error {
	if err := r.setupPipelineRBAC(ctx, function); err != nil {
		return fmt.Errorf("failed to setup pipeline RBAC: %w", err)
	}

	opts := funccli.DeployOptions{}

	if function.Spec.Registry.AuthSecretRef != nil && function.Spec.Registry.AuthSecretRef.Name != "" {
		authFile, err := r.writeRegistryAuthFile(ctx, function)
		if err != nil {
			return fmt.Errorf("failed to write registry auth file: %w", err)
		}
		defer os.Remove(authFile)
		opts.RegistryAuthFile = authFile
	}

	log.FromContext(ctx).Info("Deploying function")
	if err := r.FuncCliManager.Deploy(ctx, repo.Path(), function.Namespace, opts); err != nil {
		return fmt.Errorf("failed to deploy function: %w", err)
	}

	return nil
}

func (r *FunctionReconciler) writeRegistryAuthFile(ctx context.Context, function *v1alpha1.Function) (string, error) {
	authSecret := &v1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: function.Spec.Registry.AuthSecretRef.Name, Namespace: function.Namespace}, authSecret); err != nil {
		return "", fmt.Errorf("failed to get registry auth secret: %w", err)
	}

	if authSecret.Type != v1.SecretTypeDockerConfigJson {
		return "", fmt.Errorf("registry auth secret must be type %s", v1.SecretTypeDockerConfigJson)
	}

	if authSecret.Data[v1.DockerConfigJsonKey] == nil {
		return "", fmt.Errorf("registry auth secret must contain key %s", v1.DockerConfigJsonKey)
	}

	f, err := os.CreateTemp("", "auth-file-*.json")
	if err != nil {
		return "", fmt.Errorf("failed to create temp auth file: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(authSecret.Data[v1.DockerConfigJsonKey]); err != nil {
		return "", fmt.Errorf("failed to write temp auth file: %w", err)
	}

	return f.Name(), nil
}
