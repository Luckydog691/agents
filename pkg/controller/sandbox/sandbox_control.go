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

package sandbox

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/controller/sandbox/core"
	"github.com/openkruise/agents/pkg/features"
	utilfeature "github.com/openkruise/agents/pkg/utils/feature"
)

func (r *SandboxReconciler) getControl(pod *corev1.Pod) core.SandboxControl {
	if !utilfeature.DefaultFeatureGate.Enabled(features.RuncPauseResumeGate) {
		return r.controls[core.CommonControlName]
	}
	name := resolveControlName(pod)
	if ctrl, ok := r.controls[name]; ok {
		return ctrl
	}
	return r.controls[core.CommonControlName]
}

// resolveControlName determines which SandboxControl to use based on the Pod's
// runtime class or explicit annotation override.
func resolveControlName(pod *corev1.Pod) string {
	if pod == nil {
		return core.CommonControlName
	}

	// 1. Annotation override takes priority
	if ann, ok := pod.Annotations[agentsv1alpha1.AnnotationSandboxRuntime]; ok && ann != "" {
		if ann == "runc" {
			return core.RuncControlName
		}
		return core.CommonControlName
	}

	// 2. RuntimeClassName-based routing
	// nil or empty runtimeClassName means the default runtime (runc)
	rcn := pod.Spec.RuntimeClassName
	if rcn == nil || *rcn == "" {
		return core.RuncControlName
	}
	if strings.Contains(strings.ToLower(*rcn), "runc") {
		return core.RuncControlName
	}

	return core.CommonControlName
}
