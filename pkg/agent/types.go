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

package agent

import (
	"context"

	corev1 "k8s.io/api/core/v1"
)

// Executor defines the interface for executing container pause/resume operations.
// For runc, the real implementation uses nsenter + ctr; tests can use a mock.
type Executor interface {
	// PauseContainers pauses all specified containers. If any container fails to pause,
	// already-paused containers are rolled back (resumed).
	PauseContainers(ctx context.Context, containerIDs []string) error

	// ResumeContainers resumes all specified containers. Unlike pause, resume does not
	// roll back on partial failure (partially resumed is better than fully frozen).
	ResumeContainers(ctx context.Context, containerIDs []string) error
}

// StatusReporter patches Pod status conditions to report operation results
// back to the control plane.
type StatusReporter interface {
	// ReportCondition patches the given condition onto the Pod's status.
	ReportCondition(ctx context.Context, pod *corev1.Pod, conditionType corev1.PodConditionType, status corev1.ConditionStatus, reason, message string) error
}
