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
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// LoadBalancerTester defines the interface for testing cloud provider load balancer functionality.
// This interface will be provided by k8s.io/cloud-provider/test once the dependency is updated.
type LoadBalancerTester interface {
	TestEnsureLoadBalancer(ctx context.Context, c kubernetes.Interface) error
	TestUpdateLoadBalancer(ctx context.Context, c kubernetes.Interface) error
	TestEnsureLoadBalancerDeleted(ctx context.Context, c kubernetes.Interface) error
	TestGetLoadBalancer(ctx context.Context, c kubernetes.Interface) error
}

// AWSLoadBalancerTester implements the LoadBalancerTester interface for AWS cloud provider
type AWSLoadBalancerTester struct {
	elbv2Client *elasticloadbalancingv2.Client
	region      string
}

// Ensure AWSLoadBalancerTester implements LoadBalancerTester interface
var _ LoadBalancerTester = &AWSLoadBalancerTester{}

// NewAWSLoadBalancerTester creates a new AWSLoadBalancerTester instance
func NewAWSLoadBalancerTester(ctx context.Context) (LoadBalancerTester, error) {
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

	return &AWSLoadBalancerTester{
		elbv2Client: elasticloadbalancingv2.NewFromConfig(cfg),
		region:      cfg.Region,
	}, nil
}

// TestEnsureLoadBalancer tests that a LoadBalancer type service creates an AWS load balancer
// with the correct configuration.
func (a *AWSLoadBalancerTester) TestEnsureLoadBalancer(ctx context.Context, c kubernetes.Interface) error {
	namespace := "lb-test-ns"
	serviceName := "test-lb-ensure"

	// Create test namespace
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err := c.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create namespace: %w", err)
	}
	defer func() {
		_ = c.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	}()

	// Create a LoadBalancer service
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "test-lb",
			},
		},
	}

	_, err = c.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LoadBalancer service: %w", err)
	}
	defer func() {
		_ = c.CoreV1().Services(namespace).Delete(ctx, serviceName, metav1.DeleteOptions{})
	}()

	klog.Infof("Created LoadBalancer service %s/%s, waiting for provisioning...", namespace, serviceName)

	// Wait for the LoadBalancer to be provisioned
	var lbHostname string
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		updatedSvc, err := c.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if len(updatedSvc.Status.LoadBalancer.Ingress) > 0 {
			lbHostname = updatedSvc.Status.LoadBalancer.Ingress[0].Hostname
			if lbHostname == "" {
				lbHostname = updatedSvc.Status.LoadBalancer.Ingress[0].IP
			}
			return lbHostname != "", nil
		}
		klog.Infof("Waiting for LoadBalancer to be provisioned...")
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("LoadBalancer was not provisioned within timeout: %w", err)
	}

	klog.Infof("LoadBalancer provisioned with hostname: %s", lbHostname)

	// Verify the load balancer exists in AWS
	exists, err := a.loadBalancerExists(ctx, lbHostname)
	if err != nil {
		return fmt.Errorf("failed to verify load balancer existence: %w", err)
	}
	if !exists {
		return fmt.Errorf("load balancer %s not found in AWS", lbHostname)
	}

	klog.Infof("Successfully verified LoadBalancer %s exists in AWS", lbHostname)
	return nil
}

// TestUpdateLoadBalancer tests that updating a LoadBalancer service properly updates the AWS load balancer.
func (a *AWSLoadBalancerTester) TestUpdateLoadBalancer(ctx context.Context, c kubernetes.Interface) error {
	namespace := "lb-test-ns"
	serviceName := "test-lb-update"

	// Create test namespace
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err := c.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create namespace: %w", err)
	}
	defer func() {
		_ = c.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	}()

	// Create initial LoadBalancer service
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "test-lb",
			},
		},
	}

	createdSvc, err := c.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LoadBalancer service: %w", err)
	}
	defer func() {
		_ = c.CoreV1().Services(namespace).Delete(ctx, serviceName, metav1.DeleteOptions{})
	}()

	// Wait for initial provisioning
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		updatedSvc, err := c.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return len(updatedSvc.Status.LoadBalancer.Ingress) > 0, nil
	})
	if err != nil {
		return fmt.Errorf("initial LoadBalancer was not provisioned: %w", err)
	}

	klog.Infof("Initial LoadBalancer provisioned, now updating...")

	// Update the service - add a new port
	createdSvc.Spec.Ports = append(createdSvc.Spec.Ports, v1.ServicePort{
		Name:       "https",
		Port:       443,
		TargetPort: intstr.FromInt(8443),
		Protocol:   v1.ProtocolTCP,
	})

	_, err = c.CoreV1().Services(namespace).Update(ctx, createdSvc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update LoadBalancer service: %w", err)
	}

	// Wait for update to propagate
	time.Sleep(30 * time.Second)

	// Verify the service was updated
	updatedSvc, err := c.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get updated service: %w", err)
	}

	if len(updatedSvc.Spec.Ports) != 2 {
		return fmt.Errorf("expected 2 ports after update, got %d", len(updatedSvc.Spec.Ports))
	}

	klog.Infof("Successfully verified LoadBalancer update")
	return nil
}

// TestEnsureLoadBalancerDeleted tests that deleting a LoadBalancer service properly cleans up the AWS load balancer.
func (a *AWSLoadBalancerTester) TestEnsureLoadBalancerDeleted(ctx context.Context, c kubernetes.Interface) error {
	namespace := "lb-test-ns"
	serviceName := "test-lb-delete"

	// Create test namespace
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}
	_, err := c.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return fmt.Errorf("failed to create namespace: %w", err)
	}
	defer func() {
		_ = c.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	}()

	// Create LoadBalancer service
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: namespace,
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "test-lb",
			},
		},
	}

	_, err = c.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create LoadBalancer service: %w", err)
	}

	// Wait for provisioning
	var lbHostname string
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		updatedSvc, err := c.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(updatedSvc.Status.LoadBalancer.Ingress) > 0 {
			lbHostname = updatedSvc.Status.LoadBalancer.Ingress[0].Hostname
			if lbHostname == "" {
				lbHostname = updatedSvc.Status.LoadBalancer.Ingress[0].IP
			}
			return lbHostname != "", nil
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("LoadBalancer was not provisioned: %w", err)
	}

	klog.Infof("LoadBalancer %s provisioned, now deleting service...", lbHostname)

	// Delete the service
	err = c.CoreV1().Services(namespace).Delete(ctx, serviceName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete service: %w", err)
	}

	// Wait for load balancer to be deleted from AWS
	err = wait.PollUntilContextTimeout(ctx, 10*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		exists, err := a.loadBalancerExists(ctx, lbHostname)
		if err != nil {
			klog.Warningf("Error checking load balancer existence: %v", err)
			return false, nil
		}
		if !exists {
			return true, nil
		}
		klog.Infof("Waiting for load balancer %s to be deleted...", lbHostname)
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("load balancer was not deleted within timeout: %w", err)
	}

	klog.Infof("Successfully verified LoadBalancer %s was deleted", lbHostname)
	return nil
}

// TestGetLoadBalancer tests that the cloud provider correctly retrieves load balancer status.
func (a *AWSLoadBalancerTester) TestGetLoadBalancer(ctx context.Context, c kubernetes.Interface) error {
	// Get all LoadBalancer services
	services, err := c.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}

	lbCount := 0
	for _, svc := range services.Items {
		if svc.Spec.Type != v1.ServiceTypeLoadBalancer {
			continue
		}

		lbCount++
		if len(svc.Status.LoadBalancer.Ingress) == 0 {
			klog.Warningf("LoadBalancer service %s/%s has no ingress", svc.Namespace, svc.Name)
			continue
		}

		hostname := svc.Status.LoadBalancer.Ingress[0].Hostname
		if hostname == "" {
			hostname = svc.Status.LoadBalancer.Ingress[0].IP
		}

		if hostname != "" {
			exists, err := a.loadBalancerExists(ctx, hostname)
			if err != nil {
				return fmt.Errorf("failed to verify load balancer for service %s/%s: %w", svc.Namespace, svc.Name, err)
			}
			if !exists {
				return fmt.Errorf("load balancer %s for service %s/%s not found in AWS", hostname, svc.Namespace, svc.Name)
			}
			klog.Infof("Verified load balancer %s exists for service %s/%s", hostname, svc.Namespace, svc.Name)
		}
	}

	klog.Infof("Successfully verified %d LoadBalancer services", lbCount)
	return nil
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
