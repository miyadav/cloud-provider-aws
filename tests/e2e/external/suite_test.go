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
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

			// Get a random ready schedulable node to test with
			nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to list nodes")
			Expect(nodes.Items).NotTo(BeEmpty(), "No nodes available for testing")

			// Find a schedulable node
			var testNode *v1.Node
			for i := range nodes.Items {
				node := &nodes.Items[i]
				if !node.Spec.Unschedulable {
					// Check if node is ready
					for _, condition := range node.Status.Conditions {
						if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
							testNode = node
							break
						}
					}
					if testNode != nil {
						break
					}
				}
			}

			Expect(testNode).NotTo(BeNil(), "No ready schedulable nodes found")

			klog.Infof("Testing with node: %s", testNode.Name)
			originalNodeCount := len(nodes.Items)

			// Delete the node on the cloud provider (terminate the EC2 instance)
			err = nodeTester.DeleteNodeOnCloudProvider(testNode)
			Expect(err).NotTo(HaveOccurred(), "Failed to delete node on cloud provider")

			// Wait for the node to be removed from the API server
			// The cloud controller manager should detect the missing instance and delete the node
			Eventually(func() bool {
				_, err := clientset.CoreV1().Nodes().Get(ctx, testNode.Name, metav1.GetOptions{})
				return err != nil // Node should be gone
			}, "5m", "10s").Should(BeTrue(), "Node was not deleted from API server")

			// Verify node count decreased
			updatedNodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			Expect(err).NotTo(HaveOccurred(), "Failed to list nodes after deletion")
			Expect(len(updatedNodes.Items)).To(Equal(originalNodeCount-1), "Node count did not decrease by 1")

			klog.Infof("Successfully verified node %s was deleted from API server", testNode.Name)
		})
	})
})
