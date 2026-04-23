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
	"strings"

	"github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/internal/funccli"
	"github.com/functions-dev/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *FunctionReconciler) deploy(ctx context.Context, function *v1alpha1.Function, repo *git.Repository) error {
	logger := log.FromContext(ctx)

	if err := r.setupPipelineRBAC(ctx, function); err != nil {
		return fmt.Errorf("failed to setup pipeline RBAC: %w", err)
	}

	deployOptions := funccli.DeployOptions{}

	if function.Spec.Registry.AuthSecretRef != nil && function.Spec.Registry.AuthSecretRef.Name != "" {
		authFile, err := r.persistRegistryAuthSecret(ctx, function)
		if err != nil {
			return fmt.Errorf("failed to persist registry auth secret temporarily: %w", err)
		}

		defer os.Remove(authFile)

		deployOptions.RegistryAuthFile = authFile
	}

	logger.Info("Deploying function", "deployOptions", deployOptions)
	err := r.FuncCliManager.Deploy(ctx, repo.Path(), function.Namespace, deployOptions)
	if err != nil {
		return fmt.Errorf("failed to deploy function: %w", err)
	}

	logger.Info("function deployed successfully")

	return nil
}

func (r *FunctionReconciler) persistRegistryAuthSecret(ctx context.Context, function *v1alpha1.Function) (string, error) {
	logger := log.FromContext(ctx)

	logger.Info("Persist registry auth secret temporarily")

	authSecret := &v1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: function.Spec.Registry.AuthSecretRef.Name, Namespace: function.Namespace}, authSecret)
	if err != nil {
		logger.Error(err, "Failed to get registry auth secret", "secret", function.Spec.Registry.AuthSecretRef.Name, "namespace", function.Namespace)
		return "", fmt.Errorf("failed to get registry auth secret: %w", err)
	}

	if authSecret.Type != v1.SecretTypeDockerConfigJson {
		return "", fmt.Errorf("invalid registry auth secret type, must be of type %s", v1.SecretTypeDockerConfigJson)
	}

	if authSecret.Data[v1.DockerConfigJsonKey] == nil {
		return "", fmt.Errorf("invalid registry auth secret data, must contain key %s", v1.DockerConfigJsonKey)
	}

	authFile, err := os.CreateTemp("", "auth-file-*.json")
	if err != nil {
		logger.Error(err, "Failed to create temp auth file")
		return "", fmt.Errorf("failed to create temp auth file: %w", err)
	}
	defer authFile.Close()

	_, err = authFile.Write(authSecret.Data[v1.DockerConfigJsonKey])
	if err != nil {
		logger.Error(err, "Failed to write temp auth file")
		return "", fmt.Errorf("failed to write temp auth file: %w", err)
	}

	return authFile.Name(), nil
}

func (r *FunctionReconciler) isDeployed(ctx context.Context, name, namespace string) (bool, error) {
	_, err := r.FuncCliManager.Describe(ctx, name, namespace)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no describe function") {
			return false, nil
		}

		return false, fmt.Errorf("failed to describe function: %w", err)
	}

	return true, nil
}
