/*
Copyright 2026 Tsuga.

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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	tsugav1alpha1 "github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

// noopDashboardClient is a minimal stub that prevents panics in the scaffold integration test.
type noopDashboardClient struct{}

func (n *noopDashboardClient) FindByTag(context.Context, string, string) (string, error) {
	return "", nil
}

func (n *noopDashboardClient) Create(ctx context.Context, payload []byte) (string, error) {
	return "test-id", nil
}
func (n *noopDashboardClient) Update(ctx context.Context, id string, payload []byte) error {
	return nil
}
func (n *noopDashboardClient) Delete(ctx context.Context, id string) error { return nil }

var _ = Describe("Dashboard Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: testNamespace,
		}
		dashboard := &tsugav1alpha1.Dashboard{}
		request := reconcile.Request{NamespacedName: typeNamespacedName}
		var controllerReconciler *TsugaDashboardReconciler

		BeforeEach(func() {
			controllerReconciler = &TsugaDashboardReconciler{
				Client:      k8sClient,
				TsugaClient: &noopDashboardClient{},
			}

			By("creating the custom resource for the Kind Dashboard")
			err := k8sClient.Get(ctx, typeNamespacedName, dashboard)
			if err != nil && errors.IsNotFound(err) {
				resource := &tsugav1alpha1.Dashboard{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: testNamespace,
					},
					Spec: tsugav1alpha1.DashboardSpec{
						Name:  "Dashboard Test",
						Owner: testOwner,
						Graphs: []tsugav1alpha1.DashboardGraph{
							{
								ID: testGraphID,
								Visualization: apiextensionsv1.JSON{
									Raw: []byte(`{"type":"note","note":"hello"}`),
								},
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &tsugav1alpha1.Dashboard{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Dashboard")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			By("Reconciling the delete so the finalizer is removed")
			_, err = controllerReconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())
			err = k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(errors.IsNotFound(err)).To(BeTrue(), "want the Dashboard gone, got %v", err)
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource to add the finalizer")
			_, err := controllerReconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			By("Reconciling again to sync it to Tsuga")
			_, err = controllerReconciler.Reconcile(ctx, request)
			Expect(err).NotTo(HaveOccurred())

			synced := &tsugav1alpha1.Dashboard{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, synced)).To(Succeed())
			Expect(synced.Status.ID).To(Equal("test-id"))
			Expect(synced.Status.Phase).To(Equal(phaseReady))
		})
	})
})
