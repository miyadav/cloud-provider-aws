package external_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
)

func init() {
	testing.Init()

	// Kubeconfig default so the framework can connect when running go test ./external/ without flags.
	if os.Getenv("KUBECONFIG") == "" {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		_ = os.Setenv("KUBECONFIG", kubeconfig)
	}

	framework.RegisterCommonFlags(flag.CommandLine)
	framework.RegisterClusterFlags(flag.CommandLine)
	flag.Parse()
	framework.AfterReadingAllFlags(&framework.TestContext)
}

func TestExternal(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AWS CCM External Test Interface Suite")
}
