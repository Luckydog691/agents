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
	"fmt"
	"os/exec"
	"strings"

	"k8s.io/klog/v2"
)

// CommandExecutor is the function type for running system commands.
// It allows mocking in tests.
type CommandExecutor func(ctx context.Context, name string, args ...string) ([]byte, error)

// DefaultCommandExecutor executes a command using os/exec.
func DefaultCommandExecutor(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

// RuncExecutor implements Executor using nsenter + ctr to pause/resume containers.
type RuncExecutor struct {
	// cmdExecutor allows injecting a mock for testing.
	cmdExecutor CommandExecutor
}

// NewRuncExecutor creates a new RuncExecutor with default command execution.
func NewRuncExecutor() *RuncExecutor {
	return &RuncExecutor{
		cmdExecutor: DefaultCommandExecutor,
	}
}

// NewRuncExecutorWithCmdExecutor creates a RuncExecutor with a custom command executor (for testing).
func NewRuncExecutorWithCmdExecutor(executor CommandExecutor) *RuncExecutor {
	return &RuncExecutor{
		cmdExecutor: executor,
	}
}

// PauseContainers pauses containers one by one. If any fails, already-paused containers are rolled back.
func (e *RuncExecutor) PauseContainers(ctx context.Context, containerIDs []string) error {
	var paused []string
	for _, id := range containerIDs {
		if err := e.pauseOne(ctx, id); err != nil {
			klog.ErrorS(err, "Failed to pause container, rolling back", "containerID", id, "pausedCount", len(paused))
			// Rollback: resume already-paused containers
			for _, pid := range paused {
				if resumeErr := e.resumeOne(ctx, pid); resumeErr != nil {
					klog.ErrorS(resumeErr, "Failed to rollback (resume) container during pause rollback", "containerID", pid)
				}
			}
			return fmt.Errorf("failed to pause container %s: %w (rolled back %d containers)", id, err, len(paused))
		}
		paused = append(paused, id)
	}
	klog.InfoS("All containers paused successfully", "count", len(paused))
	return nil
}

// ResumeContainers resumes containers one by one. Does NOT roll back on failure
// (partially resumed is better than fully frozen).
func (e *RuncExecutor) ResumeContainers(ctx context.Context, containerIDs []string) error {
	for _, id := range containerIDs {
		if err := e.resumeOne(ctx, id); err != nil {
			return fmt.Errorf("failed to resume container %s: %w", id, err)
		}
	}
	klog.InfoS("All containers resumed successfully", "count", len(containerIDs))
	return nil
}

func (e *RuncExecutor) pauseOne(ctx context.Context, containerID string) error {
	output, err := e.cmdExecutor(ctx,
		"nsenter", "-t", "1", "-m", "--",
		"ctr", "-n", "k8s.io", "task", "pause", containerID)
	if err != nil {
		return fmt.Errorf("ctr task pause failed for %s: %s: %w",
			containerID, strings.TrimSpace(string(output)), err)
	}
	klog.V(4).InfoS("Container paused", "containerID", containerID)
	return nil
}

func (e *RuncExecutor) resumeOne(ctx context.Context, containerID string) error {
	output, err := e.cmdExecutor(ctx,
		"nsenter", "-t", "1", "-m", "--",
		"ctr", "-n", "k8s.io", "task", "resume", containerID)
	if err != nil {
		return fmt.Errorf("ctr task resume failed for %s: %s: %w",
			containerID, strings.TrimSpace(string(output)), err)
	}
	klog.V(4).InfoS("Container resumed", "containerID", containerID)
	return nil
}
