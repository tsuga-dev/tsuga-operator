package controller

import (
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type failingMapper struct{ meta.RESTMapper }

func (failingMapper) RESTMapping(schema.GroupKind, ...string) (*meta.RESTMapping, error) {
	return nil, errors.New("the server is currently unable to handle the request")
}

func TestCRDServed(t *testing.T) {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(collectorGVK, meta.RESTScopeNamespace)

	if ok, err := crdServed(mapper, collectorGVK); !ok || err != nil {
		t.Errorf("served CRD: got (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := crdServed(mapper, instrumentationGVK); ok || err != nil {
		t.Errorf("missing CRD: got (%v, %v), want (false, nil)", ok, err)
	}
	if ok, err := crdServed(failingMapper{}, collectorGVK); ok || err == nil {
		t.Errorf("discovery error: got (%v, %v), want (false, error)", ok, err)
	}
}
