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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	functionsdevv1alpha1 "github.com/creydr/func-operator/api/v1alpha1"
)

var _ = Describe("Function Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		function := &functionsdevv1alpha1.Function{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Function")
			err := k8sClient.Get(ctx, typeNamespacedName, function)
			if err != nil && errors.IsNotFound(err) {
				resource := &functionsdevv1alpha1.Function{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: functionsdevv1alpha1.FunctionSpec{
						Source: functionsdevv1alpha1.FunctionSpecSource{
							RepositoryURL: "https://github.com/foo/bar",
						},
						Registry: functionsdevv1alpha1.FunctionSpecRegistry{
							Path: "quay.io/foo/bar",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &functionsdevv1alpha1.Function{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Function")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			funcCliManagerMock := funccli.NewMockManager(GinkgoT())
			funcCliManagerMock.EXPECT().GetMiddlewareVersion(mock.Anything, mock.Anything, mock.Anything).Return("v1.0.0", nil)
			funcCliManagerMock.EXPECT().GetLatestMiddlewareVersion(mock.Anything, mock.Anything, mock.Anything).Return("v1.0.0", nil)
			funcCliManagerMock.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything).Return("", nil)

			gitManagerMock := git.NewMockManager(GinkgoT())
			gitManagerMock.EXPECT().CloneRepository(mock.Anything, "https://github.com/foo/bar", "main").Return(&git.Repository{CloneDir: "testdata/foo-bar"}, nil)

			controllerReconciler := &FunctionReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				Recorder:       &record.FakeRecorder{},
				FuncCliManager: funcCliManagerMock,
				GitManager:     gitManagerMock,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
