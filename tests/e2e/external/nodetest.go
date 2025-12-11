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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/test/e2e/cloud/external"
)

// AWSNodeTester implements the NodeTester and InstancesV2Tester interfaces for AWS cloud provider
// It embeds CCMNodeTester and CCMInstancesV2Tester to use the default test implementations and only
// implements cloud-specific operations via NodeTester and InstanceV2Verifier
type AWSNodeTester struct {
	*external.CCMNodeTester
	*external.CCMInstancesV2Tester
	ec2Client *ec2.Client
}

// Ensure AWSNodeTester implements NodeTester and InstancesV2Tester interfaces
var _ external.NodeTester = &AWSNodeTester{}
var _ external.InstancesV2Tester = &AWSNodeTester{}

// NewAWSNodeTester creates a new AWSNodeTester instance that implements NodeTester
func NewAWSNodeTester(ctx context.Context) (external.NodeTester, error) {
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

	ccmNodeTester := external.NewCCMNodeTester()
	// Type assert to get the concrete type
	ccmNode, ok := ccmNodeTester.(*external.CCMNodeTester)
	if !ok {
		return nil, fmt.Errorf("failed to get CCMNodeTester instance")
	}

	ccmInstancesV2Tester := external.NewCCMInstancesV2Tester()
	// Type assert to get the concrete type
	ccmInstancesV2, ok := ccmInstancesV2Tester.(*external.CCMInstancesV2Tester)
	if !ok {
		return nil, fmt.Errorf("failed to get CCMInstancesV2Tester instance")
	}

	awsTester := &AWSNodeTester{
		CCMNodeTester:        ccmNode,
		CCMInstancesV2Tester: ccmInstancesV2,
		ec2Client:            ec2.NewFromConfig(cfg),
	}
	// Set the node tester so CCMNodeTester can call our DeleteNodeOnCloudProvider
	awsTester.SetNodeTester(awsTester)
	// Set the instance verifier so CCMInstancesV2Tester can call our InstanceV2Verifier methods
	awsTester.SetInstanceV2Verifier(awsTester)
	return awsTester, nil
}

// DeleteNodeOnCloudProvider deletes the specified node from AWS by terminating the EC2 instance
// This is the cloud-specific implementation required by the NodeTester interface
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

// The following test methods are now provided by CCMInstancesV2Tester:
// - TestInstanceExists
// - TestInstanceShutdown
// - TestInstanceMetadata
// We only need to implement InstanceV2Verifier methods below.

// VerifyInstanceExists checks if an instance exists in AWS for the given node.
// This implements the InstanceV2Verifier interface for cloud-specific verification
func (a *AWSNodeTester) VerifyInstanceExists(ctx context.Context, node *v1.Node) (bool, error) {
	if node == nil {
		return false, fmt.Errorf("node is nil")
	}

	providerID := node.Spec.ProviderID
	if providerID == "" {
		return false, fmt.Errorf("node %s has no providerID", node.Name)
	}

	instanceID, err := parseAWSProviderID(providerID)
	if err != nil {
		return false, fmt.Errorf("failed to parse providerID: %w", err)
	}

	return a.instanceExists(ctx, instanceID)
}

// VerifyInstanceShutdown checks if an instance is shutdown in AWS for the given node.
// This implements the InstanceV2Verifier interface for cloud-specific verification
func (a *AWSNodeTester) VerifyInstanceShutdown(ctx context.Context, node *v1.Node) (bool, error) {
	if node == nil {
		return false, fmt.Errorf("node is nil")
	}

	providerID := node.Spec.ProviderID
	if providerID == "" {
		return false, fmt.Errorf("node %s has no providerID", node.Name)
	}

	instanceID, err := parseAWSProviderID(providerID)
	if err != nil {
		return false, fmt.Errorf("failed to parse providerID: %w", err)
	}

	return a.isInstanceShutdown(ctx, instanceID)
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

// GetInstanceMetadata retrieves instance metadata from AWS for the given node.
// This implements the InstanceV2Verifier interface for cloud-specific verification
func (a *AWSNodeTester) GetInstanceMetadata(ctx context.Context, node *v1.Node) (map[string]interface{}, error) {
	if node == nil {
		return nil, fmt.Errorf("node is nil")
	}

	providerID := node.Spec.ProviderID
	if providerID == "" {
		return nil, fmt.Errorf("node %s has no providerID", node.Name)
	}

	instanceID, err := parseAWSProviderID(providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse providerID: %w", err)
	}

	metadata, err := a.getInstanceMetadata(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// Convert to map[string]interface{} format expected by InstanceV2Verifier
	result := map[string]interface{}{
		"instanceType": metadata.InstanceType,
		"zone":         metadata.Zone,
		"region":       metadata.Region,
		"privateIP":    metadata.PrivateIP,
		"publicIP":     metadata.PublicIP,
	}

	return result, nil
}

// instanceExists checks if an EC2 instance exists
func (a *AWSNodeTester) instanceExists(ctx context.Context, instanceID string) (bool, error) {
	result, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return false, err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.State != nil && instance.State.Name != ec2types.InstanceStateNameTerminated {
				return true, nil
			}
		}
	}

	return false, nil
}

// The following test methods are now provided by CCMInstancesV2Tester:
// - TestInstanceExists
// - TestInstanceShutdown
// - TestInstanceMetadata
// All duplicate implementations have been removed.

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
