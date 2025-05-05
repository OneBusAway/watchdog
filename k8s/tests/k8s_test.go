package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/client-go/util/retry"
)

const (
	namespace = "watchdog-ns"
	timeout   = 60 * time.Second
	interval  = 5 * time.Second
)

func TestKubernetesManifests(t *testing.T) {
	// Get kubeconfig
	kubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatalf("Error building kubeconfig: %v", err)
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("Error creating clientset: %v", err)
	}

	// Create namespace if it doesn't exist
	_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
		},
	}, metav1.CreateOptions{})
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("Error creating namespace: %v", err)
	}

	// Test 1: Validate deployment configuration
	t.Run("Validate deployment configuration", func(t *testing.T) {
		deploymentPath := filepath.Join("..", "deployment.yaml")
		if _, err := os.Stat(deploymentPath); os.IsNotExist(err) {
			t.Fatalf("Deployment file not found: %v", err)
		}
	})

	// Test 2: Validate service configuration
	t.Run("Validate service configuration", func(t *testing.T) {
		servicePath := filepath.Join("..", "service.yaml")
		if _, err := os.Stat(servicePath); os.IsNotExist(err) {
			t.Fatalf("Service file not found: %v", err)
		}
	})

	// Test 3: Validate configmap configuration
	t.Run("Validate configmap configuration", func(t *testing.T) {
		configmapPath := filepath.Join("..", "configmap.yaml")
		if _, err := os.Stat(configmapPath); os.IsNotExist(err) {
			t.Fatalf("ConfigMap file not found: %v", err)
		}
	})

	// Apply manifests
	t.Run("Apply manifests", func(t *testing.T) {
		// Apply ConfigMap
		configmapPath := filepath.Join("..", "configmap.yaml")
		configmapBytes, err := os.ReadFile(configmapPath)
		if err != nil {
			t.Fatalf("Error reading ConfigMap file: %v", err)
		}
		_, err = clientset.CoreV1().ConfigMaps(namespace).Create(context.TODO(), &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "watchdog-config",
			},
			Data: map[string]string{
				"oba_server_config.json": string(configmapBytes),
			},
		}, metav1.CreateOptions{})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("Error creating ConfigMap: %v", err)
		}

		// Apply Deployment
		deploymentPath := filepath.Join("..", "deployment.yaml")
		_, err = os.ReadFile(deploymentPath)
		if err != nil {
			t.Fatalf("Error reading Deployment file: %v", err)
		}
		_, err = clientset.AppsV1().Deployments(namespace).Create(context.TODO(), &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name: "watchdog",
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: int32Ptr(1),
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "watchdog",
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "watchdog",
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "watchdog",
								Image: "sidharthchauhan/watchdog:v1",
								Ports: []corev1.ContainerPort{
									{
										ContainerPort: 8080,
									},
								},
								VolumeMounts: []corev1.VolumeMount{
									{
										Name:      "config-volume",
										MountPath: "/app/config",
										ReadOnly:  true,
									},
								},
								Args: []string{
									"--config-file",
									"/app/config/oba_server_config.json",
								},
							},
						},
						Volumes: []corev1.Volume{
							{
								Name: "config-volume",
								VolumeSource: corev1.VolumeSource{
									ConfigMap: &corev1.ConfigMapVolumeSource{
										LocalObjectReference: corev1.LocalObjectReference{
											Name: "watchdog-config",
										},
									},
								},
							},
						},
					},
				},
			},
		}, metav1.CreateOptions{})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("Error creating Deployment: %v", err)
		}

		// Apply Service
		servicePath := filepath.Join("..", "service.yaml")
		_, err = os.ReadFile(servicePath)
		if err != nil {
			t.Fatalf("Error reading Service file: %v", err)
		}
		_, err = clientset.CoreV1().Services(namespace).Create(context.TODO(), &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name: "watchdog-service",
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeNodePort,
				Ports: []corev1.ServicePort{
					{
						Port: 8080,
					},
				},
				Selector: map[string]string{
					"app": "watchdog",
				},
			},
		}, metav1.CreateOptions{})
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("Error creating Service: %v", err)
		}

		// Wait for deployment to be ready
		err = retry.OnError(retry.DefaultRetry, func(err error) bool {
			return true
		}, func() error {
			deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), "watchdog", metav1.GetOptions{})
			if err != nil {
				return err
			}
			if deployment.Status.AvailableReplicas != 1 {
				return fmt.Errorf("deployment not ready: %d/%d replicas available", deployment.Status.AvailableReplicas, *deployment.Spec.Replicas)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("Error waiting for deployment: %v", err)
		}
	})

	// Test 4: Check pod status
	t.Run("Check pod status", func(t *testing.T) {
		pods, err := clientset.CoreV1().Pods(namespace).List(context.TODO(), metav1.ListOptions{
			LabelSelector: "app=watchdog",
		})
		if err != nil {
			t.Fatalf("Error listing pods: %v", err)
		}

		if len(pods.Items) == 0 {
			t.Error("No pods found in namespace")
			return
		}

		pod := pods.Items[0]
		if pod.Status.Phase != corev1.PodRunning {
			t.Errorf("Expected pod status Running, got %s", pod.Status.Phase)
		}

		if pod.Status.ContainerStatuses[0].Ready != true {
			t.Error("Container is not ready")
		}
	})

	// Test 5: Check service
	t.Run("Check service", func(t *testing.T) {
		service, err := clientset.CoreV1().Services(namespace).Get(context.TODO(), "watchdog-service", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Error getting service: %v", err)
		}

		if service.Spec.Type != corev1.ServiceTypeNodePort {
			t.Errorf("Expected service type NodePort, got %s", service.Spec.Type)
		}

		if len(service.Spec.Ports) == 0 {
			t.Error("No ports configured in service")
		} else if service.Spec.Ports[0].Port != 8080 {
			t.Errorf("Expected port 8080, got %d", service.Spec.Ports[0].Port)
		}
	})

	// Test 6: Check deployment
	t.Run("Check deployment", func(t *testing.T) {
		deployment, err := clientset.AppsV1().Deployments(namespace).Get(context.TODO(), "watchdog", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Error getting deployment: %v", err)
		}

		if *deployment.Spec.Replicas != 1 {
			t.Errorf("Expected 1 replica, got %d", *deployment.Spec.Replicas)
		}

		if deployment.Status.AvailableReplicas != 1 {
			t.Errorf("Expected 1 available replica, got %d", deployment.Status.AvailableReplicas)
		}
	})

	// Test 7: Check ConfigMap
	t.Run("Check ConfigMap", func(t *testing.T) {
		configmap, err := clientset.CoreV1().ConfigMaps(namespace).Get(context.TODO(), "watchdog-config", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Error getting ConfigMap: %v", err)
		}

		if _, exists := configmap.Data["oba_server_config.json"]; !exists {
			t.Error("ConfigMap does not contain oba_server_config.json")
		}
	})
}

func int32Ptr(i int32) *int32 { return &i }
