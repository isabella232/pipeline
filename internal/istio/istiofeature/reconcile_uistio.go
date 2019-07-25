// Copyright © 2019 Banzai Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package istiofeature

import (
	"time"

	"emperror.dev/emperror"
	"github.com/ghodss/yaml"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/banzaicloud/pipeline/cluster"
	"github.com/banzaicloud/pipeline/internal/backoff"
	pkgHelm "github.com/banzaicloud/pipeline/pkg/helm"
)

type monitoringConfig struct {
	hostname string
	url      string
}

func (m *MeshReconciler) ReconcileUistio(desiredState DesiredState) error {
	m.logger.Debug("reconciling Uistio")
	defer m.logger.Debug("Uistio reconciled")

	if desiredState == DesiredStatePresent {
		apiextclient, err := m.getApiExtensionK8sClient(m.Master)
		if err != nil {
			return emperror.Wrap(err, "could not get api extension client")
		}

		err = m.waitForMetricCRD("metrics.config.istio.io", apiextclient)
		if err != nil {
			return emperror.Wrap(err, "error while waiting for metric CRD")
		}

		k8sclient, err := m.getMasterK8sClient()
		if err != nil {
			return emperror.Wrap(err, "could not get k8s client")
		}
		err = m.waitForSidecarInjectorPod(k8sclient)
		if err != nil {
			return emperror.Wrap(err, "error while waiting for running sidecar injector")
		}

		err = m.installUistio(m.Master, monitoringConfig{
			hostname: prometheusHostname,
			url:      prometheusURL,
		})
		if err != nil {
			return emperror.Wrap(err, "could not install Uistio")
		}
	} else {
		err := m.uninstallUistio(m.Master)
		if err != nil {
			return emperror.Wrap(err, "could not remove Uistio")
		}
	}

	return nil
}

// waitForSidecarInjectorPod waits for Sidecar Injector Pods to be running
func (m *MeshReconciler) waitForSidecarInjectorPod(client *kubernetes.Clientset) error {
	m.logger.Debug("waiting for sidecar injector pod")

	var backoffConfig = backoff.ConstantBackoffConfig{
		Delay:      time.Duration(backoffDelaySeconds) * time.Second,
		MaxRetries: backoffMaxretries,
	}
	var backoffPolicy = backoff.NewConstantBackoffPolicy(&backoffConfig)

	err := backoff.Retry(func() error {
		pods, err := client.CoreV1().Pods(istioOperatorNamespace).List(metav1.ListOptions{
			LabelSelector: "app=istio-sidecar-injector",
			FieldSelector: "status.phase=Running",
		})

		if err != nil {
			return emperror.Wrap(err, "could not list pods")
		}

		if len(pods.Items) == 0 {
			return errors.New("could not find any running sidecar injector")
		}

		return nil
	}, backoffPolicy)

	return err
}

// waitForMetricCRD waits for Metric CRD to be present in the cluster
func (m *MeshReconciler) waitForMetricCRD(name string, client *apiextensionsclient.Clientset) error {
	m.logger.WithField("name", name).Debug("waiting for metric CRD")

	var backoffConfig = backoff.ConstantBackoffConfig{
		Delay:      time.Duration(backoffDelaySeconds) * time.Second,
		MaxRetries: backoffMaxretries,
	}
	var backoffPolicy = backoff.NewConstantBackoffPolicy(&backoffConfig)

	err := backoff.Retry(func() error {
		_, err := client.ApiextensionsV1beta1().CustomResourceDefinitions().Get(name, metav1.GetOptions{})
		if err != nil {
			return emperror.Wrap(err, "could not get metric CRD")
		}

		return nil
	}, backoffPolicy)

	return err
}

// uninstallIstioOperator removes istio-operator from a cluster
func (m *MeshReconciler) uninstallUistio(c cluster.CommonCluster) error {
	m.logger.Debug("removing Uistio")

	err := deleteDeployment(c, uistioReleaseName)
	if err != nil {
		return emperror.Wrap(err, "could not remove Uistio")
	}

	return nil
}

// installIstioOperator installs istio-operator on a cluster
func (m *MeshReconciler) installUistio(c cluster.CommonCluster, monitoring monitoringConfig) error {
	m.logger.Debug("installing Uistio")

	type istio struct {
		CRname    string `json:"CRName,omitempty"`
		Namespace string `json:"namespace,omitempty"`
	}

	type application struct {
		Image imageChartValue   `json:"image,omitempty"`
		Env   map[string]string `json:"env,omitempty"`
	}

	type Values struct {
		Affinity    v1.Affinity          `json:"affinity,omitempty"`
		Tolerations []v1.Toleration      `json:"tolerations,omitempty"`
		Istio       istio                `json:"istio,omitempty"`
		Application application          `json:"application,omitempty"`
		Prometheus  prometheusChartValue `json:"prometheus,omitempty"`
	}

	values := Values{
		Affinity:    cluster.GetHeadNodeAffinity(c),
		Tolerations: cluster.GetHeadNodeTolerations(),
		Application: application{
			Image: imageChartValue{},
			Env: map[string]string{
				"APP_CANARYENABLED": "true",
			},
		},
		Istio: istio{
			CRname:    m.Configuration.name,
			Namespace: istioOperatorNamespace,
		},
		Prometheus: prometheusChartValue{
			Enabled:  true,
			URL:      monitoring.url,
			Hostname: monitoring.hostname,
		},
	}

	if m.Configuration.internalConfig.uistio.imageRepository != "" {
		values.Application.Image.Repository = m.Configuration.internalConfig.uistio.imageRepository
	}
	if m.Configuration.internalConfig.uistio.imageTag != "" {
		values.Application.Image.Tag = m.Configuration.internalConfig.uistio.imageTag

	}

	valuesOverride, err := yaml.Marshal(values)
	if err != nil {
		return emperror.Wrap(err, "could not marshal chart value overrides")
	}

	err = installOrUpgradeDeployment(
		c,
		meshNamespace,
		pkgHelm.BanzaiRepository+"/"+m.Configuration.internalConfig.uistio.chartName,
		uistioReleaseName,
		valuesOverride,
		m.Configuration.internalConfig.uistio.chartVersion,
		true,
		true,
	)
	if err != nil {
		return emperror.Wrap(err, "could not install Uistio")
	}

	return nil
}