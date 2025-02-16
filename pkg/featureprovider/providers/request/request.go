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

package request

import (
	"sync"

	kstoneapiv1 "tkestack.io/kstone/pkg/apis/kstone/v1alpha1"
	"tkestack.io/kstone/pkg/featureprovider"
	"tkestack.io/kstone/pkg/inspection"
)

const (
	ProviderName = string(kstoneapiv1.KStoneFeatureRequest)
)

type FeatureRequest struct {
	name       string
	once       sync.Once
	inspection *inspection.Server
	ctx        *featureprovider.FeatureContext
}

func init() {
	featureprovider.RegisterFeatureFactory(
		ProviderName,
		func(ctx *featureprovider.FeatureContext) (featureprovider.Feature, error) {
			return NewFeatureRequest(ctx)
		},
	)
}

func NewFeatureRequest(ctx *featureprovider.FeatureContext) (featureprovider.Feature, error) {
	return &FeatureRequest{
		name: ProviderName,
		ctx:  ctx,
	}, nil
}

func (c *FeatureRequest) Init() error {
	var err error
	c.once.Do(func() {
		c.inspection = &inspection.Server{
			Clientbuilder: c.ctx.Clientbuilder,
		}
		err = c.inspection.Init()
	})
	return err
}

func (c *FeatureRequest) Equal(cluster *kstoneapiv1.EtcdCluster) bool {
	return c.inspection.IsNotFound(cluster, ProviderName)
}

func (c *FeatureRequest) Sync(cluster *kstoneapiv1.EtcdCluster) error {
	return c.inspection.AddRequestTask(cluster, ProviderName)
}

func (c *FeatureRequest) Do(inspection *kstoneapiv1.EtcdInspection) error {
	return c.inspection.CollectEtcdClusterRequest(inspection)
}
