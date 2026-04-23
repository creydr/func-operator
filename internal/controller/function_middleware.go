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
	"strconv"
	"strings"

	"github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	funcfn "knative.dev/func/pkg/functions"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type middlewareState struct {
	updateEnabled  bool
	updateSource   string
	isLatest       bool
	currentVersion string
	latestVersion  string
}

// handleMiddlewareUpdate is the single owner of all middleware, service, and deploy-ready
// conditions. Helper methods it calls (checkMiddlewareState, deploy) perform work but
// never set conditions — that responsibility stays here.
func (r *FunctionReconciler) handleMiddlewareUpdate(ctx context.Context, function *v1alpha1.Function, repo *git.Repository, metadata *funcfn.Function) error {
	logger := log.FromContext(ctx)

	// Get current function state
	describe, err := r.FuncCliManager.Describe(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return fmt.Errorf("failed to describe function: %w", err)
	}
	function.Status.Deployment.Image = describe.Image
	markServiceStatus(describe.Ready, function)

	// Check middleware state
	state, err := r.checkMiddlewareState(ctx, function, metadata)
	if err != nil {
		function.MarkMiddlewareNotUpToDate("MiddlewareCheckFailed", "Failed to check middleware: %s", err)
		return err
	}
	function.Status.Middleware.AutoUpdate.Enabled = state.updateEnabled
	function.Status.Middleware.AutoUpdate.Source = state.updateSource
	function.Status.Middleware.Current = describe.Middleware.Version
	function.Status.Middleware.PendingRebuild = false

	// Act based on middleware state
	switch {
	case state.isLatest:
		logger.Info("Function is on latest middleware", "version", state.currentVersion)
		function.MarkMiddlewareUpToDate()
		function.Status.Middleware.Available = nil

	case !state.updateEnabled:
		logger.Info("Middleware update available but disabled", "source", state.updateSource)
		function.Status.Middleware.Available = ptr.To(state.latestVersion)
		function.MarkMiddlewareNotUpToDateIntentionally("SkipMiddlewareUpdate", "Skipping middleware update as update is disabled (source: %s)", state.updateSource)

	default:
		logger.Info("Redeploying for middleware update", "current", state.currentVersion, "latest", state.latestVersion)
		function.Status.Middleware.Available = ptr.To(state.latestVersion)
		function.MarkMiddlewareNotUpToDate("MiddlewareOutdated", "Middleware is outdated (%s available), redeploying...", state.latestVersion)
		function.Status.Middleware.PendingRebuild = true

		if err := FlushStatus(ctx, function); err != nil {
			logger.Error(err, "Failed to update status before redeployment")
		}

		if err := r.deploy(ctx, function, repo); err != nil {
			function.MarkDeployNotReady("DeployFailed", "Redeployment failed: %s", err.Error())
			return fmt.Errorf("failed to redeploy function: %w", err)
		}

		function.Status.Middleware.PendingRebuild = false
		function.Status.Middleware.LastRebuild = metav1.Now()
		function.Status.Deployment.ImageBuilt = metav1.Now()
		function.Status.Middleware.Available = nil
		function.RecordHistoryEvent(fmt.Sprintf("Middleware updated from %q to %q", state.currentVersion, state.latestVersion))
		function.MarkMiddlewareUpToDate()
	}

	// Refresh deployment status after potential redeploy
	describe, err = r.FuncCliManager.Describe(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return fmt.Errorf("failed to refresh function status: %w", err)
	}
	function.Status.Deployment.Image = describe.Image
	function.Status.Middleware.Current = describe.Middleware.Version
	markServiceStatus(describe.Ready, function)
	function.MarkDeployReady()

	return nil
}

// checkMiddlewareState gathers all middleware version information needed to decide
// whether a redeploy is necessary. It performs only work — no conditions are set here.
func (r *FunctionReconciler) checkMiddlewareState(ctx context.Context, function *v1alpha1.Function, metadata *funcfn.Function) (middlewareState, error) {
	updateEnabled, source, err := r.isMiddlewareUpdateEnabled(ctx, function)
	if err != nil {
		return middlewareState{}, fmt.Errorf("failed to check if middleware should be updated: %w", err)
	}

	latestVersion, err := r.FuncCliManager.GetLatestMiddlewareVersion(ctx, metadata.Runtime, metadata.Invoke)
	if err != nil {
		return middlewareState{}, fmt.Errorf("failed to get latest middleware version: %w", err)
	}

	currentVersion, err := r.FuncCliManager.GetMiddlewareVersion(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return middlewareState{}, fmt.Errorf("failed to get current middleware version: %w", err)
	}

	return middlewareState{
		updateEnabled:  updateEnabled,
		updateSource:   source,
		isLatest:       currentVersion == latestVersion,
		currentVersion: currentVersion,
		latestVersion:  latestVersion,
	}, nil
}

func markServiceStatus(ready string, function *v1alpha1.Function) {
	switch strings.ToLower(ready) {
	case "true":
		function.MarkServiceReady()
	case "false":
		function.MarkServiceNotReady("ServiceNotReady", "Underlying service is not ready")
	default:
		function.MarkServiceNotReady("ServiceReadyUnknown", "Underlying service readiness is unknown")
	}
}

// isMiddlewareUpdateEnabled returns if the middleware should be updated given by the functions spec or the operators
// default.
func (r *FunctionReconciler) isMiddlewareUpdateEnabled(ctx context.Context, function *v1alpha1.Function) (bool, string, error) {
	logger := log.FromContext(ctx)

	// setting from function overrides operator default
	if function.Spec.AutoUpdateMiddleware != nil {
		return *function.Spec.AutoUpdateMiddleware, "function", nil
	}

	// nothing defined in function spec --> check operator config
	cm := &v1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Namespace: r.OperatorNamespace, Name: controllerConfigName}, cm)
	if err != nil {
		return false, "", fmt.Errorf("failed to get operator config configmap: %w", err)
	}

	val, ok := cm.Data["autoUpdateMiddleware"]
	if !ok {
		logger.Info("No autoUpdateMiddleware field in configmap found. Fallback to hardcoded autoUpdateMiddleware=true")
		// TODO: check if returning an error would be better here
		return true, "operator", nil
	}

	boolVal, err := strconv.ParseBool(val)
	if err != nil {
		return false, "", fmt.Errorf("failed to parse autoUpdateMiddleware value from configmap: %w", err)
	}

	return boolVal, "operator", nil
}
