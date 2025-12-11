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
	"fmt"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	kubeconfig         string
	awsRegion          string
	nodeTester         *AWSNodeTester
	loadBalancerTester *AWSLoadBalancerTester
	zoneTester         *AWSZoneTester
	clientset          *kubernetes.Clientset
)

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file")
	flag.StringVar(&awsRegion, "region", "", "AWS region (e.g., us-east-2). Can also be set via AWS_REGION env var")
	// Note: klog.InitFlags is not called here to avoid flag redefinition conflicts with testing framework
}

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS Cloud Provider E2E Suite")
}

// detectRegionFromCluster attempts to detect the AWS region from cluster nodes
func detectRegionFromCluster(ctx context.Context, cs *kubernetes.Clientset) (string, error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return "", fmt.Errorf("no nodes found in cluster")
	}

	// Try to get region from node labels
	for _, node := range nodes.Items {
		// Check for standard region label
		if region, ok := node.Labels["topology.kubernetes.io/region"]; ok && region != "" {
			return region, nil
		}
		// Check for legacy region label
		if region, ok := node.Labels["failure-domain.beta.kubernetes.io/region"]; ok && region != "" {
			return region, nil
		}
		// Try to extract from provider ID (format: aws://region/zone/instance-id)
		if providerID := node.Spec.ProviderID; providerID != "" {
			if strings.HasPrefix(providerID, "aws://") {
				parts := strings.Split(providerID, "/")
				if len(parts) >= 3 && parts[1] != "" {
					return parts[1], nil
				}
			}
		}
	}

	return "", fmt.Errorf("could not detect region from cluster nodes")
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

	// Auto-detect region from cluster if not provided
	detectedRegion := awsRegion
	if detectedRegion == "" {
		detected, err := detectRegionFromCluster(ctx, clientset)
		if err == nil {
			detectedRegion = detected
		}
		// If detection fails, we'll let the AWS SDK try to get it from environment/config
	}

	// Create AWS node tester (implements NodeTester interface from k8s.io/kubernetes/test/e2e/cloud/external)
	// Note: Cloud provider annotation should be set by the framework in the Kubernetes fork
	var ntErr error
	nodeTester, ntErr = newAWSNodeTesterWithRegion(ctx, detectedRegion)
	Expect(ntErr).NotTo(HaveOccurred(), "Failed to create AWS node tester")

	// Create AWS load balancer tester
	var lbtErr error
	loadBalancerTester, lbtErr = newAWSLoadBalancerTesterWithRegion(ctx, detectedRegion)
	Expect(lbtErr).NotTo(HaveOccurred(), "Failed to create AWS load balancer tester")

	// Create AWS zone tester
	var ztErr error
	zoneTester, ztErr = newAWSZoneTesterWithRegion(ctx, detectedRegion)
	Expect(ztErr).NotTo(HaveOccurred(), "Failed to create AWS zone tester")
})

var _ = Describe("AWS Cloud Controller Manager Node Tests", func() {
	Context("Node lifecycle", func() {
		It("should delete node from API server when not in cloud provider", func() {
			ctx := context.Background()

			// Call the interface method which contains the full test orchestration logic
			// This properly uses the NodeTester interface from k8s.io/kubernetes/test/e2e/cloud/external
			result, err := nodeTester.TestNodeDeletedOnAPIServerWhenNotInCloudProvider(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Node deletion test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("Instance existence", func() {
		It("should verify all nodes exist as instances in AWS", func() {
			ctx := context.Background()

			// Verify that all Kubernetes nodes correspond to existing EC2 instances
			result, err := nodeTester.TestInstanceExists(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Instance existence test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("Instance shutdown detection", func() {
		It("should correctly detect running vs shutdown instances", func() {
			ctx := context.Background()

			// Verify that running nodes are not reported as shutdown
			result, err := nodeTester.TestInstanceShutdown(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Instance shutdown test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("Instance metadata", func() {
		It("should correctly report instance metadata", func() {
			ctx := context.Background()

			// Verify instance type, zone, region, and addresses match AWS
			result, err := nodeTester.TestInstanceMetadata(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Instance metadata test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})
})

var _ = Describe("AWS Cloud Controller Manager LoadBalancer Tests", func() {
	Context("LoadBalancer provisioning", func() {
		It("should create a load balancer for a LoadBalancer type service", func() {
			ctx := context.Background()

			result, err := loadBalancerTester.TestEnsureLoadBalancer(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "EnsureLoadBalancer test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("LoadBalancer updates", func() {
		It("should update load balancer when service is modified", func() {
			ctx := context.Background()

			result, err := loadBalancerTester.TestUpdateLoadBalancer(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "UpdateLoadBalancer test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("LoadBalancer deletion", func() {
		It("should delete load balancer when service is deleted", func() {
			ctx := context.Background()

			result, err := loadBalancerTester.TestEnsureLoadBalancerDeleted(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "EnsureLoadBalancerDeleted test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("LoadBalancer status", func() {
		It("should correctly retrieve load balancer status", func() {
			ctx := context.Background()

			result, err := loadBalancerTester.TestGetLoadBalancer(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "GetLoadBalancer test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})
})

var _ = Describe("AWS Cloud Controller Manager Zone Tests", func() {
	Context("Zone information", func() {
		It("should correctly report zone information for nodes", func() {
			ctx := context.Background()

			result, err := zoneTester.TestGetZone(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "GetZone test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("Zone by provider ID", func() {
		It("should correctly return zone by provider ID", func() {
			ctx := context.Background()

			result, err := zoneTester.TestGetZoneByProviderID(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "GetZoneByProviderID test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("Zone by node name", func() {
		It("should correctly return zone by node name", func() {
			ctx := context.Background()

			result, err := zoneTester.TestGetZoneByNodeName(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "GetZoneByNodeName test failed")
			Expect(result.Success).To(BeTrue(), result.Message)
		})
	})

	Context("AWS Zone ID labels", func() {
		It("should have correct zone-id topology labels on nodes", func() {
			ctx := context.Background()

			// Use the AWS-specific test for zone ID labels
			// Note: TestZoneIDLabel is AWS-specific and not part of the ZonesTester interface
			// This test remains as a custom AWS test
			err := zoneTester.TestZoneIDLabel(ctx, clientset)
			Expect(err).NotTo(HaveOccurred(), "Zone ID label test failed")
		})
	})
})
