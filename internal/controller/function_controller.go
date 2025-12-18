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
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/creydr/func-operator/internal/funccli"
	fn "github.com/creydr/func-operator/internal/function"
	"github.com/creydr/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	funcfn "knative.dev/func/pkg/functions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/creydr/func-operator/api/v1alpha1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// FunctionReconciler reconciles a Function object
type FunctionReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Recorder       record.EventRecorder
	FuncCliManager funccli.Manager
	GitManager     git.Manager
}

// +kubebuilder:rbac:groups=functions.dev,resources=functions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=functions.dev,resources=functions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=functions.dev,resources=functions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="apps",resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="serving.knative.dev",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="eventing.knative.dev",resources=triggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelines;pipelineruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=taskruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=get;list;watch;create;update;patch;delete

// Reconcile a Function
func (r *FunctionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	function := &v1alpha1.Function{}
	err := r.Get(ctx, req.NamespacedName, function)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// If the custom resource is not found then it usually means that it was deleted or not created
			// In this way, we will stop the reconciliation
			logger.Info("function resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get function")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciling Function", "function", req.NamespacedName)

	// get metadata from repo
	repo, err := r.GitManager.CloneRepository(ctx, function.Spec.Source.RepositoryURL, "main")
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to setup git repository: %w", err)
	}

	metadata, err := fn.Metadata(repo.Path())
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get function metadata: %w", err)
	}

	// deploy if needed
	deployed, err := r.isDeployed(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to check if function is already deployed: %w", err)
	}

	if deployed {
		// check middleware for updates and eventually redeploy

		isOnLatestMiddleware, err := r.isMiddlewareLatest(ctx, metadata, function.Namespace)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check if function is using latest middleware: %w", err)
		}

		if !isOnLatestMiddleware {
			logger.Info("Function is not on latest middleware. Will redeploy", "function", req.NamespacedName)
			if err = r.deploy(ctx, function, repo); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to redeploy function: %w", err)
			}
		} else {
			logger.Info("Function is deployed with latest middleware. No need to redeploy", "function", req.NamespacedName)
		}
	} else {
		// simply deploy
		logger.Info("Function is not deployed already. Will deploy it", "function", req.NamespacedName)

		if err = r.deploy(ctx, function, repo); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to deploy function: %w", err)
		}
	}

	// function status update
	functionImage, err := r.deployedImage(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get deployed image of function: %w", err)
	}

	funcMiddlewareVersion, err := r.FuncCliManager.GetMiddlewareVersion(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get function middleware version: %w", err)
	}

	function.Status.Name = metadata.Name
	function.Status.Runtime = metadata.Runtime
	function.Status.DeployedImage = functionImage
	function.Status.MiddlewareVersion = funcMiddlewareVersion

	if err = r.Status().Update(ctx, function); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update function: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *FunctionReconciler) setupPipelineRBAC(ctx context.Context, function *v1alpha1.Function) error {
	logger := logf.FromContext(ctx)

	logger.Info("Create rolebinding for deploy-function role")
	expectedRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "deploy-function-default",
			Namespace: function.Namespace,
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: function.APIVersion,
					Kind:       function.Kind,
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
			Kind:     "ClusterRole",
			Name:     "func-operator-deploy-function",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}
	foundRoleBinding := &rbacv1.RoleBinding{}
	err := r.Get(ctx, types.NamespacedName{Name: expectedRoleBinding.Name, Namespace: expectedRoleBinding.Namespace}, foundRoleBinding)
	if err != nil {
		if apierrors.IsNotFound(err) {
			err = r.Create(ctx, expectedRoleBinding)
			if err != nil {
				return fmt.Errorf("failed to create role binding for deploy-function role: %w", err)
			}
			logger.Info("Created role binding for deploy-function role")
			return nil
		}

		return fmt.Errorf("failed to check if deploy-function role binding already exists: %w", err)
	} else {
		if !equality.Semantic.DeepDerivative(expectedRoleBinding, foundRoleBinding) {
			err = r.Update(ctx, foundRoleBinding)
			if err != nil {
				return fmt.Errorf("failed to update deploy-function role binding: %w", err)
			}

			logger.Info("Updated deploy-function role binding")
			return nil
		}

		logger.Info("Role binding already exists and is up to date. No need to update")
	}

	return nil
}

func (r *FunctionReconciler) persistRegistryAuthSecret(ctx context.Context, function *v1alpha1.Function) (string, error) {
	logger := logf.FromContext(ctx)

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

	// persist secret temporarily
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

func (r *FunctionReconciler) deploy(ctx context.Context, function *v1alpha1.Function, repo *git.Repository) error {
	logger := logf.FromContext(ctx)

	if err := r.setupPipelineRBAC(ctx, function); err != nil {
		return fmt.Errorf("failed to setup pipeline RBAC: %w", err)
	}

	// deploy function
	deployArgs := []string{
		"deploy",
		"--remote",
		"--namespace", function.Namespace,
		"--registry", function.Spec.Registry.Path,
		"--git-url", function.Spec.Source.RepositoryURL,
		"--builder", "s2i",
	}

	if function.Spec.Registry.Insecure {
		deployArgs = append(deployArgs, "--registry-insecure")
	}

	if function.Spec.Registry.AuthSecretRef != nil && function.Spec.Registry.AuthSecretRef.Name != "" {
		// we have a registry auth secret referenced -> use this for func deploy
		authFile, err := r.persistRegistryAuthSecret(ctx, function)
		if err != nil {
			return fmt.Errorf("failed to persist registry auth secret temporarily: %w", err)
		}

		defer os.Remove(authFile)

		deployArgs = append(deployArgs, "--registry-authfile", authFile)
	}

	logger.Info("Deploying function", "function", function.Name, "namespace", function.Namespace, "deployArgs", deployArgs)
	out, err := r.FuncCliManager.Run(ctx, repo.Path(), deployArgs...)
	if err != nil {
		logger.Error(err, "Failed to deploy function", "output", out)
		return fmt.Errorf("failed to deploy function: %w", err)
	}

	logger.Info("function deployed successfully", "output", out)

	return nil
}

func (r *FunctionReconciler) isDeployed(ctx context.Context, name, namespace string) (bool, error) {
	out, err := r.FuncCliManager.Run(ctx, "", "--namespace", namespace, "describe", name)
	if err != nil {
		if strings.Contains(out, "not found") || strings.Contains(out, "no describe function") {
			return false, nil
		}

		return false, fmt.Errorf("failed to describe function: %w", err)
	}

	return true, nil
}

func (r *FunctionReconciler) deployedImage(ctx context.Context, name, namespace string) (string, error) {
	out, err := r.FuncCliManager.Run(ctx, "", "--namespace", namespace, "describe", name, "--output", "json")
	if err != nil {
		return "", fmt.Errorf("failed to describe function: %w", err)
	}

	result := make(map[string]interface{})
	if err = json.Unmarshal([]byte(out), &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal describe output: %w", err)
	}

	image, ok := result["image"]
	if !ok {
		return "", fmt.Errorf("failed to find image in describe output")
	}

	sImage, ok := image.(string)
	if !ok {
		return "", fmt.Errorf("failed to convert image from describe output to string")
	}

	return sImage, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FunctionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Function{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}). // only reconcile when the spec changed (e.g. not on status updates)
		Named("function").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100, // TODO: find a good value
		}).
		Complete(r)
}

func (r *FunctionReconciler) isMiddlewareLatest(ctx context.Context, metadata funcfn.Function, namespace string) (bool, error) {
	latestMiddleware, err := r.FuncCliManager.GetLatestMiddlewareVersion(ctx, metadata.Runtime, metadata.Invoke)
	if err != nil {
		return false, fmt.Errorf("failed to get latest available middleware version: %w", err)
	}

	functionMiddleware, err := r.FuncCliManager.GetMiddlewareVersion(ctx, metadata.Name, namespace)
	if err != nil {
		return false, fmt.Errorf("failed to get middleware version of function: %w", err)
	}

	return latestMiddleware == functionMiddleware, nil
}
