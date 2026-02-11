package external

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	cloudexternal "k8s.io/kubernetes/test/e2e/cloud/external"
)

// Compile-time check to ensure AWSTestInterface implements cloudexternal.TestInterface
var _ cloudexternal.TestInterface = (*AWSTestInterface)(nil)

// AWSTestInterface implements TestInterface for AWS cloud provider
// This is the main entry point for CCM test interface implementation
type AWSTestInterface struct {
	ec2Client        *ec2.Client
	ctx              context.Context
	nodeName         string
	providerID       string
	instanceID       string
	availabilityZone string
}

// NewAWSTestInterface creates a new AWS test interface implementation
// Parameters:
//   - ctx: context for AWS API calls
//   - ec2Client: AWS EC2 client for instance operations
//   - nodeName: Kubernetes node name
//   - providerID: AWS provider ID (format: aws:///zone/instance-id)
func NewAWSTestInterface(ctx context.Context, ec2Client *ec2.Client, nodeName, providerID string) (*AWSTestInterface, error) {
	// Extract instance ID from provider ID (format: aws:///zone/instance-id)
	instanceID := extractInstanceIDFromProviderID(providerID)

	return &AWSTestInterface{
		ec2Client:  ec2Client,
		ctx:        ctx,
		nodeName:   nodeName,
		providerID: providerID,
		instanceID: instanceID,
	}, nil
}

// NodeLifecycle returns the node lifecycle test interface implementation
// This is the only method fully implemented for the POC
// Returns:
//   - implemented: true (this method is implemented)
//   - iface: AWSNodeLifecycle implementation
func (a *AWSTestInterface) NodeLifecycle() (implemented bool, iface cloudexternal.TestNodeLifecycleInterface) {
	return true, &AWSNodeLifecycle{
		ec2Client:  a.ec2Client,
		ctx:        a.ctx,
		nodeName:   a.nodeName,
		providerID: a.providerID,
		instanceID: a.instanceID,
	}
}

// LoadBalancer returns false for POC - not implemented
// Returns:
//   - implemented: false
//   - iface: nil
func (a *AWSTestInterface) LoadBalancer() (implemented bool, iface cloudexternal.TestLoadBalancerInterface) {
	return false, nil
}

// Routes returns false for POC - not implemented
// Returns:
//   - implemented: false
//   - iface: nil
func (a *AWSTestInterface) Routes() (implemented bool, iface cloudexternal.TestRoutesInterface) {
	return false, nil
}

// Topology returns false for POC - not implemented
// Returns:
//   - implemented: false
//   - iface: nil
func (a *AWSTestInterface) Topology() (implemented bool, iface cloudexternal.TestTopologyInterface) {
	return false, nil
}

// Cluster returns false for POC - not implemented
// Returns:
//   - implemented: false
//   - iface: nil
func (a *AWSTestInterface) Cluster() (implemented bool, iface cloudexternal.TestClusterInterface) {
	return false, nil
}

// extractInstanceIDFromProviderID extracts the instance ID from AWS provider ID
// Provider ID format: aws:///zone/instance-id or aws://zone/instance-id
// Example: aws:///us-west-2a/i-1234567890abcdef0 -> i-1234567890abcdef0
func extractInstanceIDFromProviderID(providerID string) string {
	if len(providerID) == 0 {
		return ""
	}

	// Find the last '/' and extract everything after it
	lastSlash := -1
	for i := len(providerID) - 1; i >= 0; i-- {
		if providerID[i] == '/' {
			lastSlash = i
			break
		}
	}

	if lastSlash >= 0 && lastSlash < len(providerID)-1 {
		return providerID[lastSlash+1:]
	}

	// If no '/' found, return the original string
	return providerID
}
