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
	"testing"
)

func TestRuncExecutor_PauseContainers_Success(t *testing.T) {
	var called []string
	executor := NewRuncExecutorWithCmdExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		// args: -t 1 -m -- ctr -n k8s.io task pause <containerID>
		containerID := args[len(args)-1]
		called = append(called, containerID)
		return nil, nil
	})

	err := executor.PauseContainers(context.Background(), []string{"abc123", "def456"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(called) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(called))
	}
	if called[0] != "abc123" || called[1] != "def456" {
		t.Errorf("unexpected call order: %v", called)
	}
}

func TestRuncExecutor_PauseContainers_Rollback(t *testing.T) {
	var paused []string
	var resumed []string
	executor := NewRuncExecutorWithCmdExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		action := args[len(args)-2] // "pause" or "resume"
		containerID := args[len(args)-1]
		if action == "pause" {
			if containerID == "fail-me" {
				return []byte("error output"), fmt.Errorf("exit status 1")
			}
			paused = append(paused, containerID)
		} else if action == "resume" {
			resumed = append(resumed, containerID)
		}
		return nil, nil
	})

	err := executor.PauseContainers(context.Background(), []string{"ok1", "ok2", "fail-me"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// ok1 and ok2 should have been paused then rolled back
	if len(paused) != 2 {
		t.Errorf("expected 2 paused before failure, got %d", len(paused))
	}
	if len(resumed) != 2 {
		t.Errorf("expected 2 resumed (rollback), got %d", len(resumed))
	}
}

func TestRuncExecutor_ResumeContainers_Success(t *testing.T) {
	var called []string
	executor := NewRuncExecutorWithCmdExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		containerID := args[len(args)-1]
		called = append(called, containerID)
		return nil, nil
	})

	err := executor.ResumeContainers(context.Background(), []string{"abc123", "def456"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if len(called) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(called))
	}
}

func TestRuncExecutor_ResumeContainers_NoRollback(t *testing.T) {
	callCount := 0
	executor := NewRuncExecutorWithCmdExecutor(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		callCount++
		containerID := args[len(args)-1]
		if containerID == "fail-me" {
			return []byte("error"), fmt.Errorf("exit status 1")
		}
		return nil, nil
	})

	err := executor.ResumeContainers(context.Background(), []string{"ok1", "fail-me", "ok2"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// Should stop at fail-me, NOT continue to ok2
	if callCount != 2 {
		t.Errorf("expected 2 calls (stop on failure), got %d", callCount)
	}
}
