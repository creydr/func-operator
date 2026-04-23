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

	"github.com/functions-dev/func-operator/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *FunctionReconciler) setupPipelineRBAC(ctx context.Context, function *v1alpha1.Function) error {
	if err := r.ensureDeployFunctionRole(ctx, function.Namespace); err != nil {
		return fmt.Errorf("failed to ensure deploy-function role: %w", err)
	}

	if err := r.ensureDeployFunctionRoleBinding(ctx, function); err != nil {
		return fmt.Errorf("failed to ensure deploy-function role binding: %w", err)
	}

	return nil
}

// ensureDeployFunctionRole ensures the deploy-function Role exists in the namespace and is up-to-date.
// This is a namespace-scoped Role so multiple operator instances won't conflict.
func (r *FunctionReconciler) ensureDeployFunctionRole(ctx context.Context, namespace string) error {
	logger := log.FromContext(ctx)

	// TODO: only add the rules which are needed for the functions deployer
	expectedRole := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployFunctionRoleName,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"serving.knative.dev"},
				Resources: []string{"services", "routes"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			}, {
				APIGroups: []string{"eventing.knative.dev"},
				Resources: []string{"triggers"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			}, {
				APIGroups: []string{"apps"},
				Resources: []string{"deployments", "replicasets"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			}, {
				APIGroups: []string{""},
				Resources: []string{"services", "pods"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			}, {
				APIGroups: []string{"http.keda.sh"},
				Resources: []string{"httpscaledobjects"},
				Verbs:     []string{"create", "delete", "get", "list", "patch", "update", "watch"},
			},
		},
	}

	foundRole := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: expectedRole.Name, Namespace: expectedRole.Namespace}, foundRole)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, expectedRole); err != nil {
				return fmt.Errorf("failed to create role: %w", err)
			}
			logger.Info("Created deploy-function role")
			return nil
		}
		return fmt.Errorf("failed to get role: %w", err)
	}

	// Role exists - update if needed
	if !equality.Semantic.DeepEqual(expectedRole.Rules, foundRole.Rules) {
		foundRole.Rules = expectedRole.Rules
		if err := r.Update(ctx, foundRole); err != nil {
			return fmt.Errorf("failed to update role: %w", err)
		}
		logger.Info("Updated deploy-function role")
	} else {
		logger.Info("Deploy-function role already up to date")
	}

	return nil
}

// ensureDeployFunctionRoleBinding ensures the RoleBinding for the deploy-function role exists and is up-to-date.
func (r *FunctionReconciler) ensureDeployFunctionRoleBinding(ctx context.Context, function *v1alpha1.Function) error {
	logger := log.FromContext(ctx)

	expectedRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deploy-function-default",
			Namespace: function.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: v1alpha1.GroupVersion.String(),
					Kind:       "Function",
					Name:       function.Name,
					UID:        function.UID,
					Controller: ptr.To(true),
				},
			},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      "default",
			Namespace: function.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     deployFunctionRoleName,
		},
	}

	foundRoleBinding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: expectedRoleBinding.Name, Namespace: expectedRoleBinding.Namespace}, foundRoleBinding)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if err := r.Create(ctx, expectedRoleBinding); err != nil {
				return fmt.Errorf("failed to create role binding: %w", err)
			}
			logger.Info("Created deploy-function role binding")
			return nil
		}
		return fmt.Errorf("failed to get role binding: %w", err)
	}

	// Update if needed
	if !equality.Semantic.DeepDerivative(expectedRoleBinding, foundRoleBinding) {
		foundRoleBinding.Subjects = expectedRoleBinding.Subjects
		foundRoleBinding.RoleRef = expectedRoleBinding.RoleRef
		foundRoleBinding.OwnerReferences = expectedRoleBinding.OwnerReferences

		if err := r.Update(ctx, foundRoleBinding); err != nil {
			return fmt.Errorf("failed to update role binding: %w", err)
		}
		logger.Info("Updated deploy-function role binding")
	} else {
		logger.Info("Deploy-function role binding already up to date")
	}

	return nil
}
