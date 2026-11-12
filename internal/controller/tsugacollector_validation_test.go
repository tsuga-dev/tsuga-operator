package controller

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/tsuga-dev/tsuga-operator/api/v1alpha1"
)

var _ = Describe("TsugaCollector schema validation", func() {
	newCollector := func(name string) *v1alpha1.TsugaCollector {
		return &v1alpha1.TsugaCollector{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
			Spec: v1alpha1.TsugaCollectorSpec{
				Collector: &apiextensionsv1.JSON{Raw: []byte(`{"config":{"receivers":{"otlp":{}},"service":{"pipelines":{"logs":{"receivers":["otlp"]}}}}}`)},
			},
		}
	}

	It("accepts a minimal CR and defaults every toggle to true", func() {
		c := newCollector("col-defaults")
		Expect(k8sClient.Create(context.Background(), c)).To(Succeed())
		Expect(c.Spec.TsugaExporter).To(HaveValue(BeTrue()))
		Expect(c.Spec.SelfTelemetry).To(HaveValue(BeTrue()))
		Expect(c.Spec.DefaultProcessors).To(HaveValue(BeTrue()))
		Expect(k8sClient.Delete(context.Background(), c)).To(Succeed())
	})

	It("rejects a name longer than 36 characters", func() {
		err := k8sClient.Create(context.Background(), newCollector(strings.Repeat("a", 37)))
		Expect(err).To(MatchError(ContainSubstring("at most 36 characters")))
	})

	It("accepts a name at the 36 character limit", func() {
		c := newCollector(strings.Repeat("a", 36))
		Expect(k8sClient.Create(context.Background(), c)).To(Succeed())
		Expect(k8sClient.Delete(context.Background(), c)).To(Succeed())
	})

	It("rejects a CR without spec.collector", func() {
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion("observability.tsuga.com/v1alpha1")
		obj.SetKind("TsugaCollector")
		obj.SetName("col-missing")
		obj.SetNamespace(testNamespace)
		Expect(unstructured.SetNestedField(obj.Object, true, "spec", "tsugaExporter")).To(Succeed())
		err := k8sClient.Create(context.Background(), obj)
		Expect(err).To(MatchError(ContainSubstring("collector")))
	})
})
