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

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
)

var (
	scheme    *runtime.Scheme
	k8sClient client.Client
	clientset *kubernetes.Clientset
)

var (
	LabelDescribe = "describe"
	LabelIt       = "it"
	Namespace     = "default"
)

// imageOf returns a fully-qualified image reference, prepending the value of
// the E2E_IMAGE_REGISTRY environment variable when it is set.
//
// When E2E_IMAGE_REGISTRY is empty, the original short name is returned
// unchanged (default upstream behaviour, e.g. "nginx:stable-alpine3.23").
//
// When set to e.g. "cr.registry.inter.env149.shuguang.com/acs", the result
// becomes "cr.registry.inter.env149.shuguang.com/acs/nginx:stable-alpine3.23",
// allowing private-cloud environments without dockerhub access to pull the
// images from a local mirror.
//
// References that already include a registry host (i.e. contain "/" before
// the first ":") are returned unchanged so callers can mix prefixed and
// non-prefixed references freely.
func imageOf(shortName string) string {
	prefix := strings.TrimRight(os.Getenv("E2E_IMAGE_REGISTRY"), "/")
	if prefix == "" {
		return shortName
	}
	// Already fully qualified (contains a registry host with a dot before the first slash).
	if slash := strings.Index(shortName, "/"); slash > 0 && strings.ContainsAny(shortName[:slash], ".:") {
		return shortName
	}
	return prefix + "/" + shortName
}

func init() {
	scheme = runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = agentsv1alpha1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)

	cfg := config.GetConfigOrDie()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(fmt.Sprintf("Failed to create client: %v", err))
	}
	k8sClient = c

	// Create clientset for eviction API
	clientset, err = kubernetes.NewForConfig(cfg)
	if err != nil {
		panic(fmt.Sprintf("Failed to create clientset: %v", err))
	}
}

// +kubebuilder:scaffold:e2e-webhooks-checks

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purpose of being used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting agent-sandbox integration test suite\n")

	// Get configuration from command line flags
	suiteConfig, reporterConfig := GinkgoConfiguration()

	RunSpecs(t, "e2e suite", suiteConfig, reporterConfig)
}
