/*
 * Tencent is pleased to support the open source community by making TKEStack
 * available.
 *
 * Copyright (C) 2012-2023 Tencent. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use
 * this file except in compliance with the License. You may obtain a copy of the
 * License at
 *
 * https://opensource.org/licenses/Apache-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
 * WARRANTIES OF ANY KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations under the License.
 */

package kstone

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"go.etcd.io/etcd/client/pkg/v3/transport"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kstoneapiv1 "tkestack.io/kstone/pkg/apis/kstone/v1alpha1"
	"tkestack.io/kstone/pkg/clusterprovider"
	"tkestack.io/kstone/pkg/controllers/util"
	platformscheme "tkestack.io/kstone/pkg/generated/clientset/versioned/scheme"
)

const (
	providerName    = kstoneapiv1.EtcdClusterKstone
	AnnoImportedURI = "importedAddr"
)

type EtcdClusterKstone struct {
	name    kstoneapiv1.EtcdClusterType
	cluster *kstoneapiv1.EtcdCluster
}

func init() {
	clusterprovider.RegisterEtcdClusterFactory(
		providerName,
		func(cluster *kstoneapiv1.EtcdCluster) (clusterprovider.EtcdClusterProvider, error) {
			return NewEtcdClusterKstone(cluster)
		},
	)
}

// NewEtcdClusterKstone generates etcd-operator provider
func NewEtcdClusterKstone(cluster *kstoneapiv1.EtcdCluster) (clusterprovider.EtcdClusterProvider, error) {
	return &EtcdClusterKstone{
		name:    providerName,
		cluster: cluster,
	}, nil
}

func (c *EtcdClusterKstone) BeforeCreate() error {
	return nil
}

// Create creates an etcd cluster
func (c *EtcdClusterKstone) Create() error {
	etcdRes := schema.GroupVersionResource{Group: "etcd.tkestack.io", Version: "v1alpha1", Resource: "etcdclusters"}
	etcdcluster := map[string]interface{}{
		"apiVersion": "etcd.tkestack.io/v1alpha1",
		"kind":       "EtcdCluster",
		"metadata": map[string]interface{}{
			"name":      c.cluster.Name,
			"namespace": c.cluster.Namespace,
		},
		"spec": c.generateEtcdSpec(),
	}

	etcdclusterRequest := &unstructured.Unstructured{
		Object: etcdcluster,
	}

	err := controllerutil.SetOwnerReference(c.cluster, etcdclusterRequest, platformscheme.Scheme)
	if err != nil {
		return err
	}

	_, err = clusterprovider.DynamicClient.Resource(etcdRes).
		Namespace(c.cluster.Namespace).
		Create(context.TODO(), etcdclusterRequest, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return err
	}

	return nil
}

// AfterCreate handles etcdcluster after created
func (c *EtcdClusterKstone) AfterCreate() error {
	if c.cluster.Annotations["scheme"] == "https" {
		c.cluster.Annotations["certName"] = fmt.Sprintf("%s/%s-etcd-client-cert", c.cluster.Namespace, c.cluster.Name)
	}

	c.cluster.Annotations["importedAddr"] = fmt.Sprintf(
		"%s://%s-etcd.%s.svc.cluster.local:2379",
		c.cluster.Annotations["scheme"],
		c.cluster.Name,
		c.cluster.Namespace,
	)
	// update extClientURL
	extClientURL := ""
	for i := 0; i < int(c.cluster.Spec.Size); i++ {
		key := fmt.Sprintf("%s-etcd-%d:2379", c.cluster.Name, i)
		value := fmt.Sprintf(
			"%s-etcd-%d.%s-etcd-headless.%s.svc.cluster.local:2379",
			c.cluster.Name,
			i,
			c.cluster.Name,
			c.cluster.Namespace,
		)
		if i < int(c.cluster.Spec.Size)-1 {
			extClientURL += fmt.Sprintf("%s->%s,", key, value)
		} else {
			extClientURL += fmt.Sprintf("%s->%s", key, value)
		}
	}
	c.cluster.Annotations["extClientURL"] = extClientURL
	return nil
}

// BeforeUpdate handles etcdcluster before updated
func (c *EtcdClusterKstone) BeforeUpdate() error {
	return nil
}

// Update updates cluster of kstone-etcd-operator
func (c *EtcdClusterKstone) Update() error {
	etcdRes := schema.GroupVersionResource{Group: "etcd.tkestack.io", Version: "v1alpha1", Resource: "etcdclusters"}
	etcd, err := clusterprovider.DynamicClient.Resource(etcdRes).
		Namespace(c.cluster.Namespace).
		Get(context.TODO(), c.cluster.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	err = c.updateEtcdSpec(etcd)
	if err != nil {
		return err
	}

	_, updateErr := clusterprovider.DynamicClient.Resource(etcdRes).
		Namespace(c.cluster.Namespace).
		Update(context.TODO(), etcd, metav1.UpdateOptions{})
	if updateErr != nil {
		klog.Error(updateErr.Error())
		return updateErr
	}
	return nil
}

// Equal checks etcdcluster, if not equal, sync etcdclusters.etcd.tkestack.io
// if equal, nothing to do
func (c *EtcdClusterKstone) Equal() (bool, error) {
	etcdRes := schema.GroupVersionResource{Group: "etcd.tkestack.io", Version: "v1alpha1", Resource: "etcdclusters"}
	etcd, err := clusterprovider.DynamicClient.Resource(etcdRes).
		Namespace(c.cluster.Namespace).
		Get(context.TODO(), c.cluster.Name, metav1.GetOptions{})
	if err != nil {
		return true, err
	}

	oldSize, _, _ := unstructured.NestedInt64(etcd.Object, "spec", "size")
	if int64(c.cluster.Spec.Size) != oldSize {
		klog.Info("size is different")
		return false, nil
	}

	oldVersion, _, _ := unstructured.NestedString(etcd.Object, "spec", "version")
	if strings.TrimLeft(oldVersion, "v") != strings.TrimLeft(c.cluster.Spec.Version, "v") {
		klog.Info("version is different")
		return false, nil
	}

	oldStorage, _, _ := unstructured.NestedString(
		etcd.Object,
		"spec",
		"template",
		"persistentVolumeClaimSpec",
		"resources",
		"requests",
		"storage",
	)
	if strings.TrimRight(oldStorage, "Gi") != strconv.Itoa(int(c.cluster.Spec.DiskSize)) {
		klog.Info("storage is different")
		return false, nil
	}

	oldCPU, _, _ := unstructured.NestedString(etcd.Object, "spec", "template", "resources", "requests", "cpu")
	if oldCPU != strconv.Itoa(int(c.cluster.Spec.TotalCpu)) {
		klog.Info("cpu is different")
		return false, nil
	}

	oldMemory, _, _ := unstructured.NestedString(
		etcd.Object,
		"spec",
		"template",
		"resources",
		"requests",
		"memory",
	)
	if strings.TrimRight(oldMemory, "Gi") != strconv.Itoa(int(c.cluster.Spec.TotalMem)) {
		klog.Info("memory is different")
		return false, nil
	}

	oldEnvObject, _, _ := unstructured.NestedSlice(etcd.Object, "spec", "template", "env")
	oldEnv := make([]corev1.EnvVar, 0)
	oldEnvBytes, err := json.Marshal(oldEnvObject)
	if err != nil {
		return true, err
	}
	err = json.Unmarshal(oldEnvBytes, &oldEnv)
	if err != nil {
		return true, err
	}
	if len(oldEnv) == 0 && len(c.cluster.Spec.Env) == 0 {
		return true, nil
	}
	if !reflect.DeepEqual(oldEnv, c.cluster.Spec.Env) {
		klog.Info("env is different")
		return false, nil
	}

	return true, nil
}

// AfterUpdate handles etcdcluster after updated
func (c *EtcdClusterKstone) AfterUpdate() error {
	return nil
}

// BeforeDelete handles etcdcluster before deleted
func (c *EtcdClusterKstone) BeforeDelete() error {
	return nil
}

// Delete handles delete
func (c *EtcdClusterKstone) Delete() error {
	return nil
}

// AfterDelete handles etcdcluster after deleted
func (c *EtcdClusterKstone) AfterDelete() error {
	return nil
}

// Status checks etcd member and returns new status
func (c *EtcdClusterKstone) Status(tlsConfig *transport.TLSInfo) (kstoneapiv1.EtcdClusterStatus, error) {
	var phase kstoneapiv1.EtcdClusterPhase

	status := c.cluster.Status

	annotations := c.cluster.Annotations
	if annotations == nil {
		annotations = make(map[string]string)
	}

	// endpoints
	endpoints := clusterprovider.GetStorageMemberEndpoints(c.cluster)

	if len(endpoints) == 0 {
		if addr, found := annotations[AnnoImportedURI]; found {
			endpoints = append(endpoints, addr)
			status.ServiceName = addr
		} else {
			status.Phase = kstoneapiv1.EtcdCluterCreating
			return status, nil
		}
	}

	members, err := clusterprovider.GetRuntimeEtcdMembers(
		endpoints,
		c.cluster.Annotations[util.ClusterExtensionClientURL],
		tlsConfig,
	)
	if err != nil || len(members) == 0 || int(c.cluster.Spec.Size) != len(members) {
		if status.Phase == kstoneapiv1.EtcdClusterRunning {
			status.Phase = kstoneapiv1.EtcdClusterUnknown
		}
		return status, err
	}

	status.Members, phase = clusterprovider.GetEtcdClusterMemberStatus(members, tlsConfig)
	if status.Phase == kstoneapiv1.EtcdClusterRunning || phase != kstoneapiv1.EtcdClusterUnknown {
		status.Phase = phase
	}
	return status, err
}

// updateEtcdSpec update spec
func (c *EtcdClusterKstone) updateEtcdSpec(etcd *unstructured.Unstructured) error {
	newSpec := c.generateEtcdSpec()

	spec, found, err := unstructured.NestedMap(etcd.Object, "spec")
	if err != nil || !found || spec == nil {
		return fmt.Errorf("get spec error")
	}

	if err = unstructured.SetNestedField(etcd.Object, newSpec, "spec"); err != nil {
		klog.Error(err.Error())
		return err
	}

	return nil
}

// generateEtcdSpec generate spec with etcdcluster
func (c *EtcdClusterKstone) generateEtcdSpec() map[string]interface{} {
	extraServerCertSANsStr := c.cluster.Annotations["extraServerCertSANs"]
	extraServerCertSANList := make([]interface{}, 0)
	for _, certSAN := range strings.Split(extraServerCertSANsStr, ",") {
		temp := strings.TrimSpace(certSAN)
		if temp == "" {
			continue
		}
		extraServerCertSANList = append(extraServerCertSANList, temp)
	}
	if len(extraServerCertSANList) == 0 {
		extraServerCertSANList = nil
	}

	labels := make(map[string]interface{}, len(c.cluster.Labels))
	for k, v := range c.cluster.Labels {
		labels[k] = v
	}
	annotations := make(map[string]interface{}, len(c.cluster.Annotations))
	for k, v := range c.cluster.Annotations {
		annotations[k] = v
	}
	env := make([]interface{}, 0)
	envBytes, _ := json.Marshal(c.cluster.Spec.Env)
	_ = json.Unmarshal(envBytes, &env)

	spec := map[string]interface{}{
		"size":    int64(c.cluster.Spec.Size),
		"version": c.cluster.Spec.Version,
		"template": map[string]interface{}{
			"extraArgs": []interface{}{
				"logger=zap",
			},
			"labels":      labels,
			"annotations": annotations,
			"env":         env,
			"persistentVolumeClaimSpec": map[string]interface{}{
				"accessModes": []interface{}{
					"ReadWriteOnce",
				},
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{
						"storage": fmt.Sprintf("%dGi", c.cluster.Spec.DiskSize),
					},
				},
			},
			"resources": map[string]interface{}{
				"requests": map[string]interface{}{
					"cpu":    fmt.Sprintf("%d", c.cluster.Spec.TotalCpu),
					"memory": fmt.Sprintf("%dGi", c.cluster.Spec.TotalMem),
				},
				"limits": map[string]interface{}{
					"cpu":    fmt.Sprintf("%d", c.cluster.Spec.TotalCpu),
					"memory": fmt.Sprintf("%dGi", c.cluster.Spec.TotalMem),
				},
			},
		},
	}

	if c.cluster.Annotations["scheme"] == "https" {
		spec["secure"] = map[string]interface{}{
			"tls": map[string]interface{}{
				"autoTLSCert": map[string]interface{}{
					"autoGenerateClientCert": true,
					"autoGeneratePeerCert":   true,
					"autoGenerateServerCert": true,
					"extraServerCertSANs":    extraServerCertSANList,
				},
			},
		}

		spec["template"].(map[string]interface{})["extraArgs"] = []interface{}{
			"logger=zap",
			"client-cert-auth=true",
		}
	}
	return spec
}