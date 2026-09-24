// Command apply-runtime installs the gVisor or Kata sandbox runtime on the current kubeconfig cluster.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/hatchet-dev/brickie/internal/awsboot"
)

func main() {
	runtime := flag.String("runtime", "gvisor", "sandbox runtime to install on the current cluster: gvisor or kata")
	flag.Parse()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		fail(err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	switch *runtime {
	case "gvisor":
		if err := awsboot.EnsureGVisor(ctx, clientset); err != nil {
			fail(err)
		}
		fmt.Println("gvisor runtime class and installer applied; waiting for readiness")
		if err := awsboot.WaitGVisorReady(ctx, clientset, 5*time.Minute); err != nil {
			fail(err)
		}
		fmt.Println("gvisor ready on all nodes")
	case "kata":
		if err := awsboot.EnsureKata(ctx, cfg); err != nil {
			fail(err)
		}
		fmt.Println("kata-deploy installed; waiting for the kata-clh runtime class on all nodes")
		if err := awsboot.WaitKataReady(ctx, clientset, 10*time.Minute); err != nil {
			fail(err)
		}
		fmt.Println("kata (cloud hypervisor) ready on all nodes")
	default:
		fail(fmt.Errorf("unknown runtime %q (gvisor|kata)", *runtime))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
