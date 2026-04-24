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
	"strings"

	"github.com/functions-dev/func-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

type reconcileState struct {
	source     *sourceState
	deployment *deploymentState
	middleware *middlewareState
}

type sourceState struct {
	name        string
	branch      string
	commit      string
	failReason  string
	failMessage string
}

type deploymentState struct {
	deployed    bool
	deployer    string
	runtime     string
	image       string
	ready       string
	failReason  string
	failMessage string
}

type middlewareState struct {
	updateEnabled  bool
	updateSource   string
	isLatest       bool
	currentVersion string
	latestVersion  string
	pendingRebuild bool
	redeployed     bool
	lastRebuild    metav1.Time
	historyMessage string
	failReason     string
	failMessage    string
}

func syncStatus(function *v1alpha1.Function, state *reconcileState) {
	// --- Source ---
	if state.source == nil {
		return
	}
	if state.source.failReason != "" {
		function.MarkSourceNotReady(state.source.failReason, "%s", state.source.failMessage)
		return
	}
	function.MarkSourceReady()
	function.Status.Name = state.source.name
	function.Status.Git.ResolvedBranch = state.source.branch
	function.Status.Git.ObservedCommit = state.source.commit
	function.Status.Git.LastChecked = metav1.Now()

	// --- Deployment ---
	if state.deployment == nil {
		return
	}
	if state.deployment.failReason != "" {
		function.MarkDeployNotReady(state.deployment.failReason, "%s", state.deployment.failMessage)
		return
	}
	if !state.deployment.deployed {
		function.MarkDeployNotReady("NotDeployed", "Function not deployed yet")
		return
	}
	function.MarkDeployReady()
	function.Status.Deployment.Deployer = state.deployment.deployer
	function.Status.Deployment.Runtime = state.deployment.runtime
	function.Status.Deployment.Image = state.deployment.image
	markServiceStatus(state.deployment.ready, function)

	// --- Middleware ---
	if state.middleware == nil {
		return
	}
	if state.middleware.failReason != "" {
		function.MarkMiddlewareNotUpToDate(state.middleware.failReason, "%s", state.middleware.failMessage)
		return
	}
	switch {
	case state.middleware.isLatest:
		function.MarkMiddlewareUpToDate()
	case !state.middleware.updateEnabled:
		function.Status.Middleware.Available = ptr.To(state.middleware.latestVersion)
		function.MarkMiddlewareNotUpToDateIntentionally("SkipMiddlewareUpdate",
			"Skipping middleware update as update is disabled (source: %s)", state.middleware.updateSource)
	case state.middleware.redeployed:
		function.Status.Middleware.Available = nil
		function.Status.Middleware.LastRebuild = state.middleware.lastRebuild
		function.Status.Deployment.ImageBuilt = state.middleware.lastRebuild
		function.MarkMiddlewareUpToDate()
		function.MarkDeployReady()
		if state.middleware.historyMessage != "" {
			function.RecordHistoryEvent(state.middleware.historyMessage)
		}
	}
	function.Status.Middleware.AutoUpdate.Enabled = state.middleware.updateEnabled
	function.Status.Middleware.AutoUpdate.Source = state.middleware.updateSource
	function.Status.Middleware.Current = state.middleware.currentVersion
	function.Status.Middleware.PendingRebuild = state.middleware.pendingRebuild
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
