/*
Copyright 2026.

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

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/zapr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	hermesv1alpha1 "github.com/UndermountainCC/hermes-operator/api/v1alpha1"
	// +kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	ctx       context.Context
	cancel    context.CancelFunc
	testEnv   *envtest.Environment
	cfg       *rest.Config
	k8sClient client.Client

	// reconciler is the singleton controller instance, exposed so tests can
	// swap ProbeHealthFn to inject synthetic /api/status responses for the
	// Phase 7b dashboard-probe path.
	reconciler *HermesAgentReconciler

	// observedLogs captures every log entry at ErrorLevel from the controller-
	// runtime logger so tests can assert on reconcile error noise. Tests that
	// want a clean slate should call observedLogs.TakeAll() at their start.
	observedLogs *observer.ObservedLogs
)

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	// Wire up controller-runtime's logger with TWO sinks:
	//   1. Console output to GinkgoWriter (so logs appear under the failing test)
	//   2. zaptest/observer at ErrorLevel (so tests can assert on error logs —
	//      specifically the "Reconciler error" entries produced by the apiserver
	//      conflict-on-Update pattern that motivated server-side apply).
	obsCore, ol := observer.New(uberzap.ErrorLevel)
	observedLogs = ol

	encCfg := uberzap.NewDevelopmentEncoderConfig()
	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encCfg),
		zapcore.AddSync(GinkgoWriter),
		zapcore.DebugLevel,
	)
	teedCore := zapcore.NewTee(consoleCore, obsCore)
	logf.SetLogger(zapr.NewLogger(uberzap.New(teedCore, uberzap.AddCaller())))

	ctx, cancel = context.WithCancel(context.Background())

	var err error
	err = hermesv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// +kubebuilder:scaffold:scheme

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	// Retrieve the first found binary directory to allow running tests from IDEs
	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("starting the controller manager")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:         scheme.Scheme,
		LeaderElection: false,
		Metrics:        metricsserver.Options{BindAddress: "0"},
	})
	Expect(err).NotTo(HaveOccurred())

	// ProbeHealthFn defaults to a stub returning nil — tests that want to
	// drive the dashboard probe path override this via DeferCleanup. The
	// real defaultProbeDashboardStatus would try to dial an in-cluster URL
	// that envtest has no way to satisfy.
	reconciler = &HermesAgentReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Config: OperatorConfig{
			AllowedClusterRoles: []string{"cluster-admin", "admin", "view"},
		},
		ProbeHealthFn: func(_ context.Context, _ string) (*DashboardStatus, error) {
			return nil, fmt.Errorf("probe stub: no /api/status configured for this test")
		},
	}
	Expect(reconciler.SetupWithManager(mgr)).To(Succeed())

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()

	// Give the manager a moment to start its caches.
	time.Sleep(500 * time.Millisecond)
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
//
// This function streamlines the process by finding the required binaries, similar to
// setting the 'KUBEBUILDER_ASSETS' environment variable. To ensure the binaries are
// properly set up, run 'make setup-envtest' beforehand.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
