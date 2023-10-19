/*
Copyright 2019 tommylikehu@gmail.com.

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

package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	csv1alpha1 "github.com/opensourceways/code-server-operator/api/v1alpha1"
	"github.com/opensourceways/code-server-operator/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

const (
	REQUEST_CHAN_SIZE = 10
)

func init() {
	_ = clientgoscheme.AddToScheme(scheme)

	_ = csv1alpha1.AddToScheme(scheme)
	// +kubebuilder:scaffold:scheme
}

var onlyOneSignalHandler = make(chan struct{})
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func SetupSignalHandler() (stopCh <-chan struct{}) {
	close(onlyOneSignalHandler) // panics when called twice

	stop := make(chan struct{})
	c := make(chan os.Signal, 2)
	signal.Notify(c, shutdownSignals...)
	go func() {
		<-c
		close(stop)
		<-c
		os.Exit(1) // second signal. Exit directly.
	}()

	return stop
}

func main() {
	var enableLeaderElection bool
	csOption := controllers.CodeServerOption{}
	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&csOption.DomainName, "domain-name", "pool1.playground.osinfra.cn", "Code server domain name.")
	flag.StringVar(&csOption.VSExporterImage, "vs-default-exporter", "tommylike/active-exporter-x86:latest",
		"Default exporter image used as a code server sidecar for VS code instance.")
	flag.IntVar(&csOption.ProbeInterval, "probe-interval", 20,
		"time in seconds between two probes on code server instance.")
	flag.IntVar(&csOption.MaxProbeRetry, "max-probe-retry", 10,
		"count before marking code server inactive when failed to probe liveness")
	flag.StringVar(&csOption.HttpsSecretName, "secret-name", "code-server-secret", "Secret which holds the https cert(tls.crt) and key file(tls.key). This secret will be used in ingress controller as well as code server instance.")
	flag.StringVar(&csOption.LxdClientSecretName, "lxd-client-secret-name", "lxd-client-secret", "Secret which holds the key and secret for lxc client to communicate to server.")
	flag.IntVar(&csOption.MaxConcurrency, "max-concurrency", 10, "Max concurrency of reconcile worker.")
	flag.StringVar(&csOption.Oauth2ProxyServiceName, "oauth2-proxy-service", "oauth2-proxy", "Service name which used for performing additional Authentication, should exist in same namespace.")
	flag.Parse()

	ctrl.SetLogger(zap.New(func(o *zap.Options) {
		o.Development = true
	}))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:         scheme,
		LeaderElection: enableLeaderElection,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}
	csRequest := make(chan controllers.CodeServerRequest, REQUEST_CHAN_SIZE)
	if err = (&controllers.CodeServerReconciler{
		Client:  mgr.GetClient(),
		Log:     ctrl.Log.WithName("controllers").WithName("CodeServer"),
		Scheme:  mgr.GetScheme(),
		Options: &csOption,
		ReqCh:   csRequest,
	}).SetupWithManager(mgr, csOption.MaxConcurrency); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "CodeServer")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder
	probeTicker := time.NewTicker(time.Duration(csOption.ProbeInterval) * time.Second)
	defer probeTicker.Stop()
	//setup code server watcher
	codeServerWatcher := controllers.NewCodeServerWatcher(
		mgr.GetClient(),
		ctrl.Log.WithName("controllers").WithName("CodeServerWatcher"),
		mgr.GetScheme(),
		&csOption,
		csRequest,
		probeTicker.C)
	stopContext := ctrl.SetupSignalHandler()
	go codeServerWatcher.Run(stopContext.Done())

	setupLog.Info("starting manager")
	if err := mgr.Start(stopContext); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
