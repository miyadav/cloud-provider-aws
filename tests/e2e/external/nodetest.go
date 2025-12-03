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
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	cloudprovidertest "k8s.io/cloud-provider/test"
	"k8s.io/klog/v2"
)

// AWSNodeTester implements the NodeTester interface for AWS cloud provider
type AWSNodeTester struct {
	ec2Client *ec2.Client
}

// NewAWSNodeTester creates a new AWSNodeTester instance
func NewAWSNodeTester(ctx context.Context) (cloudprovidertest.NodeTester, error) {
	// Load AWS config from the environment
	cfg, err := config.LoadDefaultConfig(ctx)
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

	// Find a ready, schedulable node
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

	if testNode == nil {
		return fmt.Errorf("no ready schedulable nodes found")
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
