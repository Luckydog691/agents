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

package sandbox

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/controller/sandbox/core"
)

func TestResolveControlName(t *testing.T) {
	tests := []struct {
		name     string
		pod      *corev1.Pod
		expected string
	}{
		{
			name:     "nil pod returns common",
			pod:      nil,
			expected: core.CommonControlName,
		},
		{
			name: "nil runtimeClassName returns runc",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{},
			},
			expected: core.RuncControlName,
		},
		{
			name: "empty runtimeClassName returns runc",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					RuntimeClassName: ptr.To(""),
				},
			},
			expected: core.RuncControlName,
		},
		{
			name: "runc runtimeClassName returns runc",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					RuntimeClassName: ptr.To("runc"),
				},
			},
			expected: core.RuncControlName,
		},
		{
			name: "runsc runtimeClassName returns common (not runc)",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					RuntimeClassName: ptr.To("runsc"),
				},
			},
			expected: core.CommonControlName,
		},
		{
			name: "gvisor runtimeClassName returns common",
			pod: &corev1.Pod{
				Spec: corev1.PodSpec{
					RuntimeClassName: ptr.To("gvisor"),
				},
			},
			expected: core.CommonControlName,
		},
		{
			name: "annotation override runc",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						agentsv1alpha1.AnnotationSandboxRuntime: "runc",
					},
				},
				Spec: corev1.PodSpec{
					RuntimeClassName: ptr.To("runsc"), // annotation takes priority
				},
			},
			expected: core.RuncControlName,
		},
		{
			name: "annotation override non-runc",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						agentsv1alpha1.AnnotationSandboxRuntime: "runsc",
					},
				},
				Spec: corev1.PodSpec{}, // would normally be runc (nil)
			},
			expected: core.CommonControlName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveControlName(tt.pod)
			if got != tt.expected {
				t.Errorf("resolveControlName() = %q, want %q", got, tt.expected)
			}
		})
	}
}
