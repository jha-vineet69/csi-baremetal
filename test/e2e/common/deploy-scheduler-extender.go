/*
Copyright © 2020 Dell Inc. or its subsidiaries. All Rights Reserved.

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

package common

import (
	"fmt"
	"io/ioutil"
	"path"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	"sigs.k8s.io/yaml"
)

const (
	extenderManifestsFolder = "scheduler-extender/templates/"
	schedulerLabel          = "component=kube-scheduler"
	restartWaitTimeout      = time.Minute * 2
)

func DeploySchedulerExtender(f *framework.Framework) (func(), error) {
	return deployExtenderManifests(f)
}

func deployExtenderManifests(f *framework.Framework) (func(), error) {
	manifests := []string{
		extenderManifestsFolder + "rbac.yaml",
	}

	daemonSetCleanup, err := buildDaemonSet(f.ClientSet, f.Namespace.Name, "scheduler-extender.yaml")
	if err != nil {
		return nil, err
	}

	manifestsCleanupFunc, err := f.CreateFromManifests(nil, manifests...)
	if err != nil {
		return nil, err
	}

	cleanupFunc := func() {
		daemonSetCleanup()
		manifestsCleanupFunc()
	}

	return cleanupFunc, nil
}

func DeployPatcher(c clientset.Interface, namespace string) (func(), error) {
	manifestsCleanupFunc, err := waitForRestart(c,
		func() (func(), error) {
			return deployPatcherManifests(c, namespace)
		})
	if err != nil {
		return nil, err
	}
	return func() {
		_, err := waitForRestart(c,
			func() (func(), error) {
				manifestsCleanupFunc()
				return func() {}, nil
			})
		if err != nil {
			e2elog.Logf("failed to cleanup patcher, err: %s", err.Error())
		}
	}, nil
}

func deployPatcherManifests(c clientset.Interface, namespace string) (func(), error) {
	daemonSetCleanup, err := buildDaemonSet(c, namespace, "patcher.yaml")
	if err != nil {
		return nil, err
	}
	configMapCleanup, err := buildConfigMap(c, namespace)
	if err != nil {
		return nil, err
	}
	return func() {
		daemonSetCleanup()
		configMapCleanup()
	}, nil
}

func waitForRestart(c clientset.Interface, fu func() (func(), error)) (func(), error) {
	wait := BMDriverTestContext.BMWaitSchedulerRestart

	rc := newSchedulerRestartChecker(c)
	if wait {
		err := rc.ReadInitialState()
		if err != nil {
			return nil, err
		}
	}
	result, err := fu()
	if err != nil {
		return nil, err
	}
	if wait {
		e2elog.Logf("Wait for scheduler restart")
		deadline := time.Now().Add(restartWaitTimeout)
		for {
			ready, err := rc.CheckRestarted()
			if err != nil {
				return nil, err
			}
			if ready {
				e2elog.Logf("Scheduler restarted")
				break
			}
			msg := "Scheduler restart NOT detected yet"
			e2elog.Logf(msg)
			if time.Now().After(deadline) {
				e2elog.Logf("Scheduler restart NOT detected after %d minutes. Continue.",
					restartWaitTimeout.Minutes())
				break
			}
			time.Sleep(time.Second * 5)
		}
	}
	return result, nil
}

func buildConfigMap(c clientset.Interface, namespace string) (func(), error) {
	file, err := ioutil.ReadFile("/tmp/" + extenderManifestsFolder + "/patcher-configmap.yaml")
	if err != nil {
		return nil, err
	}

	cm := &corev1.ConfigMap{}
	err = yaml.Unmarshal(file, cm)
	if err != nil {
		return nil, err
	}
	cm.ObjectMeta.Namespace = namespace
	cm, err = c.CoreV1().ConfigMaps(namespace).Create(cm)
	if err != nil {
		return nil, err
	}
	return func() {
		if err := c.CoreV1().ConfigMaps(namespace).Delete(cm.Name, &metav1.DeleteOptions{}); err != nil {
			e2elog.Logf("Failed to delete SE configmap %s: %v", cm.Name, err)
		}
	}, nil
}

func buildDaemonSet(c clientset.Interface, namespace, manifestFile string) (func(), error) {
	file, err := ioutil.ReadFile(path.Join("/tmp", extenderManifestsFolder, manifestFile))
	if err != nil {
		return nil, err
	}

	ds := &appsv1.DaemonSet{}
	err = yaml.Unmarshal(file, ds)
	if err != nil {
		return nil, err
	}

	ds.ObjectMeta.Namespace = namespace
	ds, err = c.AppsV1().DaemonSets(namespace).Create(ds)
	if err != nil {
		return nil, err
	}
	return func() {
		if err := c.AppsV1().DaemonSets(namespace).Delete(ds.Name, &metav1.DeleteOptions{}); err != nil {
			e2elog.Logf("Failed to delete daemonset %s: %v", ds.Name, err)
		}
	}, nil
}

func newSchedulerRestartChecker(client clientset.Interface) *schedulerRestartChecker {
	return &schedulerRestartChecker{
		c: client,
	}
}

type schedulerRestartChecker struct {
	c            clientset.Interface
	initialState map[string]metav1.Time
}

func (rc *schedulerRestartChecker) ReadInitialState() error {
	var err error
	rc.initialState, err = getPODStartTimeMap(rc.c)
	if err != nil {
		return err
	}
	if len(rc.initialState) == 0 {
		return fmt.Errorf("can't find schedulers PODs during reading initial state")
	}
	return nil
}

func (rc *schedulerRestartChecker) CheckRestarted() (bool, error) {
	currentState, err := getPODStartTimeMap(rc.c)
	if err != nil {
		return false, err
	}
	for podName, initialTime := range rc.initialState {
		currentTime, ok := currentState[podName]
		if !ok {
			// podName not found
			return false, nil
		}
		// check that POD start time changed
		if !currentTime.After(initialTime.Time) {
			// at lease on pod not restarted yet
			return false, nil
		}
		// check that POD uptime more than 10 seconds
		// we need to wait additional 10 seconds to protect from CrashLoopBackOff caused by frequently POD restarts
		if time.Since(currentTime.Time).Seconds() <= 10 {
			return false, nil
		}
	}
	return true, nil
}

func getPODStartTimeMap(client clientset.Interface) (map[string]metav1.Time, error) {
	pods, err := findSchedulerPods(client)
	if err != nil {
		return nil, err
	}
	return buildPODStartTimeMap(pods), nil
}

func buildPODStartTimeMap(pods *corev1.PodList) map[string]metav1.Time {
	data := map[string]metav1.Time{}
	for _, p := range pods.Items {
		if len(p.Status.ContainerStatuses) == 0 {
			continue
		}
		if p.Status.ContainerStatuses[0].State.Running == nil {
			data[p.Name] = metav1.Time{}
			continue
		}
		data[p.Name] = p.Status.ContainerStatuses[0].State.Running.StartedAt
	}
	return data
}

func findSchedulerPods(client clientset.Interface) (*corev1.PodList, error) {
	pods, err := client.CoreV1().Pods("").List(metav1.ListOptions{LabelSelector: schedulerLabel})
	if err != nil {
		return nil, err
	}
	e2elog.Logf("Find %d scheduler pods", len(pods.Items))
	return pods, nil
}
