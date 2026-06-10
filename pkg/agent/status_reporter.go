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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// podStatusReporter implements StatusReporter by patching Pod status via the Kubernetes API.
type podStatusReporter struct {
	clientset kubernetes.Interface
}

// NewStatusReporter creates a StatusReporter that patches Pod conditions.
func NewStatusReporter(clientset kubernetes.Interface) StatusReporter {
	return &podStatusReporter{clientset: clientset}
}

func (r *podStatusReporter) ReportCondition(ctx context.Context, pod *corev1.Pod, conditionType corev1.PodConditionType, status corev1.ConditionStatus, reason, message string) error {
	condition := corev1.PodCondition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}

	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []corev1.PodCondition{condition},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal status patch: %w", err)
	}

	_, err = r.clientset.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name,
		types.StrategicMergePatchType,
		patchBytes,
		metav1.PatchOptions{},
		"status",
	)
	if err != nil {
		klog.ErrorS(err, "failed to patch pod condition", "pod", klog.KObj(pod), "conditionType", conditionType)
		return err
	}
	return nil
}
