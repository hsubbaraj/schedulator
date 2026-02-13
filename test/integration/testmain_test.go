//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
)

const (
	kindClusterName  = "schedulator-test"
	kindClusterName2 = "schedulator-test-2"
)

var (
	testClientset  kubernetes.Interface
	testClientset2 kubernetes.Interface
)

func TestMain(m *testing.M) {
	if _, err := exec.LookPath("kind"); err != nil {
		fmt.Println("kind not found in PATH, skipping integration tests")
		os.Exit(0)
	}

	var err error
	testClientset, err = SetupKindCluster(kindClusterName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup kind cluster 1: %v\n", err)
		os.Exit(1)
	}

	testClientset2, err = SetupKindCluster(kindClusterName2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to setup kind cluster 2: %v\n", err)
		_ = TeardownKindCluster(kindClusterName)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Create KWOK-style nodes on cluster 1.
	if _, err := CreateKWOKNodes(ctx, testClientset, 3); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create KWOK nodes on cluster 1: %v\n", err)
		_ = TeardownKindCluster(kindClusterName)
		_ = TeardownKindCluster(kindClusterName2)
		os.Exit(1)
	}

	// Create KWOK-style nodes on cluster 2.
	if _, err := CreateKWOKNodes(ctx, testClientset2, 3); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create KWOK nodes on cluster 2: %v\n", err)
		_ = TeardownKindCluster(kindClusterName)
		_ = TeardownKindCluster(kindClusterName2)
		os.Exit(1)
	}

	code := m.Run()

	_ = TeardownKindCluster(kindClusterName)
	_ = TeardownKindCluster(kindClusterName2)
	os.Exit(code)
}
