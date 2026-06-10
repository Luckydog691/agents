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

package e2e

// This file exercises the runc in-place pause/resume path. The cases here
// only make sense when the controller is started with the
// `RuncPauseResume` feature gate explicitly enabled, e.g.
//
//	agent-sandbox-controller --feature-gates=RuncPauseResume=true
//
// When the gate is off (the default, used by upstream CI), the controller
// follows the legacy delete-and-recreate flow which does NOT preserve the
// Pod UID across pause/resume, and these cases will fail by design.
//
// Recommended invocation:
//
//	./bin/e2e.test -ginkgo.focus="runc in-place pause and resume"
//
// against a cluster whose controller has the gate turned on.

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"

	agentsv1alpha1 "github.com/openkruise/agents/api/v1alpha1"
)

const (
	// Pod conditions reported by the runc agent. Defined as string literals
	// here to keep the e2e package independent of the agent package.
	podConditionContainersPaused  corev1.PodConditionType = "ContainersPaused"
	podConditionContainersResumed corev1.PodConditionType = "ContainersResumed"
)

// getPodCondition is a tiny test helper that returns a pointer to the named
// condition on a Pod, or nil if absent.
func getPodCondition(pod *corev1.Pod, condType corev1.PodConditionType) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		c := &pod.Status.Conditions[i]
		if c.Type == condType {
			return c
		}
	}
	return nil
}

var _ = Describe("Sandbox runc in-place pause and resume", func() {
	var (
		sandbox   *agentsv1alpha1.Sandbox
		ctx       = context.Background()
		namespace string
		image     string
	)

	BeforeEach(func() {
		namespace = createNamespace(ctx)
		image = imageOf("nginx:stable-alpine3.20")
		// runtimeClassName intentionally left unset so the controller routes
		// this sandbox to the runc in-place pause/resume path.
		sandbox = &agentsv1alpha1.Sandbox{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("test-runc-sandbox-%d", time.Now().UnixNano()),
				Namespace: namespace,
			},
			Spec: agentsv1alpha1.SandboxSpec{
				EmbeddedSandboxTemplate: agentsv1alpha1.EmbeddedSandboxTemplate{
					Template: &corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "test-container",
									Image: image,
									Ports: []corev1.ContainerPort{
										{Name: "http", ContainerPort: 80},
									},
								},
							},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			},
		}
	})

	AfterEach(func() {
		_ = k8sClient.Delete(ctx, sandbox)
	})

	Context("in-place freeze", func() {
		It("should freeze containers in place without deleting the pod and resume to the same pod", func() {
			By("Creating a runc Sandbox")
			Expect(k8sClient.Create(ctx, sandbox)).To(Succeed())

			By("Waiting for the sandbox to reach Running phase")
			Eventually(func() agentsv1alpha1.SandboxPhase {
				_ = k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, sandbox)
				return sandbox.Status.Phase
			}, time.Second*120, time.Second).Should(Equal(agentsv1alpha1.SandboxRunning))

			By("Recording the original pod UID for in-place verification")
			originalPod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, apitypes.NamespacedName{
				Name:      sandbox.Name,
				Namespace: sandbox.Namespace,
			}, originalPod)).To(Succeed())
			originalUID := originalPod.UID
			Expect(originalUID).NotTo(BeEmpty())

			By("Pausing the sandbox via spec.paused = true")
			Expect(updateSandboxSpec(ctx, &agentsv1alpha1.Sandbox{
				ObjectMeta: sandbox.ObjectMeta,
				Spec:       func() agentsv1alpha1.SandboxSpec { s := sandbox.Spec; s.Paused = true; return s }(),
			})).To(Succeed())

			By("Waiting for the sandbox to reach Paused phase")
			Eventually(func() agentsv1alpha1.SandboxPhase {
				_ = k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, sandbox)
				return sandbox.Status.Phase
			}, time.Second*60, time.Second).Should(Equal(agentsv1alpha1.SandboxPaused))

			By("Verifying the pod still exists and the UID is unchanged (in-place freeze)")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				err := k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, pod)
				return err == nil && pod.UID == originalUID && pod.DeletionTimestamp.IsZero()
			}, time.Second*30, time.Second).Should(BeTrue(),
				"runc pause must keep the original Pod alive (no deletion, same UID)")

			By("Verifying the agent reported ContainersPaused=True on the pod")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				if err := k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, pod); err != nil {
					return false
				}
				cond := getPodCondition(pod, podConditionContainersPaused)
				return cond != nil && cond.Status == corev1.ConditionTrue
			}, time.Second*60, time.Second).Should(BeTrue(),
				"agent must report ContainersPaused=True after a successful pause")

			By("Verifying the SandboxPaused condition is True on the sandbox")
			Eventually(func() bool {
				_ = k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, sandbox)
				for _, c := range sandbox.Status.Conditions {
					if c.Type == string(agentsv1alpha1.SandboxConditionPaused) && c.Status == metav1.ConditionTrue {
						return true
					}
				}
				return false
			}, time.Second*30, time.Second).Should(BeTrue())

			By("Resuming the sandbox via spec.paused = false")
			Expect(updateSandboxSpec(ctx, &agentsv1alpha1.Sandbox{
				ObjectMeta: sandbox.ObjectMeta,
				Spec:       func() agentsv1alpha1.SandboxSpec { s := sandbox.Spec; s.Paused = false; return s }(),
			})).To(Succeed())

			By("Waiting for the sandbox to return to Running phase")
			Eventually(func() agentsv1alpha1.SandboxPhase {
				_ = k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, sandbox)
				return sandbox.Status.Phase
			}, time.Second*120, time.Second).Should(Equal(agentsv1alpha1.SandboxRunning))

			By("Verifying the pod UID is still the original (in-place resume, not recreation)")
			finalPod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, apitypes.NamespacedName{
				Name:      sandbox.Name,
				Namespace: sandbox.Namespace,
			}, finalPod)).To(Succeed())
			Expect(finalPod.UID).To(Equal(originalUID),
				"runc resume must keep the same Pod (same UID), not recreate it")

			By("Verifying the agent reported ContainersResumed=True on the pod")
			Eventually(func() bool {
				pod := &corev1.Pod{}
				if err := k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, pod); err != nil {
					return false
				}
				cond := getPodCondition(pod, podConditionContainersResumed)
				return cond != nil && cond.Status == corev1.ConditionTrue
			}, time.Second*60, time.Second).Should(BeTrue(),
				"agent must report ContainersResumed=True after a successful resume")

			By("Verifying the resumed sandbox carries pod information again")
			Expect(sandbox.Status.PodInfo.PodIP).NotTo(BeEmpty())
			Expect(sandbox.Status.SandboxIp).NotTo(BeEmpty())
		})

		It("should support multiple consecutive pause/resume cycles on the same pod", func() {
			By("Creating a runc Sandbox")
			Expect(k8sClient.Create(ctx, sandbox)).To(Succeed())

			By("Waiting for the sandbox to reach Running phase")
			Eventually(func() agentsv1alpha1.SandboxPhase {
				_ = k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, sandbox)
				return sandbox.Status.Phase
			}, time.Second*120, time.Second).Should(Equal(agentsv1alpha1.SandboxRunning))

			pod := &corev1.Pod{}
			Expect(k8sClient.Get(ctx, apitypes.NamespacedName{
				Name:      sandbox.Name,
				Namespace: sandbox.Namespace,
			}, pod)).To(Succeed())
			originalUID := pod.UID

			for cycle := 1; cycle <= 2; cycle++ {
				By(fmt.Sprintf("Cycle %d: pausing", cycle))
				Expect(updateSandboxSpec(ctx, &agentsv1alpha1.Sandbox{
					ObjectMeta: sandbox.ObjectMeta,
					Spec:       func() agentsv1alpha1.SandboxSpec { s := sandbox.Spec; s.Paused = true; return s }(),
				})).To(Succeed())
				Eventually(func() agentsv1alpha1.SandboxPhase {
					_ = k8sClient.Get(ctx, apitypes.NamespacedName{
						Name:      sandbox.Name,
						Namespace: sandbox.Namespace,
					}, sandbox)
					return sandbox.Status.Phase
				}, time.Second*60, time.Second).Should(Equal(agentsv1alpha1.SandboxPaused))

				By(fmt.Sprintf("Cycle %d: resuming", cycle))
				Expect(updateSandboxSpec(ctx, &agentsv1alpha1.Sandbox{
					ObjectMeta: sandbox.ObjectMeta,
					Spec:       func() agentsv1alpha1.SandboxSpec { s := sandbox.Spec; s.Paused = false; return s }(),
				})).To(Succeed())
				Eventually(func() agentsv1alpha1.SandboxPhase {
					_ = k8sClient.Get(ctx, apitypes.NamespacedName{
						Name:      sandbox.Name,
						Namespace: sandbox.Namespace,
					}, sandbox)
					return sandbox.Status.Phase
				}, time.Second*120, time.Second).Should(Equal(agentsv1alpha1.SandboxRunning))

				By(fmt.Sprintf("Cycle %d: verifying pod UID is unchanged", cycle))
				cur := &corev1.Pod{}
				Expect(k8sClient.Get(ctx, apitypes.NamespacedName{
					Name:      sandbox.Name,
					Namespace: sandbox.Namespace,
				}, cur)).To(Succeed())
				Expect(cur.UID).To(Equal(originalUID),
					"pod UID must remain stable across pause/resume cycles")
			}
		})
	})
})
