package external_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/cloud-provider-aws/tests/e2e/external"
	"k8s.io/kubernetes/test/e2e/framework"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
)

const kubeconfigEnvVar = "KUBECONFIG"

// regionFromProviderID extracts the AWS region from providerID (e.g. aws:///us-east-2a/i-xxx -> us-east-2).
// EC2 is regional; the client must use the node's region for DescribeInstances to find the instance.
func regionFromProviderID(providerID string) string {
	parts := strings.Split(providerID, "/")
	// Zone is the segment before the instance ID (last segment). Instance ID is last, zone is second-to-last.
	if len(parts) < 2 {
		return ""
	}
	zone := strings.TrimSpace(parts[len(parts)-2])
	if len(zone) < 2 {
		return ""
	}
	// Region is zone without the trailing AZ letter (e.g. us-east-2a -> us-east-2).
	return zone[:len(zone)-1]
}

// ec2ClientForNode creates an EC2 client configured for the node's region so DescribeInstances finds the instance.
func ec2ClientForNode(ctx context.Context, providerID string) *ec2.Client {
	region := regionFromProviderID(providerID)
	Expect(region).NotTo(BeEmpty(), "providerID must contain zone to derive region: %s", providerID)
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	Expect(err).NotTo(HaveOccurred(), "failed to load AWS config for region %s", region)
	return ec2.NewFromConfig(cfg)
}

var _ = Describe("[cloud-provider-aws-e2e] CCM TestInterface", func() {
	// Use KUBECONFIG or ~/.kube/config for cluster connection when framework has no kubeconfig set (e.g. when running go test ./external/ without flags).
	BeforeEach(func(ctx context.Context) {
		if framework.TestContext.KubeConfig != "" {
			return
		}
		kubeconfig := os.Getenv(kubeconfigEnvVar)
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		framework.TestContext.KubeConfig = kubeconfig
	})

	f := framework.NewDefaultFramework("cloud-provider-aws-ccm")

	Describe("NodeLifecycle Interface", func() {
		It("should implement TestInterface and return NodeLifecycle", func(ctx context.Context) {
			nodeList, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
			Expect(err).NotTo(HaveOccurred(), "failed to get nodes")
			Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "cluster has no nodes")

			node := nodeList.Items[0]
			nodeName := node.Name
			providerID := node.Spec.ProviderID

			framework.Logf("Testing with node: %s, providerID: %s", nodeName, providerID)

			ec2Client := ec2ClientForNode(ctx, providerID)
			testInterface, err := external.NewAWSTestInterface(ctx, ec2Client, nodeName, providerID)
			Expect(err).NotTo(HaveOccurred(), "failed to create AWSTestInterface")

			// Test NodeLifecycle - should be implemented
			implemented, nodeLifecycle := testInterface.NodeLifecycle()
			Expect(implemented).To(BeTrue(), "NodeLifecycle should be implemented")
			Expect(nodeLifecycle).NotTo(BeNil(), "NodeLifecycle interface should not be nil")

			// Test that other interfaces are not implemented (POC)
			implemented, _ = testInterface.LoadBalancer()
			Expect(implemented).To(BeFalse(), "LoadBalancer should not be implemented in POC")

			implemented, _ = testInterface.Routes()
			Expect(implemented).To(BeFalse(), "Routes should not be implemented in POC")

			implemented, _ = testInterface.Topology()
			Expect(implemented).To(BeFalse(), "Topology should not be implemented in POC")

			implemented, _ = testInterface.Cluster()
			Expect(implemented).To(BeFalse(), "Cluster should not be implemented in POC")
		})

		It("should verify node exists in AWS", func(ctx context.Context) {
			nodeList, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
			Expect(err).NotTo(HaveOccurred(), "failed to get nodes")
			Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "cluster has no nodes")

			node := nodeList.Items[0]
			nodeName := node.Name
			providerID := node.Spec.ProviderID

			framework.Logf("Testing node existence: %s, providerID: %s", nodeName, providerID)

			ec2Client := ec2ClientForNode(ctx, providerID)
			testInterface, err := external.NewAWSTestInterface(ctx, ec2Client, nodeName, providerID)
			Expect(err).NotTo(HaveOccurred(), "failed to create AWSTestInterface")

			// Get NodeLifecycle interface
			implemented, nodeLifecycle := testInterface.NodeLifecycle()
			Expect(implemented).To(BeTrue())
			Expect(nodeLifecycle).NotTo(BeNil())

			// Test Exists()
			exists := nodeLifecycle.Exists()
			Expect(exists).To(BeTrue(), "node should exist in AWS")

			framework.Logf("Node %s exists in AWS: %v", nodeName, exists)
		})

		It("should verify node is not shutdown", func(ctx context.Context) {
			nodeList, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
			Expect(err).NotTo(HaveOccurred(), "failed to get nodes")
			Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "cluster has no nodes")

			node := nodeList.Items[0]
			nodeName := node.Name
			providerID := node.Spec.ProviderID

			framework.Logf("Testing node shutdown status: %s, providerID: %s", nodeName, providerID)

			ec2Client := ec2ClientForNode(ctx, providerID)
			testInterface, err := external.NewAWSTestInterface(ctx, ec2Client, nodeName, providerID)
			Expect(err).NotTo(HaveOccurred(), "failed to create AWSTestInterface")

			// Get NodeLifecycle interface
			implemented, nodeLifecycle := testInterface.NodeLifecycle()
			Expect(implemented).To(BeTrue())
			Expect(nodeLifecycle).NotTo(BeNil())

			// Test IsShutdown()
			isShutdown := nodeLifecycle.IsShutdown()
			Expect(isShutdown).To(BeFalse(), "running node should not be shutdown")

			framework.Logf("Node %s is shutdown: %v", nodeName, isShutdown)
		})

		It("should get node details from AWS", func(ctx context.Context) {
			nodeList, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
			Expect(err).NotTo(HaveOccurred(), "failed to get nodes")
			Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "cluster has no nodes")

			node := nodeList.Items[0]
			nodeName := node.Name
			providerID := node.Spec.ProviderID

			framework.Logf("Getting node details: %s, providerID: %s", nodeName, providerID)

			ec2Client := ec2ClientForNode(ctx, providerID)
			testInterface, err := external.NewAWSTestInterface(ctx, ec2Client, nodeName, providerID)
			Expect(err).NotTo(HaveOccurred(), "failed to create AWSTestInterface")

			// Get NodeLifecycle interface
			implemented, nodeLifecycle := testInterface.NodeLifecycle()
			Expect(implemented).To(BeTrue())
			Expect(nodeLifecycle).NotTo(BeNil())

			// Test Details()
			details := nodeLifecycle.Details()
			Expect(details.InstanceID).NotTo(BeEmpty(), "instance ID should not be empty")
			Expect(details.ProviderID).NotTo(BeEmpty(), "provider ID should not be empty")
			Expect(details.InstanceType).NotTo(BeEmpty(), "instance type should not be empty")
			Expect(details.Metadata).To(HaveKey("availability-zone"), "metadata should contain availability zone")
			Expect(details.Metadata["availability-zone"]).NotTo(BeEmpty(), "availability zone should not be empty")
			Expect(details.Metadata).To(HaveKey("state"), "metadata should contain instance state")
			Expect(details.Metadata["state"]).NotTo(BeEmpty(), "instance state should not be empty")

			framework.Logf("Node details - InstanceID: %s, Type: %s, AZ: %s, State: %s",
				details.InstanceID, details.InstanceType, details.Metadata["availability-zone"], details.Metadata["state"])
		})

		It("should get node addresses from AWS", func(ctx context.Context) {
			nodeList, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
			Expect(err).NotTo(HaveOccurred(), "failed to get nodes")
			Expect(len(nodeList.Items)).To(BeNumerically(">", 0), "cluster has no nodes")

			node := nodeList.Items[0]
			nodeName := node.Name
			providerID := node.Spec.ProviderID

			framework.Logf("Getting node addresses: %s, providerID: %s", nodeName, providerID)

			ec2Client := ec2ClientForNode(ctx, providerID)
			testInterface, err := external.NewAWSTestInterface(ctx, ec2Client, nodeName, providerID)
			Expect(err).NotTo(HaveOccurred(), "failed to create AWSTestInterface")

			// Get NodeLifecycle interface
			implemented, nodeLifecycle := testInterface.NodeLifecycle()
			Expect(implemented).To(BeTrue())
			Expect(nodeLifecycle).NotTo(BeNil())

			// Test Addresses()
			addresses := nodeLifecycle.Addresses()
			Expect(len(addresses)).To(BeNumerically(">", 0), "node should have at least one address")

			// Log all addresses
			framework.Logf("Node %s has %d addresses:", nodeName, len(addresses))
			for _, addr := range addresses {
				framework.Logf("  - Type: %s, Address: %s", addr.Type, addr.Address)
			}

			// Verify at least InternalIP exists
			hasInternalIP := false
			for _, addr := range addresses {
				if addr.Type == "InternalIP" {
					hasInternalIP = true
					break
				}
			}
			Expect(hasInternalIP).To(BeTrue(), "node should have at least an InternalIP address")
		})
	})
})
