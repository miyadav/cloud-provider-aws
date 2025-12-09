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

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// ZoneTester defines the interface for testing cloud provider zone functionality.
// This interface will be provided by k8s.io/cloud-provider/test once the dependency is updated.
type ZoneTester interface {
	TestGetZone(ctx context.Context, c kubernetes.Interface) error
	TestGetZoneByProviderID(ctx context.Context, c kubernetes.Interface) error
	TestGetZoneByNodeName(ctx context.Context, c kubernetes.Interface) error
}

// AWSZoneTester implements the ZoneTester interface for AWS cloud provider
type AWSZoneTester struct {
	ec2Client *ec2.Client
	region    string
}

// Ensure AWSZoneTester implements ZoneTester interface
var _ ZoneTester = &AWSZoneTester{}

// NewAWSZoneTester creates a new AWSZoneTester instance
func NewAWSZoneTester(ctx context.Context) (ZoneTester, error) {
	return newAWSZoneTesterWithRegion(ctx, "")
}

// newAWSZoneTesterConcrete creates a new AWSZoneTester instance and returns the concrete type
func newAWSZoneTesterConcrete(ctx context.Context) (*AWSZoneTester, error) {
	return newAWSZoneTesterWithRegion(ctx, "")
}

// newAWSZoneTesterWithRegion creates a new AWSZoneTester instance with explicit region
func newAWSZoneTesterWithRegion(ctx context.Context, region string) (*AWSZoneTester, error) {
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

	return &AWSZoneTester{
		ec2Client: ec2.NewFromConfig(cfg),
		region:    cfg.Region,
	}, nil
}

// TestGetZone verifies that the cloud provider correctly returns zone information
// for the current instance using instance metadata.
func (a *AWSZoneTester) TestGetZone(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes to verify zone information
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	// Get available zones in the region
	availableZones, err := a.getAvailableZones(ctx)
	if err != nil {
		return fmt.Errorf("failed to get available zones: %w", err)
	}

	klog.Infof("Available zones in region %s: %v", a.region, availableZones)

	for _, node := range nodes.Items {
		// Check zone label
		zone, hasZone := node.Labels["topology.kubernetes.io/zone"]
		if !hasZone {
			// Try legacy label
			zone, hasZone = node.Labels["failure-domain.beta.kubernetes.io/zone"]
		}

		if !hasZone {
			klog.Warningf("Node %s does not have zone label", node.Name)
			continue
		}

		// Verify zone is in the list of available zones
		if !contains(availableZones, zone) {
			return fmt.Errorf("node %s has invalid zone %s (not in available zones)", node.Name, zone)
		}

		klog.Infof("Verified zone for node %s: %s", node.Name, zone)
	}

	klog.Infof("Successfully verified zone information for all nodes")
	return nil
}

// TestGetZoneByProviderID verifies that the cloud provider correctly returns zone
// information when given a provider ID.
func (a *AWSZoneTester) TestGetZoneByProviderID(ctx context.Context, c kubernetes.Interface) error {
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

		// Parse instance ID from provider ID
		instanceID, err := parseAWSProviderID(providerID)
		if err != nil {
			return fmt.Errorf("failed to parse providerID for node %s: %w", node.Name, err)
		}

		// Get zone from AWS
		awsZone, err := a.getInstanceZone(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to get zone for instance %s: %w", instanceID, err)
		}

		// Get zone from node label
		nodeZone, hasZone := node.Labels["topology.kubernetes.io/zone"]
		if !hasZone {
			nodeZone, hasZone = node.Labels["failure-domain.beta.kubernetes.io/zone"]
		}

		if hasZone && awsZone != nodeZone {
			return fmt.Errorf("zone mismatch for node %s: AWS=%s, label=%s", node.Name, awsZone, nodeZone)
		}

		klog.Infof("Verified zone by providerID for node %s: %s", node.Name, awsZone)
	}

	klog.Infof("Successfully verified GetZoneByProviderID for all nodes")
	return nil
}

// TestGetZoneByNodeName verifies that the cloud provider correctly returns zone
// information when given a node name.
func (a *AWSZoneTester) TestGetZoneByNodeName(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	for _, node := range nodes.Items {
		// Get the node's provider ID to find the instance
		providerID := node.Spec.ProviderID
		if providerID == "" {
			klog.Warningf("Skipping node %s: no providerID", node.Name)
			continue
		}

		instanceID, err := parseAWSProviderID(providerID)
		if err != nil {
			return fmt.Errorf("failed to parse providerID for node %s: %w", node.Name, err)
		}

		// Try to find instance by private DNS name (common node naming pattern)
		// AWS nodes are often named by their private DNS name
		awsZone, err := a.getInstanceZone(ctx, instanceID)
		if err != nil {
			return fmt.Errorf("failed to get zone for node %s (instance %s): %w", node.Name, instanceID, err)
		}

		// Verify zone label matches
		nodeZone, hasZone := node.Labels["topology.kubernetes.io/zone"]
		if !hasZone {
			nodeZone, hasZone = node.Labels["failure-domain.beta.kubernetes.io/zone"]
		}

		if hasZone && awsZone != nodeZone {
			return fmt.Errorf("zone mismatch for node %s: AWS=%s, label=%s", node.Name, awsZone, nodeZone)
		}

		klog.Infof("Verified zone by node name for %s: %s", node.Name, awsZone)
	}

	klog.Infof("Successfully verified GetZoneByNodeName for all nodes")
	return nil
}

// TestZoneIDLabel verifies that nodes have the AWS-specific zone-id topology label
func (a *AWSZoneTester) TestZoneIDLabel(ctx context.Context, c kubernetes.Interface) error {
	// Get list of nodes
	nodes, err := c.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list nodes: %w", err)
	}

	if len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes available for testing")
	}

	// Get zone ID mappings from AWS
	zoneIDMap, err := a.getZoneIDMap(ctx)
	if err != nil {
		return fmt.Errorf("failed to get zone ID mappings: %w", err)
	}

	klog.Infof("Zone ID mappings: %v", zoneIDMap)

	for _, node := range nodes.Items {
		// Check for zone-id label
		zoneID, hasZoneID := node.Labels["topology.k8s.aws/zone-id"]
		if !hasZoneID {
			klog.Warningf("Node %s does not have topology.k8s.aws/zone-id label", node.Name)
			continue
		}

		// Get the zone name from label
		zoneName, hasZone := node.Labels["topology.kubernetes.io/zone"]
		if !hasZone {
			zoneName, hasZone = node.Labels["failure-domain.beta.kubernetes.io/zone"]
		}

		if hasZone {
			// Verify zone ID matches the expected ID for the zone name
			expectedZoneID, ok := zoneIDMap[zoneName]
			if ok && zoneID != expectedZoneID {
				return fmt.Errorf("zone ID mismatch for node %s zone %s: got %s, expected %s",
					node.Name, zoneName, zoneID, expectedZoneID)
			}
		}

		klog.Infof("Verified zone-id label for node %s: %s", node.Name, zoneID)
	}

	klog.Infof("Successfully verified zone-id labels for all nodes")
	return nil
}

// getAvailableZones returns the list of available zones in the region
func (a *AWSZoneTester) getAvailableZones(ctx context.Context) ([]string, error) {
	result, err := a.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, err
	}

	var zones []string
	for _, az := range result.AvailabilityZones {
		if az.ZoneName != nil {
			zones = append(zones, aws.ToString(az.ZoneName))
		}
	}

	return zones, nil
}

// getZoneIDMap returns a mapping of zone names to zone IDs
func (a *AWSZoneTester) getZoneIDMap(ctx context.Context) (map[string]string, error) {
	result, err := a.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
	if err != nil {
		return nil, err
	}

	zoneMap := make(map[string]string)
	for _, az := range result.AvailabilityZones {
		if az.ZoneName != nil && az.ZoneId != nil {
			zoneMap[aws.ToString(az.ZoneName)] = aws.ToString(az.ZoneId)
		}
	}

	return zoneMap, nil
}

// getInstanceZone returns the availability zone of an EC2 instance
func (a *AWSZoneTester) getInstanceZone(ctx context.Context, instanceID string) (string, error) {
	result, err := a.ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
		InstanceIds: []string{instanceID},
	})
	if err != nil {
		return "", err
	}

	for _, reservation := range result.Reservations {
		for _, instance := range reservation.Instances {
			if instance.Placement != nil && instance.Placement.AvailabilityZone != nil {
				return aws.ToString(instance.Placement.AvailabilityZone), nil
			}
		}
	}

	return "", fmt.Errorf("instance %s not found or has no placement info", instanceID)
}

// getRegionFromZone extracts the region from an availability zone name
func getRegionFromZone(zone string) string {
	if len(zone) == 0 {
		return ""
	}
	// Zone format: region + zone suffix (e.g., us-west-2a -> us-west-2)
	// Find the last character that's a letter (the zone suffix)
	for i := len(zone) - 1; i >= 0; i-- {
		if zone[i] >= 'a' && zone[i] <= 'z' {
			return zone[:i]
		}
	}
	return zone
}

// ZoneInfo holds information about an AWS availability zone
type ZoneInfo struct {
	ZoneName string
	ZoneID   string
	Region   string
	State    string
}

// GetZoneInfo returns detailed information about a zone
func (a *AWSZoneTester) GetZoneInfo(ctx context.Context, zoneName string) (*ZoneInfo, error) {
	result, err := a.ec2Client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{
		ZoneNames: []string{zoneName},
	})
	if err != nil {
		return nil, err
	}

	if len(result.AvailabilityZones) == 0 {
		return nil, fmt.Errorf("zone %s not found", zoneName)
	}

	az := result.AvailabilityZones[0]
	return &ZoneInfo{
		ZoneName: aws.ToString(az.ZoneName),
		ZoneID:   aws.ToString(az.ZoneId),
		Region:   aws.ToString(az.RegionName),
		State:    string(az.State),
	}, nil
}

// contains checks if a string slice contains a specific string
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// Ensure the helper function to extract region is consistent with zone parsing
func extractRegionFromProviderID(providerID string) (string, error) {
	// Provider ID format: aws://region/zone/instance-id or aws:///zone/instance-id
	const prefix = "aws://"
	if !strings.HasPrefix(providerID, prefix) {
		return "", fmt.Errorf("invalid providerID format: %s", providerID)
	}

	remainder := providerID[len(prefix):]
	parts := strings.Split(remainder, "/")

	// Format: aws://region/zone/instance-id
	if len(parts) >= 2 && parts[0] != "" {
		return parts[0], nil
	}

	// Format: aws:///zone/instance-id - need to derive region from zone
	if len(parts) >= 2 {
		zone := parts[1]
		if zone != "" {
			return getRegionFromZone(zone), nil
		}
	}

	return "", fmt.Errorf("could not extract region from providerID: %s", providerID)
}
