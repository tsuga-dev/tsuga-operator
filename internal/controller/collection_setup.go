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
	"fmt"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const clusterConfigName = "cluster"

// SetupCollectionControllers wires the telemetry-collection controllers
// (TsugaCollectorConfig, TsugaPostgresMonitoring and TsugaMonitoring), which
// translate Tsuga CRs into the OpenTelemetry Operator's
// OpenTelemetryCollector/Instrumentation resources.
//
// Those CRDs only exist when the OpenTelemetry Operator is installed. So the
// operator can run on Dashboard/Monitor-only clusters without that dependency,
// each controller is set up only when its CRD is served by the API server;
// otherwise it is skipped with a warning instead of crash-looping on a cache
// sync that can never succeed. Any other discovery error is returned so the
// operator fails visibly rather than silently running without collection.
func SetupCollectionControllers(mgr ctrl.Manager, log logr.Logger) error {
	served := map[schema.GroupVersionKind]bool{}
	for _, gvk := range []schema.GroupVersionKind{collectorGVK, targetAllocatorGVK, instrumentationGVK} {
		ok, err := crdServed(mgr.GetRESTMapper(), gvk)
		if err != nil {
			return err
		}
		served[gvk] = ok
	}

	// TsugaCollectorConfig owns and prunes TargetAllocators too, so both CRDs
	// must be served or its cache never syncs.
	if served[collectorGVK] && served[targetAllocatorGVK] {
		if err := (&TsugaCollectorConfigReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			return err
		}
	} else {
		log.Info("OpenTelemetryCollector or TargetAllocator CRD not found; disabling the TsugaCollectorConfig controller. Install the OpenTelemetry Operator to enable telemetry collection (see README).")
	}

	if served[collectorGVK] {
		if err := (&TsugaPostgresMonitoringReconciler{
			Client:    mgr.GetClient(),
			APIReader: mgr.GetAPIReader(),
			Scheme:    mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			return err
		}
	} else {
		log.Info("OpenTelemetryCollector CRD not found; disabling the TsugaPostgresMonitoring controller. Install the OpenTelemetry Operator to enable telemetry collection (see README).")
	}

	if served[instrumentationGVK] {
		if err := (&TsugaMonitoringReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
		}).SetupWithManager(mgr); err != nil {
			return err
		}
	} else {
		log.Info("Instrumentation CRD not found; disabling TsugaMonitoring controller. Install the OpenTelemetry Operator to enable auto-instrumentation (see README).")
	}

	return nil
}

// crdServed reports whether the API server serves the given GVK. It queries the
// manager's RESTMapper (backed by discovery), so it reflects CRDs installed
// before the operator started. Only a no-match error means "not installed";
// anything else (timeout, 403, API server blip) is returned as an error.
func crdServed(mapper meta.RESTMapper, gvk schema.GroupVersionKind) (bool, error) {
	_, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	switch {
	case err == nil:
		return true, nil
	case meta.IsNoMatchError(err):
		return false, nil
	default:
		return false, fmt.Errorf("discovering %s: %w", gvk, err)
	}
}

// applyControlled server-side applies obj with owner as its controller. An
// existing object owner does not control is refused: ForceOwnership would
// otherwise adopt it, and pruning or garbage collection would later delete it.
func applyControlled(ctx context.Context, c client.Client, reader client.Reader, scheme *runtime.Scheme, owner, obj client.Object) error {
	existing := obj.DeepCopyObject().(client.Object)
	err := reader.Get(ctx, client.ObjectKeyFromObject(obj), existing)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("checking existing %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	case !metav1.IsControlledBy(existing, owner):
		gvk, _ := apiutil.GVKForObject(obj, scheme)
		ownerGVK, _ := apiutil.GVKForObject(owner, scheme)
		return fmt.Errorf("%s %s/%s already exists without a controller reference to %s %s",
			gvk.Kind, obj.GetNamespace(), obj.GetName(), ownerGVK.Kind, owner.GetName())
	}
	if err := controllerutil.SetControllerReference(owner, obj, scheme); err != nil {
		return err
	}
	return c.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership)
}
