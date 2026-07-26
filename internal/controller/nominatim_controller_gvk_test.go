/*
Copyright 2026.

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
	"fmt"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
)

type stubRESTMapper struct {
	mapping *meta.RESTMapping
	err     error
}

func (s *stubRESTMapper) KindFor(_ schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, fmt.Errorf("unimplemented")
}
func (s *stubRESTMapper) KindsFor(_ schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (s *stubRESTMapper) ResourceFor(_ schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, fmt.Errorf("unimplemented")
}
func (s *stubRESTMapper) ResourcesFor(_ schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (s *stubRESTMapper) RESTMapping(_ schema.GroupKind, _ ...string) (*meta.RESTMapping, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.mapping, nil
}
func (s *stubRESTMapper) RESTMappings(_ schema.GroupKind, _ ...string) ([]*meta.RESTMapping, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (s *stubRESTMapper) ResourceSingularizer(_ string) (string, error) {
	return "", fmt.Errorf("unimplemented")
}

func TestGVKAvailableFromMapper(t *testing.T) {
	mapperOK := &stubRESTMapper{mapping: &meta.RESTMapping{GroupVersionKind: HTTPRouteGVK}}
	ok, err := gvkAvailableFromMapper(mapperOK, HTTPRouteGVK)
	if err != nil || !ok {
		t.Fatalf("expected available mapping, ok=%v err=%v", ok, err)
	}

	mapperMissing := &stubRESTMapper{err: &meta.NoKindMatchError{GroupKind: HTTPRouteGVK.GroupKind()}}
	ok, err = gvkAvailableFromMapper(mapperMissing, HTTPRouteGVK)
	if err != nil || ok {
		t.Fatalf("expected unavailable mapping, ok=%v err=%v", ok, err)
	}

	mapperErr := &stubRESTMapper{err: fmt.Errorf("boom")}
	ok, err = gvkAvailableFromMapper(mapperErr, HTTPRouteGVK)
	if err == nil || ok {
		t.Fatalf("expected mapper error to propagate, ok=%v err=%v", ok, err)
	}
}

var _ = Describe("optional GVK registration", func() {
	It("registers HTTPRoute and CNPG watches when the RESTMapper knows them", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		r := &NominatimReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "nominatim-optional-gvks",
		}
		Expect(r.setupWithManager(mgr, alwaysMatchMapper{})).To(Succeed())
	})

	It("propagates RESTMapper errors from optional GVK probes", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		r := &NominatimReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "nominatim-mapper-error",
		}
		Expect(r.setupWithManager(mgr, &stubRESTMapper{err: fmt.Errorf("mapper boom")})).To(HaveOccurred())
	})

	It("propagates RESTMapper errors from the CNPG optional probe", func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		r := &NominatimReconciler{
			Client:         mgr.GetClient(),
			Scheme:         mgr.GetScheme(),
			ControllerName: "nominatim-mapper-error-cnpg",
		}
		Expect(r.setupWithManager(mgr, &failOnCNPGMapper{})).To(HaveOccurred())
	})
})

type failOnCNPGMapper struct{}

func (failOnCNPGMapper) KindFor(_ schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, fmt.Errorf("unimplemented")
}
func (failOnCNPGMapper) KindsFor(_ schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (failOnCNPGMapper) ResourceFor(_ schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, fmt.Errorf("unimplemented")
}
func (failOnCNPGMapper) ResourcesFor(_ schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (failOnCNPGMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if gk.Kind == CNPGClusterGVK.Kind {
		return nil, fmt.Errorf("cnpg mapper boom")
	}
	v := ""
	if len(versions) > 0 {
		v = versions[0]
	}
	return &meta.RESTMapping{
		GroupVersionKind: schema.GroupVersionKind{Group: gk.Group, Version: v, Kind: gk.Kind},
	}, nil
}
func (failOnCNPGMapper) RESTMappings(_ schema.GroupKind, _ ...string) ([]*meta.RESTMapping, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (failOnCNPGMapper) ResourceSingularizer(_ string) (string, error) {
	return "", fmt.Errorf("unimplemented")
}

type alwaysMatchMapper struct{}

func (alwaysMatchMapper) KindFor(_ schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	return schema.GroupVersionKind{}, fmt.Errorf("unimplemented")
}
func (alwaysMatchMapper) KindsFor(_ schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (alwaysMatchMapper) ResourceFor(_ schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	return schema.GroupVersionResource{}, fmt.Errorf("unimplemented")
}
func (alwaysMatchMapper) ResourcesFor(_ schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (alwaysMatchMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	v := ""
	if len(versions) > 0 {
		v = versions[0]
	}
	return &meta.RESTMapping{
		GroupVersionKind: schema.GroupVersionKind{Group: gk.Group, Version: v, Kind: gk.Kind},
	}, nil
}
func (alwaysMatchMapper) RESTMappings(_ schema.GroupKind, _ ...string) ([]*meta.RESTMapping, error) {
	return nil, fmt.Errorf("unimplemented")
}
func (alwaysMatchMapper) ResourceSingularizer(_ string) (string, error) {
	return "", fmt.Errorf("unimplemented")
}
