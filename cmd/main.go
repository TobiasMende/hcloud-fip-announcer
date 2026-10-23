/*
Copyright 2026 Tobias Mende.

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
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	fipannouncerv1alpha1 "github.com/TobiasMende/hcloud-fip-announcer/api/v1alpha1"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/controller"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/hetzner"
	"github.com/TobiasMende/hcloud-fip-announcer/internal/probe"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(fipannouncerv1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

// nolint:gocyclo
func main() {
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var webhookPort int
	var enableLeaderElection bool
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var leaseDuration, renewDeadline, retryPeriod, probeInterval time.Duration
	var probeFailureThreshold int
	var clusterID, loadBalancerClass string
	var antiFlap, hetznerPollInterval time.Duration
	var tlsOpts []func(*tls.Config)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.IntVar(&webhookPort, "webhook-port", 9443, "Port the webhook server listens on. "+
		"Defaults to 9443. Set -1 to disable the webhook server.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	flag.DurationVar(&leaseDuration, "leader-elect-lease-duration", 6*time.Second,
		"Duration non-leaders wait before taking over the leader lease.")
	flag.DurationVar(&renewDeadline, "leader-elect-renew-deadline", 4*time.Second,
		"Duration the leader keeps retrying to renew the lease before giving up.")
	flag.DurationVar(&retryPeriod, "leader-elect-retry-period", time.Second,
		"Duration between leader election attempts.")
	flag.DurationVar(&probeInterval, "probe-interval", time.Second,
		"Interval between active health probes (also the per-probe timeout).")
	flag.IntVar(&probeFailureThreshold, "probe-failure-threshold", 3,
		"Consecutive probe failures before a target counts as failing.")
	flag.StringVar(&clusterID, "cluster-id", "",
		"Required. Identifies this cluster in the labels of its Hetzner Floating IPs.")
	flag.StringVar(&loadBalancerClass, "load-balancer-class", "fip-announcer.inocea.de/floating-ip",
		"loadBalancerClass of the Services to handle; empty handles Services without a class.")
	flag.DurationVar(&antiFlap, "anti-flap", 30*time.Second,
		"Minimum time between two moves of a group, unless the holder is cordoned or its server is gone.")
	flag.DurationVar(&hetznerPollInterval, "hetzner-poll-interval", 30*time.Second,
		"How long Hetzner reads are cached; Floating IPs are also re-read right before every move.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	if probeInterval <= 0 {
		setupLog.Error(nil, "--probe-interval must be greater than 0", "value", probeInterval)
		os.Exit(1)
	}
	if probeFailureThreshold < 1 {
		setupLog.Error(nil, "--probe-failure-threshold must be at least 1", "value", probeFailureThreshold)
		os.Exit(1)
	}
	if clusterID == "" {
		setupLog.Error(nil, "--cluster-id is required")
		os.Exit(1)
	}
	if antiFlap < 0 {
		setupLog.Error(nil, "--anti-flap must not be negative", "value", antiFlap)
		os.Exit(1)
	}
	if hetznerPollInterval <= 0 {
		setupLog.Error(nil, "--hetzner-poll-interval must be greater than 0", "value", hetznerPollInterval)
		os.Exit(1)
	}
	hcloudToken := os.Getenv("HCLOUD_TOKEN")
	if hcloudToken == "" {
		setupLog.Error(nil, "environment variable HCLOUD_TOKEN is required")
		os.Exit(1)
	}

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
		Port:    webhookPort,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate watcher using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		// FilterProvider is used to protect the metrics endpoint with authn/authz.
		// These configurations ensure that only authorized users and service accounts
		// can access the metrics endpoint. The RBAC are configured in 'config/rbac/kustomization.yaml'. More info:
		// https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/metrics/filters#WithAuthenticationAndAuthorization
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	// If the certificate is not specified, controller-runtime will automatically
	// generate self-signed certificates for the metrics server. While convenient for development and testing,
	// this setup is not recommended for production.
	// To use cert-manager certificates, enable [METRICS-WITH-CERTS] in
	// config/default/kustomization.yaml and [PROMETHEUS-WITH-CERTS] in
	// config/prometheus/kustomization.yaml.
	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsServerOptions,
		WebhookServer:          webhookServer,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "22cb396b.inocea.de",
		LeaseDuration:          &leaseDuration,
		RenewDeadline:          &renewDeadline,
		RetryPeriod:            &retryPeriod,
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	ctx := ctrl.SetupSignalHandler()
	recorder := mgr.GetEventRecorderFor("hcloud-fip-announcer") //nolint:staticcheck // record.EventRecorder API
	if err := (&controller.ServiceReconciler{
		Client:   mgr.GetClient(),
		Class:    loadBalancerClass,
		Recorder: recorder,
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "service")
		os.Exit(1)
	}
	prober := probe.New(probeInterval, probeFailureThreshold)
	if err := mgr.Add(prober); err != nil {
		setupLog.Error(err, "Failed to add prober")
		os.Exit(1)
	}
	if err := (&controller.FloatingIPGroupReconciler{
		Client:       mgr.GetClient(),
		APIReader:    mgr.GetAPIReader(),
		Scheme:       mgr.GetScheme(),
		Hetzner:      hetzner.New(hcloudToken),
		Recorder:     recorder,
		Prober:       prober,
		ProbeEvents:  prober.Events(),
		ClusterID:    clusterID,
		Class:        loadBalancerClass,
		AntiFlap:     antiFlap,
		PollInterval: hetznerPollInterval,
		LeaderNode:   os.Getenv("NODE_NAME"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "floatingipgroup")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
