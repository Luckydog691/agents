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
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
	"github.com/openkruise/agents/pkg/agent-runtime/storages"
	"github.com/openkruise/agents/pkg/utils"
)

const RuncControlName = "runc"

// runcControl implements SandboxControl for runc runtime.
// It uses in-place container freeze/thaw via ctr task pause/resume,
// driven by annotations on the Pod and conditions reported by the Agent DaemonSet.
type runcControl struct {
	client.Client
	recorder    record.EventRecorder
	podControl  *PodControl
	rateLimiter *RateLimiter
	initializer SandboxInitializer
	common      SandboxControl
}

// NewRuncControl creates a new runcControl instance.
func NewRuncControl(args SandboxControlArgs, common SandboxControl) SandboxControl {
	return &runcControl{
		Client:      args.Client,
		recorder:    args.Recorder,
		podControl:  args.PodControl,
		rateLimiter: args.RateLimiter,
		initializer: &defaultSandboxInitializer{
			client:          args.Client,
			apiReader:       args.APIReader,
			storageRegistry: storages.NewStorageProvider(),
		},
		common: common,
	}
}

// EnsureSandboxRunning delegates to commonControl.
func (r *runcControl) EnsureSandboxRunning(ctx context.Context, args EnsureFuncArgs) (time.Duration, error) {
	return r.common.EnsureSandboxRunning(ctx, args)
}

// EnsureSandboxUpdated delegates to commonControl.
func (r *runcControl) EnsureSandboxUpdated(ctx context.Context, args EnsureFuncArgs) error {
	return r.common.EnsureSandboxUpdated(ctx, args)
}

// EnsureSandboxPaused implements in-place container freeze for runc runtime.
// Flow:
//  1. Initialize PausedCondition (False/Pausing)
//  2. Extract container IDs from pod.Status.ContainerStatuses
//  3. Patch runc-pause annotation on Pod (triggers Agent DaemonSet)
//  4. Poll Pod Condition ContainersPaused=True → Sandbox Paused
func (r *runcControl) EnsureSandboxPaused(ctx context.Context, args EnsureFuncArgs) error {
	pod, box, newStatus := args.Pod, args.Box, args.NewStatus

	// Initialize paused condition
	cond := utils.GetSandboxCondition(newStatus, string(agentsv1alpha1.SandboxConditionPaused))
	if cond == nil {
		cond = &metav1.Condition{
			Type:               string(agentsv1alpha1.SandboxConditionPaused),
			Status:             metav1.ConditionFalse,
			Reason:             agentsv1alpha1.SandboxPausedReasonPausing,
			LastTransitionTime: metav1.Now(),
		}
		utils.SetSandboxCondition(newStatus, *cond)
		klog.InfoS("Runc paused condition initialized", "sandbox", klog.KObj(box))
	} else if cond.Status == metav1.ConditionTrue {
		klog.InfoS("Runc paused condition is already true", "sandbox", klog.KObj(box))
		return nil
	}

	// Set Ready=False during pause
	if rCond := utils.GetSandboxCondition(newStatus, string(agentsv1alpha1.SandboxConditionReady)); rCond != nil && rCond.Status == metav1.ConditionTrue {
		rCond.Status = metav1.ConditionFalse
		rCond.LastTransitionTime = metav1.Now()
		utils.SetSandboxCondition(newStatus, *rCond)
	}

	// Pod must exist for runc pause
	if pod == nil {
		klog.InfoS("Pod not found for runc pause, falling back", "sandbox", klog.KObj(box))
		cond.Status = metav1.ConditionTrue
		cond.Reason = agentsv1alpha1.SandboxPausedReasonSetPause
		cond.LastTransitionTime = metav1.Now()
		utils.SetSandboxCondition(newStatus, *cond)
		return nil
	}

	// Check if Agent has already reported ContainersPaused condition on Pod
	podPausedCond := utils.GetPodCondition(&pod.Status, corev1.PodConditionType(PodConditionContainersPaused))
	if podPausedCond != nil && podPausedCond.Status == corev1.ConditionTrue {
		// Containers are paused — complete the pause
		cond.Status = metav1.ConditionTrue
		cond.Reason = agentsv1alpha1.SandboxPausedReasonSetPause
		cond.LastTransitionTime = metav1.Now()
		utils.SetSandboxCondition(newStatus, *cond)
		klog.InfoS("Runc containers paused successfully", "sandbox", klog.KObj(box))
		// Clean up the pause annotation
		if err := r.removeAnnotation(ctx, pod, agentsv1alpha1.AnnotationRuncPause); err != nil {
			klog.ErrorS(err, "Failed to clean up runc-pause annotation", "sandbox", klog.KObj(box))
		}
		return nil
	}

	// Check if Agent reported a failure
	if podPausedCond != nil && podPausedCond.Status == corev1.ConditionFalse && podPausedCond.Reason == "RuncError" {
		cond.Message = podPausedCond.Message
		utils.SetSandboxCondition(newStatus, *cond)
		r.recorder.Event(box, corev1.EventTypeWarning, "RuncPauseFailed", podPausedCond.Message)
		klog.ErrorS(nil, "Agent reported runc pause failure", "sandbox", klog.KObj(box), "message", podPausedCond.Message)
		return fmt.Errorf("runc pause failed: %s", podPausedCond.Message)
	}

	// Check if annotation is already set (waiting for Agent)
	if _, exists := pod.Annotations[agentsv1alpha1.AnnotationRuncPause]; exists {
		klog.InfoS("Waiting for Agent to pause containers", "sandbox", klog.KObj(box))
		return nil
	}

	// Extract container IDs and patch the annotation
	containerIDs, err := extractAppContainerIDs(pod)
	if err != nil {
		klog.ErrorS(err, "Failed to extract container IDs", "sandbox", klog.KObj(box))
		return err
	}

	idsJSON, err := json.Marshal(containerIDs)
	if err != nil {
		return fmt.Errorf("failed to marshal container IDs: %w", err)
	}

	if err := r.patchAnnotation(ctx, pod, agentsv1alpha1.AnnotationRuncPause, string(idsJSON)); err != nil {
		klog.ErrorS(err, "Failed to patch runc-pause annotation", "sandbox", klog.KObj(box))
		return err
	}
	klog.InfoS("Patched runc-pause annotation", "sandbox", klog.KObj(box), "containerIDs", containerIDs)
	return nil
}

// EnsureSandboxResumed implements in-place container resume for runc runtime.
func (r *runcControl) EnsureSandboxResumed(ctx context.Context, args EnsureFuncArgs) error {
	pod, box, newStatus := args.Pod, args.Box, args.NewStatus

	// If pod doesn't exist, fall back to common control (recreate)
	if pod == nil {
		return r.common.EnsureSandboxResumed(ctx, args)
	}

	// Pod is in terminating state, wait
	if !pod.DeletionTimestamp.IsZero() {
		return fmt.Errorf("the pod is in terminating state, waiting")
	}

	// Check if Agent has already reported ContainersResumed condition
	podResumedCond := utils.GetPodCondition(&pod.Status, corev1.PodConditionType(PodConditionContainersResumed))
	if podResumedCond != nil && podResumedCond.Status == corev1.ConditionTrue {
		// Containers are resumed
		if err := r.removeAnnotation(ctx, pod, agentsv1alpha1.AnnotationRuncResume); err != nil {
			klog.ErrorS(err, "Failed to clean up runc-resume annotation", "sandbox", klog.KObj(box))
		}

		// Set resumed condition
		if resumedCond := utils.GetSandboxCondition(newStatus, string(agentsv1alpha1.SandboxConditionResumed)); resumedCond != nil && resumedCond.Status == metav1.ConditionFalse {
			resumedCond.Status = metav1.ConditionTrue
			resumedCond.LastTransitionTime = metav1.Now()
			utils.SetSandboxCondition(newStatus, *resumedCond)
		}

		// When pod is ready, transition to Running
		pCond := utils.GetPodCondition(&pod.Status, corev1.PodReady)
		if pod.Status.Phase == corev1.PodRunning && pCond != nil && pCond.Status == corev1.ConditionTrue {
			newStatus.Phase = agentsv1alpha1.SandboxRunning
			newStatus.NodeName = pod.Spec.NodeName
			newStatus.SandboxIp = pod.Status.PodIP
			newStatus.PodInfo = agentsv1alpha1.PodInfo{
				PodIP:    pod.Status.PodIP,
				NodeName: pod.Spec.NodeName,
				PodUID:   pod.UID,
			}

			// Set Ready condition
			rCond := utils.GetSandboxCondition(newStatus, string(agentsv1alpha1.SandboxConditionReady))
			if rCond != nil {
				rCond.Status = metav1.ConditionTrue
				rCond.Reason = agentsv1alpha1.SandboxReadyReasonPodReady
				rCond.LastTransitionTime = metav1.Now()
				utils.SetSandboxCondition(newStatus, *rCond)
			}
			klog.InfoS("Runc containers resumed successfully, sandbox running", "sandbox", klog.KObj(box))
		}
		return nil
	}

	// Check if Agent reported a failure
	if podResumedCond != nil && podResumedCond.Status == corev1.ConditionFalse && podResumedCond.Reason == "RuncError" {
		r.recorder.Event(box, corev1.EventTypeWarning, "RuncResumeFailed", podResumedCond.Message)
		return fmt.Errorf("runc resume failed: %s", podResumedCond.Message)
	}

	// Check if annotation is already set (waiting for Agent)
	if _, exists := pod.Annotations[agentsv1alpha1.AnnotationRuncResume]; exists {
		klog.InfoS("Waiting for Agent to resume containers", "sandbox", klog.KObj(box))
		return nil
	}

	// Extract container IDs and patch the annotation
	containerIDs, err := extractAppContainerIDs(pod)
	if err != nil {
		klog.ErrorS(err, "Failed to extract container IDs for resume", "sandbox", klog.KObj(box))
		return err
	}

	idsJSON, err := json.Marshal(containerIDs)
	if err != nil {
		return fmt.Errorf("failed to marshal container IDs: %w", err)
	}

	if err := r.patchAnnotation(ctx, pod, agentsv1alpha1.AnnotationRuncResume, string(idsJSON)); err != nil {
		klog.ErrorS(err, "Failed to patch runc-resume annotation", "sandbox", klog.KObj(box))
		return err
	}
	klog.InfoS("Patched runc-resume annotation", "sandbox", klog.KObj(box), "containerIDs", containerIDs)
	return nil
}

// EnsureSandboxUpgraded delegates to commonControl.
func (r *runcControl) EnsureSandboxUpgraded(ctx context.Context, args EnsureFuncArgs) error {
	return r.common.EnsureSandboxUpgraded(ctx, args)
}

// EnsureSandboxTerminated delegates to commonControl.
func (r *runcControl) EnsureSandboxTerminated(ctx context.Context, args EnsureFuncArgs) error {
	return r.common.EnsureSandboxTerminated(ctx, args)
}

// extractAppContainerIDs extracts application container IDs from pod status.
// It returns the container IDs with the "containerd://" prefix stripped.
func extractAppContainerIDs(pod *corev1.Pod) ([]string, error) {
	if pod == nil {
		return nil, fmt.Errorf("pod is nil")
	}

	var ids []string
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.ContainerID == "" {
			return nil, fmt.Errorf("container %s has no containerID (not yet started)", cs.Name)
		}
		id := strings.TrimPrefix(cs.ContainerID, "containerd://")
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("no application container IDs found in pod %s/%s", pod.Namespace, pod.Name)
	}
	return ids, nil
}

// patchAnnotation adds or updates an annotation on the pod.
func (r *runcControl) patchAnnotation(ctx context.Context, pod *corev1.Pod, key, value string) error {
	podCopy := pod.DeepCopy()
	patch := client.MergeFrom(pod)
	if podCopy.Annotations == nil {
		podCopy.Annotations = make(map[string]string)
	}
	podCopy.Annotations[key] = value
	return r.Patch(ctx, podCopy, patch)
}

// removeAnnotation removes an annotation from the pod.
func (r *runcControl) removeAnnotation(ctx context.Context, pod *corev1.Pod, key string) error {
	if _, exists := pod.Annotations[key]; !exists {
		return nil
	}
	podCopy := pod.DeepCopy()
	patch := client.MergeFrom(pod)
	delete(podCopy.Annotations, key)
	return r.Patch(ctx, podCopy, patch)
}
