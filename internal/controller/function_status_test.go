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
	"github.com/functions-dev/func-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("syncStatus", func() {
	var function *v1alpha1.Function

	BeforeEach(func() {
		function = &v1alpha1.Function{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default"},
		}
		function.InitializeConditions()
	})

	Context("source phase", func() {
		It("should leave conditions unknown when source is nil", func() {
			state := &reconcileState{}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeSourceReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		})

		It("should mark source not ready on failure", func() {
			state := &reconcileState{
				source: &sourceState{
					failReason:  "GitCloneFailed",
					failMessage: "connection refused",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeSourceReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("GitCloneFailed"))
			Expect(cond.Message).To(Equal("connection refused"))

			deployCond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(deployCond.Status).To(Equal(metav1.ConditionUnknown))
		})

		It("should mark source ready and set git status fields", func() {
			state := &reconcileState{
				source: &sourceState{
					name:   "my-func",
					branch: "main",
					commit: "abc123",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeSourceReady)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			Expect(function.Status.Name).To(Equal("my-func"))
			Expect(function.Status.Git.ResolvedBranch).To(Equal("main"))
			Expect(function.Status.Git.ObservedCommit).To(Equal("abc123"))
			Expect(function.Status.Git.LastChecked.IsZero()).To(BeFalse())
		})
	})

	Context("deployment phase", func() {
		baseSource := &sourceState{name: "my-func", branch: "main", commit: "abc123"}

		It("should leave deploy condition unknown when deployment is nil", func() {
			state := &reconcileState{source: baseSource}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		})

		It("should mark deploy not ready on failure", func() {
			state := &reconcileState{
				source: baseSource,
				deployment: &deploymentState{
					failReason:  "DeployFailed",
					failMessage: "timeout",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("DeployFailed"))
		})

		It("should mark deploy not ready when not deployed", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: &deploymentState{deployed: false},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("NotDeployed"))
		})

		It("should mark deploy ready and set deployment fields", func() {
			state := &reconcileState{
				source: baseSource,
				deployment: &deploymentState{
					deployed: true,
					deployer: "knative",
					runtime:  "go",
					image:    "my-image:v1",
					ready:    "true",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			Expect(function.Status.Deployment.Deployer).To(Equal("knative"))
			Expect(function.Status.Deployment.Runtime).To(Equal("go"))
			Expect(function.Status.Deployment.Image).To(Equal("my-image:v1"))

			serviceCond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeServiceReady)
			Expect(serviceCond).NotTo(BeNil())
			Expect(serviceCond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("should mark service not ready when ready is false", func() {
			state := &reconcileState{
				source: baseSource,
				deployment: &deploymentState{
					deployed: true,
					ready:    "false",
				},
			}
			syncStatus(function, state)

			serviceCond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeServiceReady)
			Expect(serviceCond).NotTo(BeNil())
			Expect(serviceCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(serviceCond.Reason).To(Equal("ServiceNotReady"))
		})

		It("should mark service unknown when ready is empty", func() {
			state := &reconcileState{
				source: baseSource,
				deployment: &deploymentState{
					deployed: true,
					ready:    "",
				},
			}
			syncStatus(function, state)

			serviceCond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeServiceReady)
			Expect(serviceCond).NotTo(BeNil())
			Expect(serviceCond.Status).To(Equal(metav1.ConditionFalse))
			Expect(serviceCond.Reason).To(Equal("ServiceReadyUnknown"))
		})
	})

	Context("middleware phase", func() {
		baseSource := &sourceState{name: "my-func", branch: "main", commit: "abc123"}
		baseDeployment := &deploymentState{deployed: true, ready: "true"}

		It("should leave middleware condition unknown when middleware is nil", func() {
			state := &reconcileState{source: baseSource, deployment: baseDeployment}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionUnknown))
		})

		It("should mark middleware not up to date on failure", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					failReason:  "MiddlewareCheckFailed",
					failMessage: "config error",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("MiddlewareCheckFailed"))
		})

		It("should set pendingRebuild even when failReason is set (mid-deploy flush)", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					pendingRebuild: true,
					currentVersion: "v1.0.0",
					latestVersion:  "v2.0.0",
					updateEnabled:  true,
					updateSource:   "operator",
					failReason:     "MiddlewareOutdated",
					failMessage:    "Middleware is outdated (v2.0.0 available), redeploying...",
				},
			}
			syncStatus(function, state)

			Expect(function.Status.Middleware.PendingRebuild).To(BeTrue())
			Expect(function.Status.Middleware.Current).To(Equal("v1.0.0"))
			Expect(function.Status.Middleware.AutoUpdate.Enabled).To(BeTrue())

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Reason).To(Equal("MiddlewareOutdated"))
		})

		It("should mark middleware up to date when latest", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					isLatest:       true,
					currentVersion: "v2.0.0",
					updateEnabled:  true,
					updateSource:   "function",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			Expect(function.Status.Middleware.Current).To(Equal("v2.0.0"))
			Expect(function.Status.Middleware.AutoUpdate.Enabled).To(BeTrue())
			Expect(function.Status.Middleware.AutoUpdate.Source).To(Equal("function"))
		})

		It("should mark middleware intentionally skipped when update disabled", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					isLatest:       false,
					updateEnabled:  false,
					updateSource:   "operator",
					latestVersion:  "v2.0.0",
					currentVersion: "v1.0.0",
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cond.Reason).To(Equal("SkipMiddlewareUpdate"))

			Expect(*function.Status.Middleware.Available).To(Equal("v2.0.0"))
		})

		It("should mark middleware up to date after successful redeploy", func() {
			now := metav1.Now()
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					isLatest:       false,
					updateEnabled:  true,
					redeployed:     true,
					lastRebuild:    now,
					currentVersion: "v2.0.0",
					latestVersion:  "v2.0.0",
					historyMessage: `Middleware updated from "v1.0.0" to "v2.0.0"`,
				},
			}
			syncStatus(function, state)

			cond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeMiddlewareUpToDate)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))

			deployCond := meta.FindStatusCondition(function.Status.Conditions, v1alpha1.TypeDeployed)
			Expect(deployCond.Status).To(Equal(metav1.ConditionTrue))

			Expect(function.Status.Middleware.Available).To(BeNil())
			Expect(function.Status.Middleware.LastRebuild).To(Equal(now))
			Expect(function.Status.Deployment.ImageBuilt).To(Equal(now))

			Expect(function.Status.History).To(HaveLen(1))
			Expect(function.Status.History[0].Message).To(Equal(`Middleware updated from "v1.0.0" to "v2.0.0"`))
		})

		It("should not record history when no history message", func() {
			state := &reconcileState{
				source:     baseSource,
				deployment: baseDeployment,
				middleware: &middlewareState{
					isLatest:      true,
					updateEnabled: true,
				},
			}
			syncStatus(function, state)

			Expect(function.Status.History).To(BeEmpty())
		})
	})
})
