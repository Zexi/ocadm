// Copyright 2019 Yunion
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

package multicloud

import (
	"fmt"

	"yunion.io/x/pkg/errors"

	apis "yunion.io/x/onecloud/pkg/apis/compute"
	"yunion.io/x/onecloud/pkg/cloudprovider"
)

type SVpc struct {
	SResourceBase
}

func (self *SVpc) GetINatGateways() ([]cloudprovider.ICloudNatGateway, error) {
	return nil, fmt.Errorf("Not Implemented GetNatGateways")
}

func (self *SVpc) IsSupportSetExternalAccess() bool {
	return false
}

func (self *SVpc) GetExternalAccessMode() string {
	return apis.VPC_EXTERNAL_ACCESS_MODE_EIP
}

func (self *SVpc) AttachInternetGateway(igwId string) error {
	return errors.Wrap(cloudprovider.ErrNotSupported, "AttachInternetGateway")
}
