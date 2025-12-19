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

	"github.com/creydr/func-operator/internal/funccli"
	"github.com/creydr/func-operator/internal/git"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"knative.dev/func/pkg/functions"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	functionsdevv1alpha1 "github.com/creydr/func-operator/api/v1alpha1"
)

var _ = Describe("Function Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const resourceNamespace = "default"
		const functionName = "func-go"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: resourceNamespace,
		}

		defaultSpec := functionsdevv1alpha1.FunctionSpec{
			Source: functionsdevv1alpha1.FunctionSpecSource{
				RepositoryURL: "https://github.com/foo/bar",
				Reference:     "my-branch",
			},
			Registry: functionsdevv1alpha1.FunctionSpecRegistry{
				Path: "quay.io/foo/bar",
			},
		}

		AfterEach(func() {
			resource := &functionsdevv1alpha1.Function{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Function")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		type reconcileTestCase struct {
			spec           functionsdevv1alpha1.FunctionSpec
			configureMocks func(*funccli.MockManager, *git.MockManager)
		}

		DescribeTable("should successfully reconcile the resource",
			func(tc reconcileTestCase) {
				By("creating the Function")
				err := createFunctionResource(resourceName, resourceNamespace, tc.spec)
				Expect(err).NotTo(HaveOccurred())

				By("Setting up mocks")
				funcCliManagerMock := funccli.NewMockManager(GinkgoT())
				gitManagerMock := git.NewMockManager(GinkgoT())
				tc.configureMocks(funcCliManagerMock, gitManagerMock)

				By("Reconciling the created resource")
				controllerReconciler := &FunctionReconciler{
					Client:         k8sClient,
					Scheme:         k8sClient.Scheme(),
					Recorder:       &record.FakeRecorder{},
					FuncCliManager: funcCliManagerMock,
					GitManager:     gitManagerMock,
				}

				_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
					NamespacedName: typeNamespacedName,
				})
				Expect(err).NotTo(HaveOccurred())
			},
			Entry("should deploy when middleware update required", reconcileTestCase{
				spec: defaultSpec,
				configureMocks: func(funcMock *funccli.MockManager, gitMock *git.MockManager) {
					funcMock.EXPECT().Describe(mock.Anything, functionName, resourceNamespace).Return(functions.Instance{
						Middleware: functions.Middleware{
							Version: "v1.0.0",
						},
					}, nil)
					funcMock.EXPECT().GetLatestMiddlewareVersion(mock.Anything, mock.Anything, mock.Anything).Return("v2.0.0", nil)
					funcMock.EXPECT().GetMiddlewareVersion(mock.Anything, functionName, resourceNamespace).Return("v1.0.0", nil)
					funcMock.EXPECT().Deploy(mock.Anything, mock.Anything, resourceNamespace, funccli.DeployOptions{
						Registry: "quay.io/foo/bar",
						GitUrl:   "https://github.com/foo/bar",
						Builder:  "s2i",
					}).Return(nil)

					gitMock.EXPECT().CloneRepository(mock.Anything, "https://github.com/foo/bar", "my-branch").Return(&git.Repository{CloneDir: "testdata/foo-bar"}, nil)
				},
			}),
			Entry("should skip deploy when middleware already up to date", reconcileTestCase{
				spec: defaultSpec,
				configureMocks: func(funcMock *funccli.MockManager, gitMock *git.MockManager) {
					funcMock.EXPECT().Describe(mock.Anything, functionName, resourceNamespace).Return(functions.Instance{
						Middleware: functions.Middleware{
							Version: "v1.0.0",
						},
					}, nil)
					funcMock.EXPECT().GetLatestMiddlewareVersion(mock.Anything, mock.Anything, mock.Anything).Return("v1.0.0", nil)
					funcMock.EXPECT().GetMiddlewareVersion(mock.Anything, functionName, resourceNamespace).Return("v1.0.0", nil)

					gitMock.EXPECT().CloneRepository(mock.Anything, "https://github.com/foo/bar", "my-branch").Return(&git.Repository{CloneDir: "testdata/foo-bar"}, nil)
				},
			}),
			Entry("should use main as default branch", reconcileTestCase{
				spec: functionsdevv1alpha1.FunctionSpec{
					Source: functionsdevv1alpha1.FunctionSpecSource{
						RepositoryURL: "https://github.com/foo/bar",
					},
					Registry: functionsdevv1alpha1.FunctionSpecRegistry{
						Path: "quay.io/foo/bar",
					},
				},
				configureMocks: func(funcMock *funccli.MockManager, gitMock *git.MockManager) {
					funcMock.EXPECT().Describe(mock.Anything, functionName, resourceNamespace).Return(functions.Instance{
						Middleware: functions.Middleware{
							Version: "v1.0.0",
						},
					}, nil)
					funcMock.EXPECT().GetLatestMiddlewareVersion(mock.Anything, mock.Anything, mock.Anything).Return("v1.0.0", nil)
					funcMock.EXPECT().GetMiddlewareVersion(mock.Anything, functionName, resourceNamespace).Return("v1.0.0", nil)

					gitMock.EXPECT().CloneRepository(mock.Anything, "https://github.com/foo/bar", "main").Return(&git.Repository{CloneDir: "testdata/foo-bar"}, nil)
				},
			}),
		)
	})
})

func createFunctionResource(name, namespace string, spec functionsdevv1alpha1.FunctionSpec) error {
	resource := functionsdevv1alpha1.Function{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: spec,
	}

	return k8sClient.Create(ctx, &resource)
}
