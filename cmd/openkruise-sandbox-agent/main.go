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

package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"github.com/openkruise/agents/pkg/agent"
)

func main() {
	var (
		nodeName   string
		kubeconfig string
	)

	flag.StringVar(&nodeName, "node-name", os.Getenv("NODE_NAME"), "The name of the node this agent runs on")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (for out-of-cluster development)")

	klog.InitFlags(nil)
	flag.Parse()

	if nodeName == "" {
		klog.Fatal("--node-name or NODE_NAME environment variable must be set")
	}

	klog.InfoS("Starting openkruise-sandbox-agent", "nodeName", nodeName)

	// Build kubernetes client
	var config *rest.Config
	var err error
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		klog.Fatalf("Failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Failed to create kubernetes clientset: %v", err)
	}

	// Create shared informer factory filtered by node
	factory := informers.NewSharedInformerFactoryWithOptions(
		clientset,
		0,
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			opts.FieldSelector = "spec.nodeName=" + nodeName
		}),
	)

	// Create components
	executor := agent.NewRuncExecutor()
	reporter := agent.NewStatusReporter(clientset)
	watcher := agent.NewPodWatcher(nodeName, clientset, executor, reporter, factory)

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		klog.InfoS("Received signal, shutting down", "signal", sig)
		cancel()
	}()

	// Run the watcher (blocks until context is cancelled)
	if err := watcher.Start(ctx); err != nil {
		klog.Fatalf("PodWatcher exited with error: %v", err)
	}

	klog.InfoS("openkruise-sandbox-agent exited")
}
