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

package onecloud

import (
	"net/http"
	"strings"

	"golang.org/x/sync/errgroup"

	"yunion.io/x/jsonutils"
	"yunion.io/x/log"
	ansibleapi "yunion.io/x/onecloud/pkg/apis/ansible"
	devtoolapi "yunion.io/x/onecloud/pkg/apis/devtool"
	monitorapi "yunion.io/x/onecloud/pkg/apis/monitor"
	"yunion.io/x/onecloud/pkg/mcclient"
	"yunion.io/x/onecloud/pkg/mcclient/modulebase"
	"yunion.io/x/onecloud/pkg/mcclient/modules"
	k8smod "yunion.io/x/onecloud/pkg/mcclient/modules/k8s"
	"yunion.io/x/onecloud/pkg/mcclient/options/k8s"
	"yunion.io/x/onecloud/pkg/util/ansible"
	"yunion.io/x/onecloud/pkg/util/httputils"
	"yunion.io/x/pkg/errors"

	"yunion.io/x/onecloud-operator/pkg/apis/constants"
	"yunion.io/x/onecloud-operator/pkg/apis/onecloud/v1alpha1"
)

const (
	NotFoundMsg          = "NotFoundError"
	K8SSystemClusterName = "system-default"
)

func IsNotFoundError(err error) bool {
	if httpErr, ok := err.(*httputils.JSONClientError); ok {
		if httpErr.Code == http.StatusNotFound {
			return true
		}
	}
	if strings.Contains(err.Error(), NotFoundMsg) {
		return true
	}
	return false
}

func IsResourceExists(s *mcclient.ClientSession, manager modulebase.Manager, name string) (jsonutils.JSONObject, bool, error) {
	obj, err := manager.Get(s, name, nil)
	if err == nil {
		return obj, true, nil
	}
	if IsNotFoundError(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func EnsureResource(
	s *mcclient.ClientSession,
	man modulebase.Manager,
	name string,
	createFunc func() (jsonutils.JSONObject, error),
) (jsonutils.JSONObject, error) {
	obj, exists, err := IsResourceExists(s, man, name)
	if err != nil {
		return nil, err
	}
	if exists {
		return obj, nil
	}
	return createFunc()
}

func DeleteResource(
	s *mcclient.ClientSession,
	man modulebase.Manager,
	name string,
) error {
	obj, exists, err := IsResourceExists(s, man, name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	id, _ := obj.GetString("id")
	_, err = man.Delete(s, id, nil)
	return err
}

func IsRoleExists(s *mcclient.ClientSession, roleName string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.RolesV3, roleName)
}

func CreateRole(s *mcclient.ClientSession, roleName, description string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(roleName), "name")
	if description != "" {
		params.Add(jsonutils.NewString(description), "description")
	}
	return modules.RolesV3.Create(s, params)
}

func EnsureRole(s *mcclient.ClientSession, roleName, description string) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.RolesV3, roleName, func() (jsonutils.JSONObject, error) {
		return CreateRole(s, roleName, description)
	})
}

func IsServiceExists(s *mcclient.ClientSession, svcName string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.ServicesV3, svcName)
}

func EnsureService(s *mcclient.ClientSession, svcName, svcType string) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.ServicesV3, svcName, func() (jsonutils.JSONObject, error) {
		return CreateService(s, svcName, svcType)
	})
}

func EnsureServiceCertificate(s *mcclient.ClientSession, certName string, certDetails *jsonutils.JSONDict) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.ServiceCertificatesV3, certName, func() (jsonutils.JSONObject, error) {
		return CreateServiceCertificate(s, certName, certDetails)
	})
}

func CreateServiceCertificate(s *mcclient.ClientSession, certName string, certDetails *jsonutils.JSONDict) (jsonutils.JSONObject, error) {
	certDetails.Add(jsonutils.NewString(certName), "name")
	return modules.ServiceCertificatesV3.Create(s, certDetails)
}

func CreateService(s *mcclient.ClientSession, svcName, svcType string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(svcType), "type")
	params.Add(jsonutils.NewString(svcName), "name")
	params.Add(jsonutils.JSONTrue, "enabled")
	return modules.ServicesV3.Create(s, params)
}

func IsEndpointExists(s *mcclient.ClientSession, svcId, regionId, interfaceType string) (jsonutils.JSONObject, bool, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(svcId), "service_id")
	params.Add(jsonutils.NewString(regionId), "region_id")
	params.Add(jsonutils.NewString(interfaceType), "interface")
	eps, err := modules.EndpointsV3.List(s, params)
	if err != nil {
		return nil, false, err
	}
	if len(eps.Data) == 0 {
		return nil, false, nil
	}
	return eps.Data[0], true, nil
}

func EnsureEndpoint(
	s *mcclient.ClientSession, svcId, regionId, interfaceType, url, serviceCert string,
) (jsonutils.JSONObject, error) {
	ep, exists, err := IsEndpointExists(s, svcId, regionId, interfaceType)
	if err != nil {
		return nil, err
	}
	if !exists {
		createParams := jsonutils.NewDict()
		createParams.Add(jsonutils.NewString(svcId), "service_id")
		createParams.Add(jsonutils.NewString(regionId), "region_id")
		createParams.Add(jsonutils.NewString(interfaceType), "interface")
		createParams.Add(jsonutils.NewString(url), "url")
		createParams.Add(jsonutils.JSONTrue, "enabled")
		if len(serviceCert) > 0 {
			createParams.Add(jsonutils.NewString(serviceCert), "service_certificate")
		}
		return modules.EndpointsV3.Create(s, createParams)
	}
	epId, err := ep.GetString("id")
	if err != nil {
		return nil, err
	}
	epUrl, _ := ep.GetString("url")
	enabled, _ := ep.Bool("enabled")
	if epUrl == url && enabled {
		// same endpoint exists and already exists
		return ep, nil
	}
	updateParams := jsonutils.NewDict()
	updateParams.Add(jsonutils.NewString(url), "url")
	updateParams.Add(jsonutils.JSONTrue, "enabled")
	if len(serviceCert) > 0 {
		updateParams.Add(jsonutils.NewString(serviceCert), "service_certificate")
	}
	return modules.EndpointsV3.Update(s, epId, updateParams)
}

func IsUserExists(s *mcclient.ClientSession, username string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.UsersV3, username)
}

func CreateUser(s *mcclient.ClientSession, username string, password string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(username), "name")
	params.Add(jsonutils.NewString(password), "password")
	params.Add(jsonutils.JSONTrue, "is_system_account")
	return modules.UsersV3.Create(s, params)
}

func ChangeUserPassword(s *mcclient.ClientSession, username string, password string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(password), "password")
	return modules.UsersV3.Update(s, username, params)
}

func ProjectAddUser(s *mcclient.ClientSession, projectId string, userId string, roleId string) error {
	_, err := modules.RolesV3.PutInContexts(s, roleId, nil,
		[]modulebase.ManagerContext{
			{InstanceManager: &modules.Projects, InstanceId: projectId},
			{InstanceManager: &modules.UsersV3, InstanceId: userId},
		})
	return err
}

func IsZoneExists(s *mcclient.ClientSession, zone string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.Zones, zone)
}

func CreateZone(s *mcclient.ClientSession, zone string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(zone), "name")
	return modules.Zones.Create(s, params)
}

func IsWireExists(s *mcclient.ClientSession, wire string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.Wires, wire)
}

func CreateWire(s *mcclient.ClientSession, zone string, wire string, bw int, vpc string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(wire), "name")
	params.Add(jsonutils.NewInt(int64(bw)), "bandwidth")
	params.Add(jsonutils.NewString(vpc), "vpc")
	return modules.Wires.CreateInContext(s, params, &modules.Zones, zone)
}

func IsNetworkExists(s *mcclient.ClientSession, net string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.Networks, net)
}

func CreateNetwork(
	s *mcclient.ClientSession,
	name string,
	gateway string,
	serverType string,
	wireId string,
	maskLen int,
	startIp string,
	endIp string,
) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(name), "name")
	params.Add(jsonutils.NewString(startIp), "guest_ip_start")
	params.Add(jsonutils.NewString(endIp), "guest_ip_end")
	params.Add(jsonutils.NewInt(int64(maskLen)), "guest_ip_mask")
	if gateway != "" {
		params.Add(jsonutils.NewString(gateway), "guest_gateway")
	}
	if serverType != "" {
		params.Add(jsonutils.NewString(serverType), "server_type")
	}
	return modules.Networks.CreateInContext(s, params, &modules.Wires, wireId)
}

func NetworkPrivate(s *mcclient.ClientSession, name string) (jsonutils.JSONObject, error) {
	return modules.Networks.PerformAction(s, "private", name, nil)
}

func CreateRegion(s *mcclient.ClientSession, region, zone string) (jsonutils.JSONObject, error) {
	if zone != "" {
		region = mcclient.RegionID(region, zone)
	}
	obj, err := modules.Regions.Get(s, region, nil)
	if err == nil {
		// region already exists
		return obj, nil
	}
	if !IsNotFoundError(err) {
		return nil, err
	}
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(region), "id")
	return modules.Regions.Create(s, params)
}

func IsSchedtagExists(s *mcclient.ClientSession, name string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.Schedtags, name)
}

func CreateSchedtag(s *mcclient.ClientSession, name string, strategy string, description string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(name), "name")
	params.Add(jsonutils.NewString(strategy), "default_strategy")
	params.Add(jsonutils.NewString(description), "description")
	return modules.Schedtags.Create(s, params)
}

func EnsureSchedtag(s *mcclient.ClientSession, name string, strategy string, description string) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.Schedtags, name, func() (jsonutils.JSONObject, error) {
		return CreateSchedtag(s, name, strategy, description)
	})
}

func IsDynamicSchedtagExists(s *mcclient.ClientSession, name string) (jsonutils.JSONObject, bool, error) {
	return IsResourceExists(s, &modules.Dynamicschedtags, name)
}

func CreateDynamicSchedtag(s *mcclient.ClientSession, name, schedtag, condition string) (jsonutils.JSONObject, error) {
	params := jsonutils.NewDict()
	params.Add(jsonutils.NewString(name), "name")
	params.Add(jsonutils.NewString(schedtag), "schedtag")
	params.Add(jsonutils.NewString(condition), "condition")
	params.Add(jsonutils.JSONTrue, "enabled")
	return modules.Dynamicschedtags.Create(s, params)
}

func EnsureDynamicSchedtag(s *mcclient.ClientSession, name, schedtag, condition string) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.Dynamicschedtags, name, func() (jsonutils.JSONObject, error) {
		return CreateDynamicSchedtag(s, name, schedtag, condition)
	})
}

func GetEndpointsByService(s *mcclient.ClientSession, serviceName string) ([]jsonutils.JSONObject, error) {
	obj, err := modules.ServicesV3.Get(s, serviceName, nil)
	if err != nil {
		return nil, err
	}
	svcId, _ := obj.GetString("id")
	searchParams := jsonutils.NewDict()
	searchParams.Add(jsonutils.NewString(svcId), "service_id")
	ret, err := modules.EndpointsV3.List(s, searchParams)
	if err != nil {
		return nil, err
	}
	return ret.Data, nil
}

func DisableService(s *mcclient.ClientSession, id string) error {
	params := jsonutils.NewDict()
	params.Add(jsonutils.JSONFalse, "enabled")
	_, err := modules.ServicesV3.Patch(s, id, params)
	return err
}

func DisableEndpoint(s *mcclient.ClientSession, id string) error {
	params := jsonutils.NewDict()
	params.Add(jsonutils.JSONFalse, "enabled")
	_, err := modules.EndpointsV3.Patch(s, id, params)
	return err
}

func DeleteServiceEndpoints(s *mcclient.ClientSession, serviceName string) error {
	endpoints, err := GetEndpointsByService(s, serviceName)
	if err != nil {
		if IsNotFoundError(err) {
			return nil
		}
		return err
	}
	for _, ep := range endpoints {
		id, _ := ep.GetString("id")
		tmpId := id
		if err := DisableEndpoint(s, tmpId); err != nil {
			return err
		}
		if _, err := modules.EndpointsV3.Delete(s, id, nil); err != nil {
			return err
		}
	}
	if err := DisableService(s, serviceName); err != nil {
		return err
	}
	return DeleteResource(s, &modules.ServicesV3, serviceName)
}

func InitServiceAccount(s *mcclient.ClientSession, username string, password string) error {
	obj, exists, err := IsUserExists(s, username)
	if err != nil {
		return err
	}
	if exists {
		id, _ := obj.GetString("id")
		if _, err := ChangeUserPassword(s, id, password); err != nil {
			return errors.Wrapf(err, "user %s already exists, update password", username)
		}
		return nil
	}
	obj, err = CreateUser(s, username, password)
	if err != nil {
		return errors.Wrapf(err, "create user %s", username)
	}
	userId, _ := obj.GetString("id")
	return ProjectAddUser(s, constants.SysAdminProject, userId, constants.RoleAdmin)
}

func RegisterServiceEndpoints(
	s *mcclient.ClientSession,
	regionId string,
	serviceName string,
	serviceType string,
	serviceCert string,
	interfaceUrls map[string]string,
) error {
	svc, err := EnsureService(s, serviceName, serviceType)
	if err != nil {
		return err
	}
	svcId, err := svc.GetString("id")
	if err != nil {
		return err
	}
	errgrp := &errgroup.Group{}
	for inf, endpointUrl := range interfaceUrls {
		tmpInf := inf
		tmpUrl := endpointUrl
		errgrp.Go(func() error {
			_, err = EnsureEndpoint(s, svcId, regionId, tmpInf, tmpUrl, serviceCert)
			if err != nil {
				return err
			}
			return nil
		})
	}
	return errgrp.Wait()
}

func RegisterServiceEndpointByInterfaces(
	s *mcclient.ClientSession,
	regionId string,
	serviceName string,
	serviceType string,
	endpointUrl string,
	serviceCert string,
	interfaces []string,
) error {
	urls := make(map[string]string)
	for _, inf := range interfaces {
		urls[inf] = endpointUrl
	}
	return RegisterServiceEndpoints(s, regionId, serviceName, serviceType, serviceCert, urls)
}

func RegisterServicePublicInternalEndpoint(
	s *mcclient.ClientSession,
	regionId string,
	serviceName string,
	serviceType string,
	serviceCert string,
	endpointUrl string,
) error {
	return RegisterServiceEndpointByInterfaces(s, regionId, serviceName, serviceType,
		endpointUrl, serviceCert, []string{constants.EndpointTypeInternal, constants.EndpointTypePublic})
}

func ToPlaybook(
	hostLines []string,
	mods []string,
	files map[string]string,
) (*ansible.Playbook, error) {
	if len(mods) == 0 {
		return nil, errors.Errorf("Requires at least one mod")
	}
	if len(hostLines) == 0 {
		return nil, errors.Errorf("Requires as least one server/host to operator on")
	}
	pb := ansible.NewPlaybook()
	hosts := []ansible.Host{}
	for _, s := range hostLines {
		host, err := ansible.ParseHostLine(s)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	pb.Inventory = ansible.Inventory{Hosts: hosts}
	for _, s := range mods {
		module, err := ansible.ParseModuleLine(s)
		if err != nil {
			return nil, err
		}
		pb.Modules = append(pb.Modules, module)
	}
	pb.Files = make(map[string][]byte)
	for name, data := range files {
		pb.Files[name] = []byte(data)
	}
	return pb, nil
}

func DevtoolTemplateCreateParams(
	name string,
	hostLines []string,
	mods []string,
	files map[string]string,
	day int64,
	hour int64,
	min int64,
	sec int64,
	interval int64,
	start bool,
	enabled bool,
) (*jsonutils.JSONDict, error) {
	pb, err := ToPlaybook(hostLines, mods, files)
	if err != nil {
		return nil, err
	}
	input := ansibleapi.AnsiblePlaybookCreateInput{
		Name:     name,
		Playbook: *pb,
	}
	params := input.JSON(input)
	params.Add(jsonutils.NewInt(day), "day")
	params.Add(jsonutils.NewInt(hour), "hour")
	params.Add(jsonutils.NewInt(min), "min")
	params.Add(jsonutils.NewInt(sec), "sec")
	params.Add(jsonutils.NewInt(interval), "interval")
	params.Add(jsonutils.NewBool(start), "start")
	params.Add(jsonutils.NewBool(enabled), "enabled")
	return params, nil
}

func CreateDevtoolTemplate(
	s *mcclient.ClientSession,
	name string,
	hosts []string,
	mods []string,
	files map[string]string,
	interval int64,
) (jsonutils.JSONObject, error) {
	params, err := DevtoolTemplateCreateParams(name, hosts, mods, files, 0, 0, 0, 0, interval, false, true)
	if err != nil {
		return nil, errors.Wrapf(err, "get devtool template %s create params", name)
	}
	return modules.DevToolTemplates.Create(s, params)
}

func EnsureDevtoolTemplate(
	s *mcclient.ClientSession,
	name string,
	hosts []string,
	mods []string,
	files map[string]string,
	interval int64,
) (jsonutils.JSONObject, error) {
	return EnsureResource(s, &modules.DevToolTemplates, name, func() (jsonutils.JSONObject, error) {
		return CreateDevtoolTemplate(s, name, hosts, mods, files, interval)
	})
}

func SyncServiceConfig(
	s *mcclient.ClientSession, syncConf map[string]string, serviceName string,
) (jsonutils.JSONObject, error) {
	iconf, err := modules.ServicesV3.GetSpecific(s, serviceName, "config", nil)
	if err != nil {
		return nil, err
	}
	conf := iconf.(*jsonutils.JSONDict)
	if !conf.Contains("config") {
		conf.Add(jsonutils.NewDict(), "config")
	}
	if !conf.Contains("config", "default") {
		conf.Add(jsonutils.NewDict(), "config", "default")
	}
	for k, v := range syncConf {
		if _, ok := conf.GetString("config", "default", k); ok == nil {
			continue
		} else {
			conf.Add(jsonutils.NewString(v), "config", "default", k)
		}
	}
	return modules.ServicesV3.PerformAction(s, serviceName, "config", conf)
}

type CommonAlertTem struct {
	Database    string `json:"database"`
	Measurement string `json:"measurement"`
	//rule operator rule [and|or]
	Operator    string   `json:"operator"`
	Field       []string `json:"field"`
	FieldFunc   string   `json:"field_func"`
	Description string   `json:"description"`

	Reduce        string
	Comparator    string  `json:"comparator"`
	Threshold     float64 `json:"threshold"`
	Filters       []monitorapi.MetricQueryTag
	FieldOpt      string `json:"field_opt"`
	GetPointStr   bool   `json:"get_point_str"`
	Name          string
	ConditionType string `json:"condition_type"`
	From          string `json:"from"`
	Interval      string `json:"interval"`
	GroupBy       string `json:"group_by"`
}

func GetCommonAlertOfSys(session *mcclient.ClientSession) ([]jsonutils.JSONObject, error) {
	param := jsonutils.NewDict()
	param.Add(jsonutils.NewBool(true), "details")
	param.Add(jsonutils.NewString(monitorapi.CommonAlertSystemAlertType), "alert_type")
	param.Add(jsonutils.NewString("system"), "scope")

	rtn, err := modules.CommonAlertManager.List(session, param)
	if err != nil {
		return nil, err
	}
	return rtn.Data, nil
}

func CreateCommonAlert(s *mcclient.ClientSession, tem CommonAlertTem) (jsonutils.JSONObject, error) {
	commonAlert := newCommonalertQuery(tem)
	input := monitorapi.CommonAlertCreateInput{
		CommonMetricInputQuery: monitorapi.CommonMetricInputQuery{
			MetricQuery: []*monitorapi.CommonAlertQuery{&commonAlert},
		},
		AlertCreateInput: monitorapi.AlertCreateInput{
			Name:  tem.Name,
			Level: "important",
		},
		Recipients: []string{monitorapi.CommonAlertDefaultRecipient},
		AlertType:  monitorapi.CommonAlertSystemAlertType,
		Scope:      "system",
	}
	if len(tem.From) != 0 {
		input.From = tem.From
	}
	if len(tem.Interval) != 0 {
		input.Interval = tem.Interval
	}
	param := jsonutils.Marshal(&input)
	if tem.GetPointStr {
		param.(*jsonutils.JSONDict).Set("get_point_str", jsonutils.JSONTrue)
	}
	if len(tem.Description) != 0 {
		param.(*jsonutils.JSONDict).Set("description", jsonutils.NewString(tem.Description))
	}
	param.(*jsonutils.JSONDict).Set("meta_name", jsonutils.NewString(tem.Name))
	return modules.CommonAlertManager.Create(s, param)
}

func UpdateCommonAlert(s *mcclient.ClientSession, tem CommonAlertTem, id string,
	alert jsonutils.JSONObject) (jsonutils.JSONObject, error) {
	commonAlert := newCommonalertQuery(tem)
	input := monitorapi.CommonAlertUpdateInput{
		CommonMetricInputQuery: monitorapi.CommonMetricInputQuery{
			MetricQuery: []*monitorapi.CommonAlertQuery{&commonAlert},
		},
	}
	if len(tem.From) != 0 {
		input.From = tem.From
	}
	if len(tem.Interval) != 0 {
		input.Interval = tem.Interval
	}
	if len(tem.Description) != 0 {
		input.Description = tem.Description
	}
	diff, err := containDiffsWithRtnAlert(input, alert)
	if err != nil {
		return nil, errors.Wrap(err, "containDiffsWithRtnAlert error")
	}
	if !diff {
		return nil, nil
	}
	param := jsonutils.Marshal(&input)
	param.(*jsonutils.JSONDict).Set("force_update", jsonutils.JSONTrue)
	if tem.GetPointStr {
		param.(*jsonutils.JSONDict).Set("get_point_str", jsonutils.JSONTrue)
	}

	param.(*jsonutils.JSONDict).Set("meta_name", jsonutils.NewString(tem.Name))
	return modules.CommonAlertManager.Update(s, id, param)
}

func containDiffsWithRtnAlert(input monitorapi.CommonAlertUpdateInput, rtnAlert jsonutils.JSONObject) (bool, error) {
	conDiff := true
	alertSetting, err := rtnAlert.Get("settings")
	if err != nil {
		return conDiff, errors.Wrap(err, "get rtnAlert settings error")
	}
	setting := new(monitorapi.AlertSetting)
	err = alertSetting.Unmarshal(setting)
	if err != nil {
		return conDiff, errors.Wrap(err, "rtnAlert Unmarshal setting error")
	}
	alertDetails, err := rtnAlert.Get("common_alert_metric_details")
	if err != nil {
		return conDiff, errors.Wrap(err, "get common_alert_metric_details error")
	}
	details := make([]monitorapi.CommonAlertMetricDetails, 0)
	err = alertDetails.Unmarshal(&details)
	if err != nil {
		return conDiff, errors.Wrap(err, "rtnAlert Unmarshal common_alert_metric_details error")
	}
	if len(setting.Conditions) != len(input.CommonMetricInputQuery.MetricQuery) && len(setting.Conditions) != len(details) {
		return conDiff, nil
	}
	inputQuery := input.CommonMetricInputQuery.MetricQuery
	for i := range setting.Conditions {
		condi := setting.Conditions[i]
		if details[i].Comparator != inputQuery[i].Comparator {
			return conDiff, nil
		}
		oldSel := jsonutils.Marshal(&condi.Query.Model.Selects)
		newSel := jsonutils.Marshal(&inputQuery[i].Model.Selects)
		if !oldSel.Equals(newSel) {
			return conDiff, nil
		}
		oldTags := jsonutils.Marshal(&details[i].Filters)
		newTags := jsonutils.Marshal(&inputQuery[i].Model.Tags)
		if !oldTags.Equals(newTags) {
			return conDiff, nil
		}
	}
	return false, nil
}

func DeleteCommonAlert(s *mcclient.ClientSession, ids []string) {
	modules.CommonAlertManager.BatchDelete(s, ids, jsonutils.NewDict())
}

func newCommonalertQuery(tem CommonAlertTem) monitorapi.CommonAlertQuery {
	metricQ := monitorapi.MetricQuery{
		Alias:        "",
		Tz:           "",
		Database:     tem.Database,
		Measurement:  tem.Measurement,
		Tags:         make([]monitorapi.MetricQueryTag, 0),
		GroupBy:      make([]monitorapi.MetricQueryPart, 0),
		Selects:      nil,
		Interval:     "",
		Policy:       "",
		ResultFormat: "",
	}

	for _, field := range tem.Field {
		sel := monitorapi.MetricQueryPart{
			Type:   "field",
			Params: []string{field},
		}
		selectPart := []monitorapi.MetricQueryPart{sel}
		if len(tem.FieldFunc) != 0 {
			selectPart = append(selectPart, monitorapi.MetricQueryPart{
				Type:   tem.FieldFunc,
				Params: []string{},
			})
			if tem.GetPointStr {
				selectPart = append(selectPart, monitorapi.MetricQueryPart{
					Type:   "alias",
					Params: []string{field},
				})
			}
		} else {
			selectPart = append(selectPart, monitorapi.MetricQueryPart{
				Type:   "mean",
				Params: []string{},
			})
		}
		metricQ.Selects = append(metricQ.Selects, selectPart)
	}
	if len(tem.Filters) != 0 {
		for _, filter := range tem.Filters {
			metricQ.Tags = append(metricQ.Tags, filter)
		}
	}

	alertQ := new(monitorapi.AlertQuery)
	alertQ.Model = metricQ
	alertQ.From = "60m"

	commonAlert := monitorapi.CommonAlertQuery{
		AlertQuery: alertQ,
		Reduce:     tem.Reduce,
		Comparator: tem.Comparator,
		Threshold:  tem.Threshold,
	}
	if tem.FieldOpt != "" {
		commonAlert.FieldOpt = monitorapi.CommonAlertFieldOpt_Division
	}
	if len(tem.ConditionType) != 0 {
		commonAlert.ConditionType = tem.ConditionType
	}
	if len(tem.GroupBy) != 0 {
		commonAlert.Model.GroupBy = append(commonAlert.Model.GroupBy, monitorapi.MetricQueryPart{
			Type:   "field",
			Params: []string{tem.GroupBy},
		})
	}
	return commonAlert
}

var agentName = "monitor agent"

func EnsureAgentAnsiblePlaybookRef(s *mcclient.ClientSession) error {
	log.Infof("start to EnsureAgentAnsiblePlaybookRef")
	ctrue := true
	data, err := modules.AnsiblePlaybookReference.GetByName(s, agentName, nil)
	if err != nil {
		if httputils.ErrorCode(err) != 404 {
			return errors.Wrapf(err, "unable to get ansible playbook ref %q", agentName)
		}
		// create one
		params := ansibleapi.AnsiblePlaybookReferenceCreateInput{}
		params.SAnsiblePlaybookReference.Name = agentName
		params.SharableVirtualResourceCreateInput.Name = agentName
		params.Project = "system"
		params.IsPublic = &ctrue
		params.PublicScope = "system"
		params.PlaybookPath = "/opt/yunion/playbook/monitor-agent/playbook.yaml"
		params.Method = "offline"
		params.PlaybookParams = map[string]interface{}{
			"telegraf_agent_package_method":    "offline",
			"telegraf_agent_package_local_dir": "/opt/yunion/ansible-install-pkg",
		}
		data, err = modules.AnsiblePlaybookReference.Create(s, jsonutils.Marshal(params))
		if err != nil {
			return errors.Wrapf(err, "unable to create ansible playbook ref %q", agentName)
		}
	}
	refId, _ := data.GetString("id")
	_, err = modules.DevToolScripts.GetByName(s, agentName, nil)
	if err != nil {
		if httputils.ErrorCode(err) != 404 {
			return errors.Wrapf(err, "unable to get devtool script %q", agentName)
		}
		// create one
		params := devtoolapi.ScriptCreateInput{}
		params.Name = agentName
		params.Project = "system"
		params.IsPublic = &ctrue
		params.PublicScope = "system"
		params.PlaybookReference = refId
		params.MaxTryTimes = 3
		_, err := modules.DevToolScripts.Create(s, jsonutils.Marshal(params))
		if err != nil {
			return errors.Wrapf(err, "unable to create devtool script %q", agentName)
		}
	}
	return nil
}

func GetSystemCluster(s *mcclient.ClientSession) (jsonutils.JSONObject, error) {
	ret, err := k8smod.KubeClusters.Get(s, K8SSystemClusterName, nil)
	if err != nil {
		return nil, errors.Wrap(err, "Get kubernetes system default cluster")
	}
	return ret, nil
}

func SyncSystemCluster(s *mcclient.ClientSession, id string) error {
	_, err := k8smod.KubeClusters.PerformAction(s, id, "sync", nil)
	return err
}

func EnableMinio(s *mcclient.ClientSession) error {
	ret, err := GetSystemCluster(s)
	if err != nil {
		return err
	}
	id, err := ret.GetString("id")
	if err != nil {
		return errors.Wrapf(err, "Get kubernetes system default cluster id from %s", ret)
	}

	isEnabled, err := isMinioEnabled(s, id)
	if err != nil {
		return errors.Wrap(err, "Check minio is enabled")
	}
	if isEnabled {
		return nil
	}
	if err := SyncSystemCluster(s, id); err != nil {
		return errors.Wrap(err, "Sync system default cluster")
	}
	if err := enableMinio(s, id); err != nil {
		return errors.Wrap(err, "Enable system default cluster minio component")
	}
	return nil
}

type ComponentStatus struct {
	Id      string `json:"id"`
	Created bool   `json:"created"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}

type ClusterComponentsStatus struct {
	CephCSI   *ComponentStatus `json:"cephCSI"`
	Monitor   *ComponentStatus `json:"monitor"`
	FluentBit *ComponentStatus `json:"fluentbit"`
	Thanos    *ComponentStatus `json:"thanos"`
	Minio     *ComponentStatus `json:"minio"`
}

func GetSystemClusterComponentsStatus(s *mcclient.ClientSession, id string) (*ClusterComponentsStatus, error) {
	ret, err := k8smod.KubeClusters.GetSpecific(s, id, "components-status", nil)
	if err != nil {
		return nil, errors.Wrapf(err, "Get cluster %s components status", id)
	}
	out := new(ClusterComponentsStatus)
	if err := ret.Unmarshal(out); err != nil {
		return nil, err
	}
	return out, nil
}

func isMinioEnabled(s *mcclient.ClientSession, id string) (bool, error) {
	status, err := GetSystemClusterComponentsStatus(s, id)
	if err != nil {
		return false, errors.Wrap(err, "Get cluster components status")
	}
	if strings.HasSuffix(status.Minio.Status, "_fail") {
		// disable it here
		if err := disableMinio(s, id); err != nil {
			log.Errorf("Try disable minio when status is %s: %s", status.Minio.Status, err)
		}
		return false, errors.Errorf("Minio deploy status: %s", status.Monitor.Status)
	}
	return status.Minio.Enabled, nil
}

func disableMinio(s *mcclient.ClientSession, systemClusterId string) error {
	params := map[string]string{
		"type": "minio",
	}
	if _, err := k8smod.KubeClusters.PerformAction(s, systemClusterId, "disable-component", jsonutils.Marshal(params)); err != nil {
		return errors.Wrap(err, "disable minio")
	}
	return nil
}

func enableMinio(s *mcclient.ClientSession, systemClusterId string) error {
	opt := &k8s.ClusterEnableComponentMinioOpt{
		ClusterEnableComponentMinioBaseOpt: k8s.ClusterEnableComponentMinioBaseOpt{
			ClusterComponentOptions: k8s.ClusterComponentOptions{
				IdentOptions: k8s.IdentOptions{
					ID: systemClusterId,
				},
			},
			ClusterComponentMinioSetting: k8s.ClusterComponentMinioSetting{
				Mode:          "distributed",
				Replicas:      4,
				DrivesPerNode: 1,
				AccessKey:     "minioadmin",
				SecretKey:     "yunionminio@admin",
				MountPath:     "/export",
				Storage: k8s.ClusterComponentStorage{
					Enabled:   true,
					SizeMB:    1024 * 1024,
					ClassName: v1alpha1.DefaultStorageClass,
				},
			},
		},
	}
	params, err := opt.Params()
	if err != nil {
		return errors.Wrap(err, "Generate minio component params")
	}
	_, err = k8smod.KubeClusters.PerformAction(s, systemClusterId, "enable-component", params)
	if err != nil {
		return errors.Wrapf(err, "Enable minio cluster component of system-cluster %s", systemClusterId)
	}
	return nil
}
