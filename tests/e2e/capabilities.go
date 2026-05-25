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

package e2e

import (
	cloudprovidertesting "k8s.io/cloud-provider/testing"
)

func init() {
	cloudprovidertesting.RegisterCapabilities("aws", &cloudprovidertesting.MapCapabilities{
		Name: "aws",
		Caps: map[cloudprovidertesting.Capability]bool{
			cloudprovidertesting.CapLoadBalancer:         true,
			cloudprovidertesting.CapInstances:            true,
			cloudprovidertesting.CapInstancesV2:          true,
			cloudprovidertesting.CapZones:                true,
			cloudprovidertesting.CapRoutes:               true,
			cloudprovidertesting.CapClusters:             false,
			cloudprovidertesting.CapNodeDeletion:         true,
			cloudprovidertesting.CapSSHAccess:            true,
			cloudprovidertesting.CapInternalLoadBalancer: true,
			cloudprovidertesting.CapVolumeProvisioning:   true,
			cloudprovidertesting.CapNodeResize:           false,
			cloudprovidertesting.CapTopologyLabels:       true,
		},
	})
}
