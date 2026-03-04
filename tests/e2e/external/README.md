# AWS Cloud Provider TestInterface Implementation

## Overview

This directory implements the `cloudexternal.TestInterface` contract from the Kubernetes fork (`k8s.io/kubernetes/test/e2e/cloud/external`). This interface provides a standardized framework for testing Cloud Controller Manager (CCM) implementations across different cloud providers.

## Interface Contract

The `TestInterface` defines a set of cloud provider capabilities that can be tested in E2E environments. Each method returns a tuple `(implemented bool, interface)` indicating whether the capability is supported and providing the implementation if available:

- `NodeLifecycle()` - Node/instance lifecycle operations
- `LoadBalancer()` - Load balancer management
- `Routes()` - Network route management
- `Topology()` - Topology and zone information
- `Cluster()` - Cluster metadata

## Implementation Architecture

### AWSTestInterface (`ccmtests.go`)

The main entry point implementing `cloudexternal.TestInterface`. It uses compile-time verification to ensure contract compliance:

```go
var _ cloudexternal.TestInterface = (*AWSTestInterface)(nil)
```

This ensures all required methods are implemented correctly. The struct holds AWS-specific context including EC2 client, node identifiers, and provider ID.

### AWSNodeLifecycle (`node_lifecycle.go`)

Implements `TestNodeLifecycleInterface` with four key methods:

**Exists()** - Queries AWS EC2 `DescribeInstances` API to verify instance existence. Returns `true` if found, `false` otherwise.

**IsShutdown()** - Checks instance state via EC2 API. Returns `true` for states: `shutting-down`, `terminated`, `stopping`, or `stopped`.

**Details()** - Returns comprehensive instance information including instance type, availability zone, region, and current state. Data is structured as `NodeDetails` with extensible metadata map.

**Addresses()** - Retrieves all network addresses from EC2 instance data, mapping them to Kubernetes address types: `InternalIP` (private IP), `ExternalIP` (public IP), `InternalDNS`, `ExternalDNS`, and `Hostname`.

Each method includes compile-time verification:
```go
var _ cloudexternal.TestNodeLifecycleInterface = (*AWSNodeLifecycle)(nil)
```

## Key Design Principles

### Graceful Error Handling

Methods follow a "safe defaults" approach rather than panicking on errors. API failures return conservative values (e.g., `Exists()` returns `false`, `Addresses()` returns empty slice), allowing tests to continue meaningfully.

### Regional Awareness

EC2 is a regional service requiring region-specific clients. The implementation extracts region from provider ID (format: `aws:///zone/instance-id`) where zone contains region information (e.g., `us-east-2a` → `us-east-2`).

### Type Safety

Compile-time interface checks ensure breaking changes in upstream contracts are caught immediately during development, not at runtime.

## Current Status

**Implemented:** NodeLifecycle interface with full method coverage

**Not Implemented (POC):** LoadBalancer, Routes, Topology, and Cluster interfaces return `(false, nil)` as placeholders for future development

## Testing

The test suite (`ccmtests_test.go`) uses Ginkgo/Gomega framework with Kubernetes E2E integration. Tests verify interface implementation correctness, node existence, shutdown states, instance details retrieval, and network address mapping against live AWS infrastructure.

Tests automatically discover nodes in the cluster, extract provider IDs, create region-specific EC2 clients, and validate all NodeLifecycle methods against real AWS API responses.

## Usage
cd tests/e2e

```bash
# Run tests with kubeconfig
go test ./tests/e2e/external/

Or use -
ginkgo  -vv --progress --timeout=30m ./external/... 

# Requires AWS credentials and running Kubernetes cluster on AWS
```
