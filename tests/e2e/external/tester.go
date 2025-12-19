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
	"k8s.io/kubernetes/test/e2e/cloud/external"
)

// AWSTester implements the Tester interface for AWS cloud provider.
// It acts as a factory that provides lazy initialization and caching of individual testers.
type AWSTester struct {
	ctx    context.Context
	region string

	// Cached testers (lazy initialization)
	nodeTester         *AWSNodeTester
	loadBalancerTester *AWSLoadBalancerTester
	zoneTester         *AWSZoneTester
}

// Ensure AWSTester implements Tester interface
var _ external.Tester = &AWSTester{}

// NewAWSTester creates a new AWSTester instance with the specified context and region.
func NewAWSTester(ctx context.Context, region string) *AWSTester {
	return &AWSTester{
		ctx:    ctx,
		region: region,
	}
}

// NodeTester returns the NodeTester implementation for AWS.
// AWSNodeTester implements both NodeTester and InstancesV2Tester interfaces.
func (a *AWSTester) NodeTester() (external.NodeTester, bool) {
	if a.nodeTester == nil {
		tester, err := newAWSNodeTesterWithRegion(a.ctx, a.region)
		if err != nil {
			return nil, false
		}
		a.nodeTester = tester
	}
	return a.nodeTester, true
}

// InstancesV2Tester returns the InstancesV2Tester implementation for AWS.
// AWSNodeTester implements both NodeTester and InstancesV2Tester interfaces.
func (a *AWSTester) InstancesV2Tester() (external.InstancesV2Tester, bool) {
	if a.nodeTester == nil {
		tester, err := newAWSNodeTesterWithRegion(a.ctx, a.region)
		if err != nil {
			return nil, false
		}
		a.nodeTester = tester
	}
	return a.nodeTester, true
}

// LoadBalancerTester returns the LoadBalancerTester implementation for AWS.
func (a *AWSTester) LoadBalancerTester() (external.LoadBalancerTester, bool) {
	if a.loadBalancerTester == nil {
		tester, err := newAWSLoadBalancerTesterWithRegion(a.ctx, a.region)
		if err != nil {
			return nil, false
		}
		a.loadBalancerTester = tester
	}
	return a.loadBalancerTester, true
}

// ZonesTester returns the ZonesTester implementation for AWS.
func (a *AWSTester) ZonesTester() (external.ZonesTester, bool) {
	if a.zoneTester == nil {
		tester, err := newAWSZoneTesterWithRegion(a.ctx, a.region)
		if err != nil {
			return nil, false
		}
		a.zoneTester = tester
	}
	return a.zoneTester, true
}

// InstancesTester returns the InstancesTester implementation for AWS.
// Currently not implemented, returns (nil, false).
func (a *AWSTester) InstancesTester() (external.InstancesTester, bool) {
	// InstancesTester is not implemented for AWS
	return nil, false
}

// RoutesTester returns the RoutesTester implementation for AWS.
// Currently not implemented, returns (nil, false).
func (a *AWSTester) RoutesTester() (external.RoutesTester, bool) {
	// RoutesTester is not implemented for AWS
	return nil, false
}

// ClustersTester returns the ClustersTester implementation for AWS.
// Currently not implemented, returns (nil, false).
func (a *AWSTester) ClustersTester() (external.ClustersTester, bool) {
	// ClustersTester is not implemented for AWS
	return nil, false
}

// ProviderName returns the name of the cloud provider.
func (a *AWSTester) ProviderName() string {
	return "aws"
}

