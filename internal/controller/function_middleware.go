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

	"github.com/functions-dev/func-operator/api/v1alpha1"
	"github.com/functions-dev/func-operator/internal/git"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	funcfn "knative.dev/func/pkg/functions"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func (r *FunctionReconciler) handleMiddlewareUpdate(ctx context.Context, function *v1alpha1.Function, repo *git.Repository, metadata *funcfn.Function, state *reconcileState, describe funcfn.Instance) error {
	logger := log.FromContext(ctx)

	mwState, err := r.checkMiddlewareState(ctx, function, metadata)
	if err != nil {
		state.middleware = &middlewareState{
			failReason:  "MiddlewareCheckFailed",
			failMessage: fmt.Sprintf("Failed to check middleware: %s", err),
		}
		return err
	}

	state.middleware = &mwState

	switch {
	case mwState.isLatest:
		logger.Info("Function is on latest middleware", "version", mwState.currentVersion)

	case !mwState.updateEnabled:
		logger.Info("Middleware update available but disabled", "source", mwState.updateSource)

	default:
		if err := r.redeployMiddleware(ctx, function, repo, state); err != nil {
			return err
		}
	}

	return r.refreshDeploymentState(ctx, metadata, function, state)
}

func (r *FunctionReconciler) redeployMiddleware(ctx context.Context, function *v1alpha1.Function, repo *git.Repository, state *reconcileState) error {
	logger := log.FromContext(ctx)
	logger.Info("Redeploying for middleware update", "current", state.middleware.currentVersion, "latest", state.middleware.latestVersion)

	state.middleware.pendingRebuild = true
	state.middleware.failReason = "MiddlewareOutdated"
	state.middleware.failMessage = fmt.Sprintf("Middleware is outdated (%s available), redeploying...", state.middleware.latestVersion)

	syncStatus(function, state)
	if err := FlushStatus(ctx, function); err != nil {
		logger.Error(err, "Failed to update status before redeployment")
	}

	if err := r.deploy(ctx, function, repo); err != nil {
		state.middleware.failReason = "DeployFailed"
		state.middleware.failMessage = fmt.Sprintf("Redeployment failed: %s", err)
		return fmt.Errorf("failed to redeploy function: %w", err)
	}

	now := metav1.Now()
	state.middleware.pendingRebuild = false
	state.middleware.redeployed = true
	state.middleware.lastRebuild = now
	state.middleware.failReason = ""
	state.middleware.failMessage = ""
	state.middleware.historyMessage = fmt.Sprintf("Middleware updated from %q to %q", state.middleware.currentVersion, state.middleware.latestVersion)

	return nil
}

func (r *FunctionReconciler) refreshDeploymentState(ctx context.Context, metadata *funcfn.Function, function *v1alpha1.Function, state *reconcileState) error {
	describe, err := r.FuncCliManager.Describe(ctx, metadata.Name, function.Namespace)
	if err != nil {
		return fmt.Errorf("failed to refresh function status: %w", err)
	}

	state.deployment.image = describe.Image
	state.deployment.ready = describe.Ready
	state.middleware.currentVersion = describe.Middleware.Version
	return nil
}

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

// Precedence: function spec > operator configmap > hardcoded default (true).
func (r *FunctionReconciler) isMiddlewareUpdateEnabled(ctx context.Context, function *v1alpha1.Function) (bool, string, error) {
	if function.Spec.AutoUpdateMiddleware != nil {
		return *function.Spec.AutoUpdateMiddleware, "function", nil
	}

	cm := &v1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: r.OperatorNamespace, Name: controllerConfigName}, cm); err != nil {
		return false, "", fmt.Errorf("failed to get operator config configmap: %w", err)
	}

	val, ok := cm.Data["autoUpdateMiddleware"]
	if !ok {
		return true, "operator", nil
	}

	boolVal, err := strconv.ParseBool(val)
	if err != nil {
		return false, "", fmt.Errorf("failed to parse autoUpdateMiddleware value from configmap: %w", err)
	}

	return boolVal, "operator", nil
}
