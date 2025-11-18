/*
Copyright 2025 The Kubernetes Authors.

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

package external

import (
	"context"
	"flag"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	cloudprovidertest "k8s.io/cloud-provider/test"
	"k8s.io/klog/v2"
)

var (
	kubeconfig string
	nodeTester cloudprovidertest.NodeTester
	clientset  *kubernetes.Clientset
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	klog.InitFlags(nil)
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS Cloud Provider E2E Suite")
}

var _ = BeforeSuite(func() {
	// Parse flags
	flag.Parse()

	ctx := context.Background()

	// Create kubernetes client
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred(), "Failed to build kubeconfig")

	clientset, err = kubernetes.NewForConfig(config)
	Expect(err).NotTo(HaveOccurred(), "Failed to create kubernetes clientset")

	// Create AWS node tester
	nodeTester, err = NewAWSNodeTester(ctx)
	Expect(err).NotTo(HaveOccurred(), "Failed to create AWS node tester")
})

var _ = Describe("AWS Cloud Controller Manager Node Tests", func() {
	Context("Node lifecycle", func() {
		It("should delete node from API server when not in cloud provider", func() {
			ctx := context.Background()

			// Call the generic test implementation from CCMNodeTester
			// This delegates cloud-specific operations to our AWSNodeTester
			err := nodeTester.TestNodeDeletedOnAPIServerWhenNotInCloudProvider(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Node deletion test failed")
		})
	})
})
