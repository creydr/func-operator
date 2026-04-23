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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/internal/funccli"
	fn "github.com/functions-dev/func-operator/internal/function"
	"github.com/functions-dev/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/retry"
	funcfn "knative.dev/func/pkg/functions"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	deployFunctionRoleName = "func-operator-deploy-function"
	controllerConfigName   = "func-operator-controller-config"

	funcAnnotationPrefix       = "functions.knative.dev/"
	funcAnnotationLastDeployed = funcAnnotationPrefix + "last-deployed"
)

// reconcileError carries a condition reason alongside the error so the
// calling orchestrator can set the right status condition.
type reconcileError struct {
	reason string
	err    error
}

func (e *reconcileError) Error() string { return e.err.Error() }
func (e *reconcileError) Unwrap() error { return e.err }

// FunctionReconciler reconciles a Function object
type FunctionReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	Recorder          events.EventRecorder
	FuncCliManager    funccli.Manager
	GitManager        git.Manager
	OperatorNamespace string
}

// +kubebuilder:rbac:groups=functions.dev,resources=functions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=functions.dev,resources=functions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=functions.dev,resources=functions/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods;pods/attach;secrets;services;persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="apps",resources=deployments;replicasets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="serving.knative.dev",resources=services;routes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="eventing.knative.dev",resources=triggers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelines;pipelineruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tekton.dev,resources=taskruns,verbs=get;list;watch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings;roles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=http.keda.sh,resources=httpscaledobjects,verbs=get;list;watch;create;update;patch;delete

// Reconcile a Function with status update
func (r *FunctionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("function", fmt.Sprintf("%s/%s", req.Namespace, req.Name))

	// add logger with values to context back
	ctx = log.IntoContext(ctx, logger)

	original := &v1alpha1.Function{}
	err := r.Get(ctx, req.NamespacedName, original)
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

	function := original.DeepCopy()

	// Create tracker and add to context
	statusTracker := NewStatusTracker(r.Client, function)
	ctx = WithStatusTracker(ctx, statusTracker)

	reconcileErr := r.reconcile(ctx, function)

	// Final flush at the end (handles ready condition calculation)
	if err := statusTracker.Flush(ctx, function); err != nil {
		logger.Error(err, "Unable to update Function status")
		return ctrl.Result{}, err
	}

	if reconcileErr != nil {
		logger.Error(reconcileErr, "Failed to reconcile Function")
		return ctrl.Result{}, reconcileErr
	}

	if err := r.removeFuncAnnotations(ctx, function); err != nil {
		logger.Error(err, "Failed to remove func annotations")
		return ctrl.Result{}, err
	}

	logger.Info("Reconciliation complete")
	return ctrl.Result{}, nil
}

func (r *FunctionReconciler) reconcile(ctx context.Context, function *v1alpha1.Function) error {
	function.InitializeConditions()

	repo, metadata, err := r.prepareSource(ctx, function)
	if err != nil {
		var re *reconcileError
		if errors.As(err, &re) {
			function.MarkSourceNotReady(re.reason, "%s", re.Error())
		}
		return fmt.Errorf("prepare source failed: %w", err)
	}
	defer repo.Cleanup()

	function.MarkSourceReady()
	function.Status.Name = metadata.Name
	function.Status.Git.ResolvedBranch = repo.Branch
	function.Status.Git.ObservedCommit = repo.Commit
	function.Status.Git.LastChecked = metav1.Now()
	applyLastDeployedAnnotation(ctx, function)

	if err := r.ensureDeployment(ctx, function, repo, metadata); err != nil {
		return fmt.Errorf("deploying function failed: %w", err)
	}

	if err := FlushStatus(ctx, function); err != nil {
		return fmt.Errorf("failed to update status: %w", err)
	}

	return nil
}

// prepareSource clones the git repository and retrieves function metadata.
// It performs only work — all status/condition updates are handled by the caller.
func (r *FunctionReconciler) prepareSource(ctx context.Context, function *v1alpha1.Function) (*git.Repository, *funcfn.Function, error) {
	branchReference := "main"
	if function.Spec.Repository.Branch != "" {
		branchReference = function.Spec.Repository.Branch
	}

	gitAuthSecret := v1.Secret{}
	if function.Spec.Repository.AuthSecretRef != nil {
		if err := r.Get(ctx, types.NamespacedName{Namespace: function.Namespace, Name: function.Spec.Repository.AuthSecretRef.Name}, &gitAuthSecret); err != nil {
			return nil, nil, &reconcileError{reason: "AuthSecretNotFound", err: fmt.Errorf("auth secret not found: %w", err)}
		}
	}

	repo, err := r.GitManager.CloneRepository(ctx, function.Spec.Repository.URL, function.Spec.Repository.Path, branchReference, gitAuthSecret.Data)
	if err != nil {
		return nil, nil, &reconcileError{reason: "GitCloneFailed", err: fmt.Errorf("failed to clone repository: %w", err)}
	}

	metadata, err := fn.Metadata(repo.Path())
	if err != nil {
		return nil, nil, &reconcileError{reason: "MetadataReadFailed", err: fmt.Errorf("failed to read function metadata: %w", err)}
	}

	return repo, &metadata, nil
}

// ensureDeployment ensures the function is deployed and up-to-date
func (r *FunctionReconciler) ensureDeployment(ctx context.Context, function *v1alpha1.Function, repo *git.Repository, metadata *funcfn.Function) error {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling Function")

	deployed, err := r.isDeployed(ctx, metadata.Name, function.Namespace)
	if err != nil {
		function.MarkDeployNotReady("DeployFailed", "Failed to check deployment status: %s", err.Error())
		return fmt.Errorf("failed to check if function is already deployed: %w", err)
	}

	if !deployed {
		logger.Info("Function is not deployed")
		function.MarkDeployNotReady("NotDeployed", "Function not deployed yet")
		return nil
	}

	// function is deployed -> update status with metadata information
	deployer := metadata.Deploy.Deployer
	if deployer == "" {
		deployer = "knative"
	}
	function.Status.Deployment.Deployer = deployer
	function.Status.Deployment.Runtime = metadata.Runtime

	return r.handleMiddlewareUpdate(ctx, function, repo, metadata)
}

func (r *FunctionReconciler) removeFuncAnnotations(ctx context.Context, function *v1alpha1.Function) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &v1alpha1.Function{}
		if err := r.Get(ctx, types.NamespacedName{Name: function.Name, Namespace: function.Namespace}, latest); err != nil {
			return err
		}

		if !hasFuncAnnotations(latest) {
			return nil
		}

		annotations := latest.GetAnnotations()
		for key := range annotations {
			if strings.HasPrefix(key, funcAnnotationPrefix) {
				delete(annotations, key)
			}
		}

		latest.SetAnnotations(annotations)
		return r.Update(ctx, latest)
	})
}

func applyLastDeployedAnnotation(ctx context.Context, function *v1alpha1.Function) {
	val, ok := function.Annotations[funcAnnotationLastDeployed]
	if !ok {
		return
	}
	t, err := time.Parse(time.RFC3339, val)
	if err != nil {
		log.FromContext(ctx).Info("could not parse "+funcAnnotationLastDeployed+" annotation", "error", err)
		return
	}
	function.Status.Deployment.ImageBuilt = metav1.NewTime(t)
}

// SetupWithManager sets up the controller with the Manager.
func (r *FunctionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Reconcile Functions on spec changes (generation change) or when
		// "functions.knative.dev/" annotations are present. This predicate is applied
		// to For() instead of WithEventFilter() to ensure it doesn't filter out
		// ConfigMap-triggered reconciliations.
		For(&v1alpha1.Function{}, builder.WithPredicates(predicate.Or(predicate.GenerationChangedPredicate{}, FuncAnnotationChangedPredicate{}))).
		Watches(
			&v1.ConfigMap{},
			handler.EnqueueRequestsFromMapFunc(r.findFunctionsForConfigMap),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				// Only watch the controller-config ConfigMap in the operator namespace
				return obj.GetName() == controllerConfigName && obj.GetNamespace() == r.OperatorNamespace
			})),
		).
		Named("function").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 100, // TODO: find a good value
		}).
		Complete(r)
}

// findFunctionsForConfigMap returns reconcile requests for all Functions that should be
// reconciled when the controller-config ConfigMap changes. This triggers reconciliation
// for Functions that rely on the operator-wide default (i.e., those without an explicit
// autoUpdateMiddleware setting).
//
// Note: This function is safe for multi-controller setups. The List() call uses the manager's
// cached client, which is already scoped to the namespaces this controller is watching
// (via WATCH_NAMESPACE env var). Each controller instance only reconciles Functions in its
// own watched namespaces.
func (r *FunctionReconciler) findFunctionsForConfigMap(ctx context.Context, _ client.Object) []reconcile.Request {
	logger := log.FromContext(ctx)

	// List all Functions in the watched namespaces (scoped by the manager's cache)
	functionList := &v1alpha1.FunctionList{}
	if err := r.List(ctx, functionList); err != nil {
		logger.Error(err, "Failed to list Functions for ConfigMap watch")
		return []reconcile.Request{}
	}

	requests := make([]reconcile.Request, 0, len(functionList.Items))
	for _, function := range functionList.Items {
		// Only enqueue Functions that rely on the operator default
		// (i.e., those without an explicit autoUpdateMiddleware setting)
		if function.Spec.AutoUpdateMiddleware == nil {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      function.Name,
					Namespace: function.Namespace,
				},
			})
		}
	}

	logger.Info("Enqueueing Functions for reconciliation due to ConfigMap change", "count", len(requests))
	return requests
}
