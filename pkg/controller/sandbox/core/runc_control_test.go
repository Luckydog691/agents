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

package core

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExtractAppContainerIDs(t *testing.T) {
	tests := []struct {
		name    string
		pod     *corev1.Pod
		want    []string
		wantErr bool
	}{
		{
			name:    "nil pod",
			pod:     nil,
			wantErr: true,
		},
		{
			name: "no container statuses",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{},
			},
			wantErr: true,
		},
		{
			name: "container without ID",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", ContainerID: ""},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "single container with containerd prefix",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", ContainerID: "containerd://abc123def"},
					},
				},
			},
			want: []string{"abc123def"},
		},
		{
			name: "multiple containers",
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Name: "test-pod", Namespace: "default"},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app1", ContainerID: "containerd://id1"},
						{Name: "app2", ContainerID: "containerd://id2"},
					},
				},
			},
			want: []string{"id1", "id2"},
		},
		{
			name: "container without containerd prefix",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{Name: "app", ContainerID: "docker://xyz789"},
					},
				},
			},
			want: []string{"docker://xyz789"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractAppContainerIDs(tt.pod)
			if tt.wantErr {
				if err == nil {
					t.Errorf("extractAppContainerIDs() expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("extractAppContainerIDs() unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("extractAppContainerIDs() got %d IDs, want %d", len(got), len(tt.want))
			}
			for i, id := range got {
				if id != tt.want[i] {
					t.Errorf("extractAppContainerIDs()[%d] = %q, want %q", i, id, tt.want[i])
				}
			}
		})
	}
}
