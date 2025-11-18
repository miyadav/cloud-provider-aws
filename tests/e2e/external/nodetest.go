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

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	v1 "k8s.io/api/core/v1"
	cloudprovidertest "k8s.io/cloud-provider/test"
	k8sexternal "k8s.io/kubernetes/test/e2e/cloud/external"
)

// AWSNodeTester implements the NodeTester interface for AWS cloud provider
// by embedding the generic CCMNodeTester and providing AWS-specific implementations
type AWSNodeTester struct {
	*k8sexternal.CCMNodeTester
	ec2Client *ec2.Client
}

// NewAWSNodeTester creates a new AWSNodeTester instance
func NewAWSNodeTester(ctx context.Context) (cloudprovidertest.NodeTester, error) {
	// Load AWS config from the environment
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create the AWS node tester with embedded CCMNodeTester
	awsTester := &AWSNodeTester{
		CCMNodeTester: k8sexternal.NewCCMNodeTester().(*k8sexternal.CCMNodeTester),
		ec2Client:     ec2.NewFromConfig(cfg),
	}

	// Set the AWS tester as the implementation for the embedded CCMNodeTester
	// This allows the generic test logic to call our AWS-specific DeleteNodeOnCloudProvider
	awsTester.CCMNodeTester.SetNodeTester(awsTester)

	return awsTester, nil
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
