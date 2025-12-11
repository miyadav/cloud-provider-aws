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
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"k8s.io/kubernetes/test/e2e/cloud/external"
)

// AWSLoadBalancerTester implements the LoadBalancerTester interface for AWS cloud provider
// It embeds CCMLoadBalancerTester to use the default test implementation and only
// implements cloud-specific operations via LoadBalancerVerifier
type AWSLoadBalancerTester struct {
	*external.CCMLoadBalancerTester
	elbv2Client *elasticloadbalancingv2.Client
	region      string
}

// Ensure AWSLoadBalancerTester implements LoadBalancerTester and LoadBalancerVerifier interfaces
var _ external.LoadBalancerTester = &AWSLoadBalancerTester{}
var _ external.LoadBalancerVerifier = &AWSLoadBalancerTester{}

// NewAWSLoadBalancerTester creates a new AWSLoadBalancerTester instance
func NewAWSLoadBalancerTester(ctx context.Context) (external.LoadBalancerTester, error) {
	return newAWSLoadBalancerTesterWithRegion(ctx, "")
}

// newAWSLoadBalancerTesterConcrete creates a new AWSLoadBalancerTester instance and returns the concrete type
func newAWSLoadBalancerTesterConcrete(ctx context.Context) (*AWSLoadBalancerTester, error) {
	return newAWSLoadBalancerTesterWithRegion(ctx, "")
}

// newAWSLoadBalancerTesterWithRegion creates a new AWSLoadBalancerTester instance with explicit region
func newAWSLoadBalancerTesterWithRegion(ctx context.Context, region string) (*AWSLoadBalancerTester, error) {
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

	ccmTester := external.NewCCMLoadBalancerTester()
	// Type assert to get the concrete type
	ccmLBTester, ok := ccmTester.(*external.CCMLoadBalancerTester)
	if !ok {
		return nil, fmt.Errorf("failed to get CCMLoadBalancerTester instance")
	}

	awsTester := &AWSLoadBalancerTester{
		CCMLoadBalancerTester: ccmLBTester,
		elbv2Client:           elasticloadbalancingv2.NewFromConfig(cfg),
		region:                cfg.Region,
	}
	// Set the verifier so CCMLoadBalancerTester can call our VerifyLoadBalancerExists
	awsTester.SetLoadBalancerVerifier(awsTester)
	return awsTester, nil
}

// VerifyLoadBalancerExists checks if a load balancer exists in AWS by DNS name
// This implements the LoadBalancerVerifier interface for cloud-specific verification
func (a *AWSLoadBalancerTester) VerifyLoadBalancerExists(ctx context.Context, hostnameOrIP string) (bool, error) {
	return a.loadBalancerExists(ctx, hostnameOrIP)
}

// loadBalancerExists checks if a load balancer exists in AWS by DNS name
func (a *AWSLoadBalancerTester) loadBalancerExists(ctx context.Context, dnsName string) (bool, error) {
	// Check ELBv2 (NLB/ALB)
	return a.elbv2LoadBalancerExists(ctx, dnsName)
}

// elbv2LoadBalancerExists checks if an NLB/ALB exists by DNS name
func (a *AWSLoadBalancerTester) elbv2LoadBalancerExists(ctx context.Context, dnsName string) (bool, error) {
	paginator := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(a.elbv2Client, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return false, err
		}

		for _, lb := range page.LoadBalancers {
			if aws.ToString(lb.DNSName) == dnsName {
				return lb.State != nil && lb.State.Code != elbv2types.LoadBalancerStateEnumFailed, nil
			}
		}
	}
	return false, nil
}

// The following test methods are now provided by CCMLoadBalancerTester:
// - TestEnsureLoadBalancer
// - TestUpdateLoadBalancer
// - TestEnsureLoadBalancerDeleted
// - TestGetLoadBalancer
// - TestGetLoadBalancerName
// We only need to implement LoadBalancerVerifier.VerifyLoadBalancerExists above.

// GetLoadBalancerInfo returns information about a load balancer by DNS name
func (a *AWSLoadBalancerTester) GetLoadBalancerInfo(ctx context.Context, dnsName string) (*LoadBalancerInfo, error) {
	// Get ELBv2 info
	return a.getELBv2Info(ctx, dnsName)
}

// LoadBalancerInfo holds information about a load balancer
type LoadBalancerInfo struct {
	Name      string
	DNSName   string
	Type      string
	State     string
	VPCId     string
	Scheme    string
	Listeners []ListenerInfo
}

// ListenerInfo holds information about a load balancer listener
type ListenerInfo struct {
	Port     int32
	Protocol string
}

func (a *AWSLoadBalancerTester) getELBv2Info(ctx context.Context, dnsName string) (*LoadBalancerInfo, error) {
	paginator := elasticloadbalancingv2.NewDescribeLoadBalancersPaginator(a.elbv2Client, &elasticloadbalancingv2.DescribeLoadBalancersInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}

		for _, lb := range page.LoadBalancers {
			if aws.ToString(lb.DNSName) == dnsName {
				info := &LoadBalancerInfo{
					Name:    aws.ToString(lb.LoadBalancerName),
					DNSName: aws.ToString(lb.DNSName),
					Type:    string(lb.Type),
					VPCId:   aws.ToString(lb.VpcId),
					Scheme:  string(lb.Scheme),
				}
				if lb.State != nil {
					info.State = string(lb.State.Code)
				}

				// Get listeners
				listeners, err := a.elbv2Client.DescribeListeners(ctx, &elasticloadbalancingv2.DescribeListenersInput{
					LoadBalancerArn: lb.LoadBalancerArn,
				})
				if err == nil {
					for _, l := range listeners.Listeners {
						info.Listeners = append(info.Listeners, ListenerInfo{
							Port:     aws.ToInt32(l.Port),
							Protocol: string(l.Protocol),
						})
					}
				}

				return info, nil
			}
		}
	}
	return nil, nil
}
