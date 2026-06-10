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
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
)

const (
	// PodConditionContainersPaused is the Pod condition type set by the Agent
	// to indicate that all containers have been paused via ctr task pause.
	PodConditionContainersPaused corev1.PodConditionType = "ContainersPaused"

	// PodConditionContainersResumed is the Pod condition type set by the Agent
	// to indicate that all containers have been resumed via ctr task resume.
	PodConditionContainersResumed corev1.PodConditionType = "ContainersResumed"

	// PodConditionReasonRuncPaused is the reason when runc pause succeeds.
	PodConditionReasonRuncPaused = "RuncPaused"

	// PodConditionReasonRuncResumed is the reason when runc resume succeeds.
	PodConditionReasonRuncResumed = "RuncResumed"

	// PodConditionReasonRuncError is the reason when an runc operation fails.
	PodConditionReasonRuncError = "RuncError"
)

// PodWatcher watches Pods on the local node via SharedInformer and triggers
// pause/resume operations when runc annotations are detected.
type PodWatcher struct {
	nodeName  string
	clientset kubernetes.Interface
	executor  Executor
	reporter  StatusReporter
	factory   informers.SharedInformerFactory
	mu        sync.Mutex
	processed map[string]string // podUID -> last processed annotation state
}

// NewPodWatcher creates a new PodWatcher for the given node.
func NewPodWatcher(
	nodeName string,
	clientset kubernetes.Interface,
	executor Executor,
	reporter StatusReporter,
	factory informers.SharedInformerFactory,
) *PodWatcher {
	return &PodWatcher{
		nodeName:  nodeName,
		clientset: clientset,
		executor:  executor,
		reporter:  reporter,
		factory:   factory,
		processed: make(map[string]string),
	}
}

// Start registers event handlers and starts the informer. It blocks until ctx is cancelled.
func (w *PodWatcher) Start(ctx context.Context) error {
	podInformer := w.factory.Core().V1().Pods().Informer()
	_, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				return
			}
			w.handlePod(ctx, pod)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			pod, ok := newObj.(*corev1.Pod)
			if !ok {
				return
			}
			w.handlePod(ctx, pod)
		},
		DeleteFunc: func(obj interface{}) {
			pod, ok := obj.(*corev1.Pod)
			if !ok {
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if !ok {
					return
				}
				pod, ok = tombstone.Obj.(*corev1.Pod)
				if !ok {
					return
				}
			}
			w.mu.Lock()
			delete(w.processed, string(pod.UID))
			w.mu.Unlock()
		},
	})
	if err != nil {
		return fmt.Errorf("failed to add event handler: %w", err)
	}

	w.factory.Start(ctx.Done())
	w.factory.WaitForCacheSync(ctx.Done())

	klog.InfoS("pod watcher started", "nodeName", w.nodeName)
	<-ctx.Done()
	return nil
}

func (w *PodWatcher) handlePod(ctx context.Context, pod *corev1.Pod) {
	// Only handle pods on this node
	if pod.Spec.NodeName != w.nodeName {
		return
	}

	podUID := string(pod.UID)

	// Determine the desired action from annotations
	action := w.getDesiredAction(pod)
	if action == "" {
		// No action annotation present. Clear any previous dedup state so
		// the pod can be re-processed when an annotation is (re-)applied.
		w.resetProcessed(podUID)
		return
	}

	// Dedup: skip if already processed this action for this pod
	w.mu.Lock()
	lastAction := w.processed[podUID]
	if lastAction == action {
		w.mu.Unlock()
		return
	}
	w.processed[podUID] = action
	w.mu.Unlock()

	// Execute in a goroutine to avoid blocking the informer
	go func() {
		switch action {
		case "pause":
			w.executePause(ctx, pod)
		case "resume":
			w.executeResume(ctx, pod)
		}
	}()
}

func (w *PodWatcher) getDesiredAction(pod *corev1.Pod) string {
	if pod.Annotations == nil {
		return ""
	}
	if _, ok := pod.Annotations[agentsv1alpha1.AnnotationRuncPause]; ok {
		return "pause"
	}
	if _, ok := pod.Annotations[agentsv1alpha1.AnnotationRuncResume]; ok {
		return "resume"
	}
	return ""
}

func (w *PodWatcher) executePause(ctx context.Context, pod *corev1.Pod) {
	logger := klog.FromContext(ctx)
	podUID := string(pod.UID)
	logger.Info("executing runc pause", "pod", klog.KObj(pod))

	// Parse container IDs from annotation
	annotation := pod.Annotations[agentsv1alpha1.AnnotationRuncPause]
	containerIDs, err := parseContainerIDs(annotation)
	if err != nil {
		logger.Error(err, "failed to parse container IDs for pause", "pod", klog.KObj(pod))
		w.reportError(ctx, pod, PodConditionContainersPaused, fmt.Sprintf("parse container IDs: %v", err))
		w.resetProcessed(podUID)
		return
	}

	if err := w.executor.PauseContainers(ctx, containerIDs); err != nil {
		logger.Error(err, "runc pause failed", "pod", klog.KObj(pod))
		w.reportError(ctx, pod, PodConditionContainersPaused, fmt.Sprintf("runc pause: %v", err))
		w.resetProcessed(podUID)
		return
	}

	// Report success
	if err := w.reporter.ReportCondition(ctx, pod,
		PodConditionContainersPaused,
		corev1.ConditionTrue,
		PodConditionReasonRuncPaused,
		"runc containers paused successfully",
	); err != nil {
		logger.Error(err, "failed to report pause success", "pod", klog.KObj(pod))
	}
}

func (w *PodWatcher) executeResume(ctx context.Context, pod *corev1.Pod) {
	logger := klog.FromContext(ctx)
	podUID := string(pod.UID)
	logger.Info("executing runc resume", "pod", klog.KObj(pod))

	// Parse container IDs from annotation
	annotation := pod.Annotations[agentsv1alpha1.AnnotationRuncResume]
	containerIDs, err := parseContainerIDs(annotation)
	if err != nil {
		logger.Error(err, "failed to parse container IDs for resume", "pod", klog.KObj(pod))
		w.reportError(ctx, pod, PodConditionContainersResumed, fmt.Sprintf("parse container IDs: %v", err))
		w.resetProcessed(podUID)
		return
	}

	if err := w.executor.ResumeContainers(ctx, containerIDs); err != nil {
		logger.Error(err, "runc resume failed", "pod", klog.KObj(pod))
		w.reportError(ctx, pod, PodConditionContainersResumed, fmt.Sprintf("runc resume: %v", err))
		w.resetProcessed(podUID)
		return
	}

	// Report success
	if err := w.reporter.ReportCondition(ctx, pod,
		PodConditionContainersResumed,
		corev1.ConditionTrue,
		PodConditionReasonRuncResumed,
		"runc containers resumed successfully",
	); err != nil {
		logger.Error(err, "failed to report resume success", "pod", klog.KObj(pod))
	}
}

// resetProcessed clears the dedup entry for the given pod UID so that
// the same action can be retried on the next informer event.
func (w *PodWatcher) resetProcessed(podUID string) {
	w.mu.Lock()
	delete(w.processed, podUID)
	w.mu.Unlock()
}

func (w *PodWatcher) reportError(ctx context.Context, pod *corev1.Pod, conditionType corev1.PodConditionType, message string) {
	if err := w.reporter.ReportCondition(ctx, pod,
		conditionType,
		corev1.ConditionTrue,
		PodConditionReasonRuncError,
		message,
	); err != nil {
		klog.ErrorS(err, "failed to report error condition", "pod", klog.KObj(pod))
	}
}

// parseContainerIDs parses a JSON array of container IDs from the annotation value.
func parseContainerIDs(annotation string) ([]string, error) {
	var ids []string
	if err := json.Unmarshal([]byte(annotation), &ids); err != nil {
		return nil, fmt.Errorf("invalid container IDs JSON: %w", err)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("empty container IDs list")
	}
	return ids, nil
}
