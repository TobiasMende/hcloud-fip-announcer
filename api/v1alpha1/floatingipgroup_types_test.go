/*
Copyright 2026 Tobias Mende.

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

package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

const testLocation = "fsn1"

var k8sClient client.Client

func TestMain(m *testing.M) {
	if err := AddToScheme(scheme.Scheme); err != nil {
		panic(err)
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	if dir := firstEnvtestBinaryDir(); dir != "" && os.Getenv("KUBEBUILDER_ASSETS") == "" {
		env.BinaryAssetsDirectory = dir
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = env.Stop()
	os.Exit(code)
}

func firstEnvtestBinaryDir() string {
	base := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(base)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			return filepath.Join(base, e.Name())
		}
	}
	return ""
}

func newGroup(name string, spec FloatingIPGroupSpec) *FloatingIPGroup {
	return &FloatingIPGroup{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
}

func TestRejectsGroupWithoutAddresses(t *testing.T) {
	g := newGroup("no-addresses", FloatingIPGroupSpec{Location: testLocation})
	if err := k8sClient.Create(context.Background(), g); !apierrors.IsInvalid(err) {
		t.Fatalf("expected group without ipv4 and ipv6 to be rejected as invalid, got %v", err)
	}
}

func TestAcceptsIPv4OnlyAndIPv6Only(t *testing.T) {
	v4 := newGroup("v4-only", FloatingIPGroupSpec{Location: testLocation, IPv4: &FloatingIPRef{Name: "a"}})
	if err := k8sClient.Create(context.Background(), v4); err != nil {
		t.Fatalf("ipv4-only: %v", err)
	}
	v6 := newGroup("v6-only", FloatingIPGroupSpec{Location: testLocation, IPv6: &FloatingIPRef{Name: "b"}})
	if err := k8sClient.Create(context.Background(), v6); err != nil {
		t.Fatalf("ipv6-only: %v", err)
	}
	if v6.Spec.IPv6.IPv6Address != "::1" {
		t.Fatalf("ipv6Address default = %q, want ::1", v6.Spec.IPv6.IPv6Address)
	}
}

func TestDefaultsDeletionPolicyRetain(t *testing.T) {
	g := newGroup("defaults", FloatingIPGroupSpec{Location: testLocation, IPv4: &FloatingIPRef{Name: "a"}})
	if err := k8sClient.Create(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if g.Spec.DeletionPolicy != DeletionPolicyRetain {
		t.Fatalf("deletionPolicy = %q, want Retain", g.Spec.DeletionPolicy)
	}
}

func TestRejectsUnknownDeletionPolicy(t *testing.T) {
	g := newGroup("bad-policy", FloatingIPGroupSpec{
		Location:       testLocation,
		IPv4:           &FloatingIPRef{Name: "a"},
		DeletionPolicy: DeletionPolicy("Purge"),
	})
	if err := k8sClient.Create(context.Background(), g); !apierrors.IsInvalid(err) {
		t.Fatalf("expected unknown deletionPolicy to be rejected as invalid, got %v", err)
	}
}

func TestRejectsNameLongerThan63(t *testing.T) {
	ok := newGroup(strings.Repeat("a", 63), FloatingIPGroupSpec{Location: testLocation, IPv4: &FloatingIPRef{Name: "a"}})
	if err := k8sClient.Create(context.Background(), ok); err != nil {
		t.Fatalf("63 characters: %v", err)
	}
	long := newGroup(strings.Repeat("b", 64), FloatingIPGroupSpec{Location: testLocation, IPv4: &FloatingIPRef{Name: "a"}})
	if err := k8sClient.Create(context.Background(), long); !apierrors.IsInvalid(err) {
		t.Fatalf("expected a 64-character name to be rejected as invalid, got %v", err)
	}
}

func TestLocationRequired(t *testing.T) {
	g := newGroup("no-location", FloatingIPGroupSpec{IPv4: &FloatingIPRef{Name: "a"}})
	if err := k8sClient.Create(context.Background(), g); !apierrors.IsInvalid(err) {
		t.Fatalf("expected empty location to be rejected as invalid, got %v", err)
	}
}

func TestIPv6AddressMustBeIPv6(t *testing.T) {
	for addr, valid := range map[string]bool{"::1": true, "::abcd:1": true, "203.0.113.1": false, "not-an-ip": false} {
		name := "v6addr-" + strings.NewReplacer(":", "c", ".", "d").Replace(addr)
		g := newGroup(name, FloatingIPGroupSpec{Location: testLocation, IPv6: &FloatingIPRef{Name: "b", IPv6Address: addr}})
		err := k8sClient.Create(context.Background(), g)
		if valid && err != nil {
			t.Errorf("%s rejected: %v", addr, err)
		}
		if !valid && !apierrors.IsInvalid(err) {
			t.Errorf("%s: want invalid, got %v", addr, err)
		}
	}
}
