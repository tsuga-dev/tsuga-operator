package v1alpha1

import (
	"testing"

	"k8s.io/utils/ptr"
)

func TestEffectiveInjection(t *testing.T) {
	for _, tc := range []struct {
		injection InjectionMode
		existing  *bool
		want      InjectionMode
	}{
		{"", nil, InjectionNamespace},
		{"", ptr.To(true), InjectionWorkloads},
		{"", ptr.To(false), InjectionManual},
		{InjectionManual, ptr.To(true), InjectionManual},
		{InjectionNamespace, ptr.To(false), InjectionNamespace},
	} {
		spec := TsugaMonitoringSpec{Injection: tc.injection, InjectExistingWorkloads: tc.existing}
		if got := spec.EffectiveInjection(); got != tc.want {
			t.Errorf("injection=%q injectExistingWorkloads=%v: got %q, want %q", tc.injection, tc.existing, got, tc.want)
		}
	}
}
