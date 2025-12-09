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
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// NodeTester defines the interface for testing cloud provider node functionality.
// This interface will be provided by k8s.io/cloud-provider/test once the dependency is updated.
type NodeTester interface {
	TestNodeDeletedOnAPIServerWhenNotInCloudProvider(ctx context.Context, c kubernetes.Interface) error
	TestInstanceExists(ctx context.Context, c kubernetes.Interface) error
	TestInstanceShutdown(ctx context.Context, c kubernetes.Interface) error
	TestInstanceMetadata(ctx context.Context, c kubernetes.Interface) error
}

// AWSNodeTester implements the NodeTester interface for AWS cloud provider
type AWSNodeTester struct {
	ec2Client *ec2.Client
}

// Ensure AWSNodeTester implements NodeTester interface
var _ NodeTester = &AWSNodeTester{}

// NewAWSNodeTester creates a new AWSNodeTester instance that implements NodeTester
func NewAWSNodeTester(ctx context.Context) (NodeTester, error) {
	return newAWSNodeTesterWithRegion(ctx, "")
}

// newAWSNodeTesterConcrete creates a new AWSNodeTester instance and returns the concrete type
func newAWSNodeTesterConcrete(ctx context.Context) (*AWSNodeTester, error) {
	return newAWSNodeTesterWithRegion(ctx, "")
}

// newAWSNodeTesterWithRegion creates a new AWSNodeTester instance with explicit region
func newAWSNodeTesterWithRegion(ctx context.Context, region string) (*AWSNodeTester, error) {
	// Load AWS config from the environment
	var cfg aws.Config
	var err error

	if region != "" {
		cfg, err = config.LoadDefaultConfig(ctx, config.WithRegion(region))
	} else {
		cfg, err = config.LoadDefaultConfig(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &AWSNodeTester{
		ec2Client: ec2.NewFromConfig(cfg),
	}, nil
}

// TestNodeDeletedOnAPIServerWhenNotInCloudProvider tests that a node
// should be deleted on API server if it doesn't exist in the cloud provider.
// This implements the test orchestration logic from the NodeTester interface.
func (a *AWSNodeTester) TestNodeDeletedOnAPIServerWhenNotInCloudProvider(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	// Find a ready, schedulable worker node (skip master/control-plane nodes)
	var testNode *v1.Node
	for i := range nodes.Items {
		node := &nodes.Items[i]

		// Skip master/control-plane nodes
		if isMasterNode(node) {
			klog.Infof("Skipping master/control-plane node: %s", node.Name)
			continue
		}

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

	if testNode == nil {
		return fmt.Errorf("no ready schedulable worker nodes found (master/control-plane nodes are excluded)")
	}

	klog.Infof("Testing with node: %s", testNode.Name)
	originalNodeCount := len(nodes.Items)

	// Delete the node on the cloud provider (terminate the EC2 instance)
	if err := a.DeleteNodeOnCloudProvider(testNode); err != nil {
		return fmt.Errorf("failed to delete node on cloud provider: %w", err)
	}

	klog.Infof("Deleted node %s from cloud provider, waiting for API server to remove it...", testNode.Name)

	// Wait for the node to be removed from the API server
	// The cloud controller manager should detect the missing instance and delete the node
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := c.CoreV1().Nodes().Get(ctx, testNode.Name, metav1.GetOptions{})
		if err != nil {
			// Node is gone - this is what we expect
			klog.Infof("Node %s has been removed from API server", testNode.Name)
			return true, nil
		}
		klog.Infof("Node %s still exists, waiting...", testNode.Name)
		return false, nil
	})

	if err != nil {
		return fmt.Errorf("node %s was not deleted from API server within timeout: %w", testNode.Name, err)
	}

	// Verify node count decreased
	updatedNodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes after deletion: %w", err)
	}

	if len(updatedNodes.Items) != originalNodeCount-1 {
		return fmt.Errorf("expected node count to decrease by 1 (from %d to %d), but got %d",
			originalNodeCount, originalNodeCount-1, len(updatedNodes.Items))
	}

	klog.Infof("Successfully verified node %s was deleted from API server", testNode.Name)
	return nil
}

// DeleteNodeOnCloudProvider deletes the specified node from AWS by terminating the EC2 instance
func (a *AWSNodeTester) DeleteNodeOnCloudProvider(node *v1.Node) error {
	if node == nil {
		return fmt.Errorf("node is nil")
	}

	// Extract the instance ID from the node's ProviderID
	// ProviderID format: aws:///zone/instance-id or aws://region/zone/instance-id
	providerID := node.Spec.ProviderID
	if providerID == "" {
		return fmt.Errorf("node %s has no providerID", node.Name)
	}

	instanceID, err := parseAWSProviderID(providerID)
	if err != nil {
		return fmt.Errorf("failed to parse providerID %s: %w", providerID, err)
	}

	// Terminate the EC2 instance
	_, err = a.ec2Client.TerminateInstances(context.Background(), &ec2.TerminateInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return fmt.Errorf("failed to terminate instance %s: %w", instanceID, err)
	}

	return nil
}

// TestInstanceExists verifies that InstanceExists correctly reports instance existence
// for nodes in the cluster. This tests the cloud provider's ability to verify that
// a Kubernetes node corresponds to an actual EC2 instance.
func (a *AWSNodeTester) TestInstanceExists(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	// Test each node's existence in AWS
	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			klog.Warningf("Skipping node %s: no providerID", node.Name)
			continue
		}

		instanceID, err := parseAWSProviderID(providerID)
		if err != nil {
			return fmt.Errorf("failed to parse providerID for node %s: %w", node.Name, err)
		}

		// Check if instance exists in AWS
		exists, err := a.instanceExists(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to check instance existence for node %s: %w", node.Name, err)
		}

		if !exists {
			return fmt.Errorf("instance %s for node %s does not exist in AWS but node exists in Kubernetes", instanceID, node.Name)
		}

		klog.Infof("Verified instance %s exists for node %s", instanceID, node.Name)
	}

	klog.Infof("Successfully verified all %d nodes exist in AWS", len(nodes.Items))
	return nil
}

// instanceExists checks if an EC2 instance exists and is not terminated
func (a *AWSNodeTester) instanceExists(ctx context.Context, instanceID string) (bool, error) {
	result, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		// Check if the error indicates the instance doesn't exist
		if strings.Contains(err.Error(), "InvalidInstanceID.NotFound") {
			return false, nil
		}
		return false, err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			// Instance exists if it's not in terminated state
			if instance.State != nil && instance.State.Name != ec2types.InstanceStateNameTerminated {
				return true, nil
			}
		}
	}

	return false, nil
}

// TestInstanceShutdown verifies that the cloud provider correctly detects shutdown instances.
// This tests the InstanceShutdown functionality which is used to determine if an instance
// is in a stopped/shutdown state vs running.
func (a *AWSNodeTester) TestInstanceShutdown(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	// Verify running nodes are not reported as shutdown
	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			klog.Warningf("Skipping node %s: no providerID", node.Name)
			continue
		}

		instanceID, err := parseAWSProviderID(providerID)
		if err != nil {
			return fmt.Errorf("failed to parse providerID for node %s: %w", node.Name, err)
		}

		// Check instance state
		shutdown, err := a.isInstanceShutdown(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to check shutdown state for node %s: %w", node.Name, err)
		}

		// For nodes that are Ready, they should not be shutdown
		isReady := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == v1.NodeReady && condition.Status == v1.ConditionTrue {
				isReady = true
				break
			}
		}

		if isReady && shutdown {
			return fmt.Errorf("node %s is Ready but instance %s is reported as shutdown", node.Name, instanceID)
		}

		klog.Infof("Verified shutdown state for node %s (instance %s): shutdown=%v, ready=%v", node.Name, instanceID, shutdown, isReady)
	}

	klog.Infof("Successfully verified shutdown state for all nodes")
	return nil
}

// isInstanceShutdown checks if an EC2 instance is in a stopped/stopping state
func (a *AWSNodeTester) isInstanceShutdown(ctx context.Context, instanceID string) (bool, error) {
	result, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return false, err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State != nil {
				switch instance.State.Name {
				case ec2types.InstanceStateNameStopped,
					ec2types.InstanceStateNameStopping,
					ec2types.InstanceStateNameTerminated,
					ec2types.InstanceStateNameShuttingDown:
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// TestInstanceMetadata verifies that the cloud provider correctly returns instance metadata
// including provider ID, instance type, zone, and node addresses.
func (a *AWSNodeTester) TestInstanceMetadata(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	for _, node := range nodes.Items {
		providerID := node.Spec.ProviderID
		if providerID == "" {
			klog.Warningf("Skipping node %s: no providerID", node.Name)
			continue
		}

		instanceID, err := parseAWSProviderID(providerID)
		if err != nil {
			return fmt.Errorf("failed to parse providerID for node %s: %w", node.Name, err)
		}

		// Get instance details from AWS
		metadata, err := a.getInstanceMetadata(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to get instance metadata for node %s: %w", node.Name, err)
		}

		// Verify instance type matches node label
		if nodeInstanceType, ok := node.Labels["node.kubernetes.io/instance-type"]; ok {
			if metadata.InstanceType != nodeInstanceType {
				return fmt.Errorf("instance type mismatch for node %s: AWS=%s, label=%s",
					node.Name, metadata.InstanceType, nodeInstanceType)
			}
			klog.Infof("Verified instance type for node %s: %s", node.Name, metadata.InstanceType)
		}

		// Verify zone matches node label
		if nodeZone, ok := node.Labels["topology.kubernetes.io/zone"]; ok {
			if metadata.Zone != nodeZone {
				return fmt.Errorf("zone mismatch for node %s: AWS=%s, label=%s",
					node.Name, metadata.Zone, nodeZone)
			}
			klog.Infof("Verified zone for node %s: %s", node.Name, metadata.Zone)
		}

		// Verify region matches node label
		if nodeRegion, ok := node.Labels["topology.kubernetes.io/region"]; ok {
			if metadata.Region != nodeRegion {
				return fmt.Errorf("region mismatch for node %s: AWS=%s, label=%s",
					node.Name, metadata.Region, nodeRegion)
			}
			klog.Infof("Verified region for node %s: %s", node.Name, metadata.Region)
		}

		// Verify at least one node address exists
		if len(node.Status.Addresses) == 0 {
			return fmt.Errorf("node %s has no addresses", node.Name)
		}

		// Verify private IP matches one of the node addresses
		foundPrivateIP := false
		for _, addr := range node.Status.Addresses {
			if addr.Type == v1.NodeInternalIP && addr.Address == metadata.PrivateIP {
				foundPrivateIP = true
				break
			}
		}
		if metadata.PrivateIP != "" && !foundPrivateIP {
			klog.Warningf("Private IP %s from AWS not found in node %s addresses", metadata.PrivateIP, node.Name)
		}

		klog.Infof("Verified metadata for node %s: type=%s, zone=%s, region=%s",
			node.Name, metadata.InstanceType, metadata.Zone, metadata.Region)
	}

	klog.Infof("Successfully verified instance metadata for all nodes")
	return nil
}

// InstanceMetadata holds the metadata retrieved from AWS for an instance
type InstanceMetadata struct {
	InstanceType string
	Zone         string
	Region       string
	PrivateIP    string
	PublicIP     string
}

// getInstanceMetadata retrieves metadata for an EC2 instance
func (a *AWSNodeTester) getInstanceMetadata(ctx context.Context, instanceID string) (*InstanceMetadata, error) {
	result, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return nil, err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			metadata := &InstanceMetadata{
				InstanceType: string(instance.InstanceType),
			}

			if instance.Placement != nil && instance.Placement.AvailabilityZone != nil {
				metadata.Zone = aws.ToString(instance.Placement.AvailabilityZone)
				// Extract region from zone (e.g., us-west-2a -> us-west-2)
				if len(metadata.Zone) > 0 {
					metadata.Region = metadata.Zone[:len(metadata.Zone)-1]
				}
			}

			if instance.PrivateIpAddress != nil {
				metadata.PrivateIP = aws.ToString(instance.PrivateIpAddress)
			}

			if instance.PublicIpAddress != nil {
				metadata.PublicIP = aws.ToString(instance.PublicIpAddress)
			}

			return metadata, nil
		}
	}

	return nil, fmt.Errorf("instance %s not found", instanceID)
}

// isMasterNode checks if a node is a master/control-plane node
// by looking for common master node labels
func isMasterNode(node *v1.Node) bool {
	// Check for master/control-plane labels
	masterLabels := []string{
		"node-role.kubernetes.io/master",
		"node-role.kubernetes.io/control-plane",
		"node.kubernetes.io/master",
	}

	for _, label := range masterLabels {
		if _, exists := node.Labels[label]; exists {
			return true
		}
	}

	// Also check the node-role label value
	if role, exists := node.Labels["kubernetes.io/role"]; exists {
		if role == "master" || role == "control-plane" {
			return true
		}
	}

	return false
}

// parseAWSProviderID extracts the instance ID from an AWS provider ID
// Expected formats:
// - aws:///zone/instance-id
// - aws://region/zone/instance-id
// - aws://instance-id (legacy)
func parseAWSProviderID(providerID string) (string, error) {
	const prefix = "aws://"
	if len(providerID) < len(prefix) {
		return "", fmt.Errorf("invalid providerID format: %s", providerID)
	}

	if providerID[:len(prefix)] != prefix {
		return "", fmt.Errorf("providerID does not have aws:// prefix: %s", providerID)
	}

	// Remove the "aws://" prefix
	remainder := providerID[len(prefix):]

	// Find the last '/' to extract the instance ID
	for i := len(remainder) - 1; i >= 0; i-- {
		if remainder[i] == '/' {
			// Found the last slash, everything after is the instance ID
			return remainder[i+1:], nil
		}
	}

	// If no slash found, the entire remainder is the instance ID (legacy format)
	if len(remainder) > 0 {
		return remainder, nil
	}

	return "", fmt.Errorf("could not extract instance ID from providerID: %s", providerID)
}
