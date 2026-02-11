package external

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	cloudexternal "k8s.io/kubernetes/test/e2e/cloud/external"
)

// Compile-time check to ensure AWSNodeLifecycle implements cloudexternal.TestNodeLifecycleInterface
var _ cloudexternal.TestNodeLifecycleInterface = (*AWSNodeLifecycle)(nil)

// AWSNodeLifecycle implements TestNodeLifecycleInterface for AWS
// Provides methods to test node/instance lifecycle operations
type AWSNodeLifecycle struct {
	ec2Client  *ec2.Client
	ctx        context.Context
	nodeName   string
	providerID string
	instanceID string
}

// Exists checks if the node/instance exists in AWS
// Returns true if the instance can be found via DescribeInstances API
func (n *AWSNodeLifecycle) Exists() bool {
	if n.instanceID == "" {
		return false
	}

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{n.instanceID},
	}

	result, err := n.ec2Client.DescribeInstances(n.ctx, input)
	if err != nil {
		// Instance doesn't exist or error occurred (e.g. wrong region, credentials, or permissions)
		return false
	}

	// Check if we got any reservations and instances
	if len(result.Reservations) == 0 {
		return false
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil && *instance.InstanceId == n.instanceID {
				// Instance exists
				return true
			}
		}
	}

	return false
}

// IsShutdown checks if the instance is in a shutdown or terminated state
// Returns true if instance state is: shutting-down, terminated, stopping, or stopped
func (n *AWSNodeLifecycle) IsShutdown() bool {
	if n.instanceID == "" {
		return false
	}

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{n.instanceID},
	}

	result, err := n.ec2Client.DescribeInstances(n.ctx, input)
	if err != nil {
		// If we can't describe the instance, assume it's not shutdown
		return false
	}

	// Check instance state
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil && *instance.InstanceId == n.instanceID {
				state := instance.State
				if state != nil && state.Name != "" {
					// Check if instance is shutting-down, terminated, or stopping
					switch state.Name {
					case ec2types.InstanceStateNameShuttingDown,
						ec2types.InstanceStateNameTerminated,
						ec2types.InstanceStateNameStopping,
						ec2types.InstanceStateNameStopped:
						return true
					}
				}
			}
		}
	}

	return false
}

// Details returns detailed information about the node/instance
// Queries EC2 DescribeInstances API to get instance metadata
func (n *AWSNodeLifecycle) Details() cloudexternal.NodeDetails {
	details := cloudexternal.NodeDetails{
		InstanceID: n.instanceID,
		ProviderID: n.providerID,
		Metadata:   make(map[string]string),
	}

	if n.instanceID == "" {
		return details
	}

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{n.instanceID},
	}

	result, err := n.ec2Client.DescribeInstances(n.ctx, input)
	if err != nil {
		// Return partial details on error
		return details
	}

	// Populate details from EC2 response
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil && *instance.InstanceId == n.instanceID {
				if instance.InstanceType != "" {
					details.InstanceType = string(instance.InstanceType)
				}

				if instance.Placement != nil && instance.Placement.AvailabilityZone != nil {
					az := *instance.Placement.AvailabilityZone
					details.Metadata["availability-zone"] = az
					// Extract region from AZ (e.g., us-west-2a -> us-west-2)
					if len(az) > 0 {
						details.Metadata["region"] = az[:len(az)-1]
					}
				}

				if instance.State != nil && instance.State.Name != "" {
					details.Metadata["state"] = string(instance.State.Name)
				}

				break
			}
		}
	}

	return details
}

// Addresses returns all network addresses associated with the node/instance
// Returns slice of NodeAddress containing internal/external IPs and DNS names
func (n *AWSNodeLifecycle) Addresses() []cloudexternal.NodeAddress {
	var addresses []cloudexternal.NodeAddress

	if n.instanceID == "" {
		return addresses
	}

	input := &ec2.DescribeInstancesInput{
		InstanceIds: []string{n.instanceID},
	}

	result, err := n.ec2Client.DescribeInstances(n.ctx, input)
	if err != nil {
		// Return empty addresses on error
		return addresses
	}

	// Extract addresses from EC2 response
	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.InstanceId != nil && *instance.InstanceId == n.instanceID {
				// Add private IP address
				if instance.PrivateIpAddress != nil {
					addresses = append(addresses, cloudexternal.NodeAddress{
						Type:    "InternalIP",
						Address: *instance.PrivateIpAddress,
					})
				}

				// Add public IP address if available
				if instance.PublicIpAddress != nil {
					addresses = append(addresses, cloudexternal.NodeAddress{
						Type:    "ExternalIP",
						Address: *instance.PublicIpAddress,
					})
				}

				// Add private DNS name
				if instance.PrivateDnsName != nil && *instance.PrivateDnsName != "" {
					addresses = append(addresses, cloudexternal.NodeAddress{
						Type:    "InternalDNS",
						Address: *instance.PrivateDnsName,
					})
				}

				// Add public DNS name if available
				if instance.PublicDnsName != nil && *instance.PublicDnsName != "" {
					addresses = append(addresses, cloudexternal.NodeAddress{
						Type:    "ExternalDNS",
						Address: *instance.PublicDnsName,
					})
				}

				// Add hostname (use private DNS or instance ID)
				hostname := n.nodeName
				if hostname == "" && instance.PrivateDnsName != nil {
					hostname = *instance.PrivateDnsName
				}
				if hostname != "" {
					addresses = append(addresses, cloudexternal.NodeAddress{
						Type:    "Hostname",
						Address: hostname,
					})
				}

				break
			}
		}
	}

	return addresses
}

// String returns a string representation of the node lifecycle for debugging
func (n *AWSNodeLifecycle) String() string {
	return fmt.Sprintf("AWSNodeLifecycle{instanceID: %s, nodeName: %s, providerID: %s}",
		n.instanceID, n.nodeName, n.providerID)
}
