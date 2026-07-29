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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/zebernst/nominatim-operator/test/utils"
)

// namespace where the project is deployed in
const namespace = "nominatim-operator-system"

// serviceAccountName created for the project
const serviceAccountName = "nominatim-operator-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "nominatim-operator-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "nominatim-operator-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace)
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching curl-metrics logs")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Metrics logs:\n %s", metricsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get curl-metrics logs: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				fmt.Println("Pod description:\n", podDescription)
			} else {
				fmt.Println("Failed to describe controller pod")
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)

	Context("Manager", func() {
		It("should run successfully", func() {
			By("validating that the controller-manager pod is running as expected")
			verifyControllerUp := func(g Gomega) {
				// Get the name of the controller-manager pod
				cmd := exec.Command("kubectl", "get",
					"pods", "-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)

				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				// Validate the pod's status
				cmd = exec.Command("kubectl", "get",
					"pods", controllerPodName, "-o", "jsonpath={.status.phase}",
					"-n", namespace,
				)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}
			Eventually(verifyControllerUp).Should(Succeed())
		})

		It("should ensure the metrics endpoint is serving metrics", func() {
			By("creating a ClusterRoleBinding for the service account to allow access to metrics")
			cmd := exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=nominatim-operator-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("validating that the metrics service is available")
			cmd = exec.Command("kubectl", "get", "service", metricsServiceName, "-n", namespace)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Metrics service should exist")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("waiting for the metrics endpoint to be ready")
			verifyMetricsEndpointReady := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "endpoints", metricsServiceName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("8443"), "Metrics endpoint is not ready")
			}
			Eventually(verifyMetricsEndpointReady).Should(Succeed())

			By("verifying that the controller manager is serving the metrics server")
			verifyMetricsServerStarted := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring("controller-runtime.metrics\tServing metrics server"),
					"Metrics server not yet started")
			}
			Eventually(verifyMetricsServerStarted).Should(Succeed())

			By("creating the curl-metrics pod to access the metrics endpoint")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": ["curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics"],
							"securityContext": {
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccount": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to complete.")
			verifyCurlUp := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}",
					"-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}
			Eventually(verifyCurlUp, 5*time.Minute).Should(Succeed())

			By("getting the metrics by checking curl-metrics logs")
			metricsOutput := getMetricsOutput()
			Expect(metricsOutput).To(ContainSubstring(
				"controller_runtime_reconcile_total",
			))
		})

		// +kubebuilder:scaffold:e2e-webhooks-checks
	})

	Context("Nominatim reconcile smoke", func() {
		const (
			appNamespace = "nominatim-e2e"
			nomName      = "smoke"
		)

		BeforeAll(func() {
			By("applying Nominatim smoke fixtures (secret, PVC, CR with connectionSecretRef)")
			fixture := filepath.Join("test", "e2e", "testdata", "nominatim-smoke.yaml")
			cmd := exec.Command("kubectl", "apply", "-f", fixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Nominatim smoke fixtures")
		})

		AfterAll(func() {
			By("deleting Nominatim smoke fixtures")
			fixture := filepath.Join("test", "e2e", "testdata", "nominatim-smoke.yaml")
			cmd := exec.Command("kubectl", "delete", "-f", fixture, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", appNamespace, "--ignore-not-found=true", "--wait=false")
			_, _ = utils.Run(cmd)
		})

		It("installs Nominatim CRDs", func() {
			cmd := exec.Command("kubectl", "get", "crd", "nominatims.nominatim.zebernst.dev")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Nominatim CRD should be installed")
			cmd = exec.Command("kubectl", "get", "crd", "nominatimoperations.nominatim.zebernst.dev")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "NominatimOperation CRD should be installed")
		})

		It("reconciles API Deployment and Service for the smoke Nominatim", func() {
			By("waiting for the Nominatim controller to become ready")
			verifyNominatimController := func(g Gomega) {
				cmd := exec.Command("kubectl", "logs",
					"-l", "control-plane=controller-manager",
					"-n", namespace,
					"--tail=200",
				)
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(ContainSubstring(`"controller": "nominatim"`), "Nominatim controller should start")
				g.Expect(out).To(ContainSubstring(`Starting workers`), "Nominatim controller workers should start")
				g.Expect(out).NotTo(ContainSubstring(`no matches for kind "HTTPRoute"`))
			}
			Eventually(verifyNominatimController).Should(Succeed())

			By("waiting for the Nominatim API Deployment")
			verifyAPIDeployment := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nominatim", nomName,
					"-n", appNamespace, "-o", "yaml")
				nomYAML, _ := utils.Run(cmd)
				_, _ = fmt.Fprintf(GinkgoWriter, "Nominatim CR:\n%s\n", nomYAML)

				cmd = exec.Command("kubectl", "get", "deploy", nomName+"-api",
					"-n", appNamespace, "-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "API Deployment should exist")
				g.Expect(out).To(Equal(nomName + "-api"))

				cmd = exec.Command("kubectl", "get", "deploy", nomName+"-api",
					"-n", appNamespace, "-o", "jsonpath={.spec.replicas}")
				replicas, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(replicas).To(Equal("1"))
			}
			Eventually(verifyAPIDeployment).Should(Succeed())

			By("waiting for the Nominatim API Service")
			verifyAPIService := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "svc", nomName+"-api",
					"-n", appNamespace, "-o", "jsonpath={.metadata.name}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "API Service should exist")
				g.Expect(out).To(Equal(nomName + "-api"))
			}
			Eventually(verifyAPIService).Should(Succeed())

			By("confirming degraded database mode is recorded on status")
			verifyDatabaseStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nominatim", nomName,
					"-n", appNamespace,
					"-o", "jsonpath={.status.database.connectionSecretName}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("nominatim-pg-app"))

				cmd = exec.Command("kubectl", "get", "nominatim", nomName,
					"-n", appNamespace,
					"-o", "jsonpath={.status.database.mode}")
				mode, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(mode).To(Equal("ConnectionSecret"))
			}
			Eventually(verifyDatabaseStatus).Should(Succeed())
		})

		It("keeps the controller-manager Running after Nominatim reconcile", func() {
			cmd := exec.Command("kubectl", "get", "pods",
				"-l", "control-plane=controller-manager",
				"-n", namespace,
				"-o", "jsonpath={.items[0].status.phase}")
			out, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(out).To(Equal("Running"))
		})
	})

	Context("Monaco import", Ordered, func() {
		const (
			validationNamespace = "nominatim-validation"
			nomName             = "monaco"
			ownedDatabaseName   = nomName + "-pg-nominatim"
			reimportName        = nomName + "-reimport"
		)

		BeforeAll(func() {
			if !runImportE2E {
				Skip("set E2E_IMPORT=1 to run Monaco import e2e (CNPG + api/worker images)")
			}

			By("applying Monaco Nominatim fixture (database.cluster + regions)")
			fixture := filepath.Join("test", "e2e", "testdata", "nominatim-monaco.yaml")
			cmd := exec.Command("kubectl", "apply", "-f", fixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply Monaco fixtures")
		})

		AfterAll(func() {
			if !runImportE2E {
				return
			}
			By("deleting Monaco validation fixtures")
			reimport := filepath.Join("test", "e2e", "testdata", "nominatim-monaco-reimport.yaml")
			cmd := exec.Command("kubectl", "delete", "-f", reimport, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			fixture := filepath.Join("test", "e2e", "testdata", "nominatim-monaco.yaml")
			cmd = exec.Command("kubectl", "delete", "-f", fixture, "--ignore-not-found=true")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "ns", validationNamespace, "--ignore-not-found=true", "--wait=false")
			_, _ = utils.Run(cmd)
		})

		It("bootstraps Monaco and answers avenue pasteur search", func() {
			By("waiting for CNPG Cluster Ready")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "cluster.postgresql.cnpg.io", nomName+"-pg",
					"-n", validationNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("True"))
			}, 15*time.Minute, 10*time.Second).Should(Succeed())

			By("waiting for Bootstrap NominatimOperation Succeeded")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nominatimoperation", nomName+"-bootstrap",
					"-n", validationNamespace,
					"-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(Equal("Failed"), "Bootstrap should not Fail")
				g.Expect(out).To(Equal("Succeeded"))
			}, 40*time.Minute, 15*time.Second).Should(Succeed())

			By("waiting for API Deployment Available")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deploy", nomName+"-api",
					"-n", validationNamespace,
					"-o", "jsonpath={.status.availableReplicas}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).To(Equal("1"))
			}, 10*time.Minute, 5*time.Second).Should(Succeed())

			By("probing /search?q=avenue%20pasteur until non-empty JSON")
			assertNonEmptySearch(validationNamespace, nomName+"-api")
		})

		// Reimport must start from an empty application database so CNPG (as superuser)
		// reinstalls PostGIS/hstore. The controller drops and recreates the owned Database
		// CR and only arms the worker Job once the replacement reports applied=true.
		It("drops and recreates the owned CNPG Database before arming the Reimport Job", func() {
			By("recording the current owned CNPG Database UID")
			oldUID := cnpgDatabaseField(validationNamespace, ownedDatabaseName, "metadata.uid")
			Expect(oldUID).NotTo(BeEmpty(), "Bootstrap should have left an owned CNPG Database")

			By("creating the Reimport NominatimOperation")
			fixture := filepath.Join("test", "e2e", "testdata", "nominatim-monaco-reimport.yaml")
			cmd := exec.Command("kubectl", "apply", "-f", fixture)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create the Reimport NominatimOperation")

			expectDatabaseReplacedBeforeJob(validationNamespace, ownedDatabaseName, reimportName, oldUID)

			By("waiting for Reimport NominatimOperation Succeeded")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "nominatimoperation", reimportName,
					"-n", validationNamespace,
					"-o", "jsonpath={.status.phase}")
				out, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(out).NotTo(Equal("Failed"), "Reimport should not Fail")
				g.Expect(out).To(Equal("Succeeded"))
			}, 40*time.Minute, 15*time.Second).Should(Succeed())

			By("probing /search?q=avenue%20pasteur again after Reimport")
			assertNonEmptySearch(validationNamespace, nomName+"-api")
		})
	})
})

// cnpgDatabaseField reads a jsonpath field off the owned CNPG Database, returning "" while
// the object is absent (the window between the drop and the recreate).
func cnpgDatabaseField(ns, name, path string) string {
	cmd := exec.Command("kubectl", "get", "database.postgresql.cnpg.io", name,
		"-n", ns, "--ignore-not-found", "-o", "jsonpath={."+path+"}")
	out, err := utils.Run(cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// expectDatabaseReplacedBeforeJob asserts the drop/recreate handshake: the Reimport Job may
// only appear once the owned Database is a different object (new UID) reporting
// status.applied=true, with the Operation's handshake annotations recording the swap.
//
// Polling can miss a short-lived violation, so the loop fails fast whenever it observes a
// Job alongside the pre-Reimport UID, and the annotation assertions afterwards are the
// deterministic evidence that the handshake ran rather than being skipped.
func expectDatabaseReplacedBeforeJob(ns, dbName, opName, oldUID string) {
	By("asserting the Reimport Job is not created until the Database is replaced and applied")
	const (
		resetAnnotation   = "nominatim.zebernst.dev/reimport-db-reset"
		prevUIDAnnotation = "nominatim.zebernst.dev/reimport-db-prev-uid"
	)

	armed := false
	deadline := time.Now().Add(15 * time.Minute)
	for !armed && time.Now().Before(deadline) {
		uid := cnpgDatabaseField(ns, dbName, "metadata.uid")
		applied := cnpgDatabaseField(ns, dbName, "status.applied")

		cmd := exec.Command("kubectl", "get", "job", opName,
			"-n", ns, "--ignore-not-found", "-o", "jsonpath={.metadata.name}")
		jobName, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())

		if strings.TrimSpace(jobName) == "" {
			time.Sleep(2 * time.Second)
			continue
		}
		Expect(uid).NotTo(Equal(oldUID),
			"Reimport Job was armed while the pre-Reimport CNPG Database was still present")
		Expect(applied).To(Equal("true"),
			"Reimport Job was armed before the replacement CNPG Database reported applied=true")
		armed = true
	}
	Expect(armed).To(BeTrue(), "timed out waiting for the Reimport Job to be armed")

	By("asserting the Operation recorded the drop/recreate handshake")
	cmd := exec.Command("kubectl", "get", "nominatimoperation", opName, "-n", ns,
		"-o", "jsonpath={.metadata.annotations."+strings.ReplaceAll(resetAnnotation, ".", "\\.")+"}")
	reset, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(reset)).To(Equal("done"))

	cmd = exec.Command("kubectl", "get", "nominatimoperation", opName, "-n", ns,
		"-o", "jsonpath={.metadata.annotations."+strings.ReplaceAll(prevUIDAnnotation, ".", "\\.")+"}")
	prevUID, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(prevUID)).To(Equal(oldUID),
		"the Operation should record the UID of the Database it dropped")

	Expect(cnpgDatabaseField(ns, dbName, "metadata.uid")).NotTo(Equal(oldUID))
}

// assertNonEmptySearch port-forwards the API Service and retries until search returns hits
// (parity with mediagis/nominatim-docker assert-non-empty-json).
func assertNonEmptySearch(ns, svc string) {
	By("starting port-forward to Nominatim API")
	pf := exec.Command("kubectl", "-n", ns, "port-forward", "svc/"+svc, "18081:80")
	Expect(pf.Start()).To(Succeed())
	defer func() { _ = pf.Process.Kill() }()

	Eventually(func(g Gomega) {
		cmd := exec.Command("python3", "-c", `
import json, urllib.request
with urllib.request.urlopen("http://127.0.0.1:18081/search?q=avenue%20pasteur", timeout=30) as r:
    data = json.loads(r.read())
assert isinstance(data, list) and len(data) > 0, data
`)
		out, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred(), string(out))
	}, 10*time.Minute, 5*time.Second).Should(Succeed())
}

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// getMetricsOutput retrieves and returns the logs from the curl pod used to access the metrics endpoint.
func getMetricsOutput() string {
	By("getting the curl-metrics logs")
	cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
	metricsOutput, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
	Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
	return metricsOutput
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
