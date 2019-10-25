// Copyright 2019 Cisco Systems, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRATIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Handlers for snat updates.

package hostagent

import (
	"encoding/json"
	"fmt"
	"github.com/Sirupsen/logrus"
	snatglobal "github.com/noironetworks/aci-containers/pkg/snatglobalinfo/apis/aci.snat/v1"
	snatglobalclset "github.com/noironetworks/aci-containers/pkg/snatglobalinfo/clientset/versioned"
	snatlocal "github.com/noironetworks/aci-containers/pkg/snatlocalinfo/apis/aci.snat/v1"
	snatpolicy "github.com/noironetworks/aci-containers/pkg/snatpolicy/apis/aci.snat/v1"
	snatpolicyclset "github.com/noironetworks/aci-containers/pkg/snatpolicy/clientset/versioned"
	"github.com/noironetworks/aci-containers/pkg/util"
	"io/ioutil"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/pkg/controller"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Filename used to create external service file on host
// example snat-external.service
const SnatService = "snat-external"

type ResourceType int

const (
	POD ResourceType = 1 << iota
	SERVICE
	DEPLOYMENT
	NAMESPACE
	CLUSTER
)

type OpflexPortRange struct {
	Start int `json:"start,omitempty"`
	End   int `json:"end,omitempty"`
}

// This structure is to write the  SnatFile
type OpflexSnatIp struct {
	Uuid          string                   `json:"uuid"`
	InterfaceName string                   `json:"interface-name,omitempty"`
	SnatIp        string                   `json:"snat-ip,omitempty"`
	InterfaceMac  string                   `json:"interface-mac,omitempty"`
	Local         bool                     `json:"local,omitempty"`
	DestIpAddress string                   `json:"destip-dddress,omitempty"`
	DestPrefix    uint16                   `json:"dest-prefix,omitempty"`
	PortRange     []OpflexPortRange        `json:"port-range,omitempty"`
	InterfaceVlan uint                     `json:"interface-vlan,omitempty"`
	Zone          uint                     `json:"zone,omitempty"`
	Remote        []OpflexSnatIpRemoteInfo `json:"remote,omitempty"`
}

type OpflexSnatIpRemoteInfo struct {
	NodeIp     string            `json:"snat_ip,omitempty"`
	MacAddress string            `json:"mac,omitempty"`
	PortRange  []OpflexPortRange `json:"port-range,omitempty"`
	Refcount   int               `json:"ref,omitempty"`
}
type OpflexSnatGlobalInfo struct {
	SnatIp         string
	MacAddress     string
	PortRange      []OpflexPortRange
	SnatIpUid      string
	SnatPolicyName string
}

type OpflexSnatLocalInfo struct {
	Snatpolicies map[ResourceType][]string //Each resource can represent multiple entries
	PlcyUuids    []string
	MarkDelete   bool
}

func (agent *HostAgent) initSnatGlobalInformerFromClient(
	snatClient *snatglobalclset.Clientset) {
	agent.initSnatGlobalInformerBase(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return snatClient.AciV1().SnatGlobalInfos(metav1.NamespaceAll).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return snatClient.AciV1().SnatGlobalInfos(metav1.NamespaceAll).Watch(options)
			},
		})
}

func (agent *HostAgent) initSnatPolicyInformerFromClient(
	snatClient *snatpolicyclset.Clientset) {
	agent.initSnatPolicyInformerBase(
		cache.NewListWatchFromClient(
			snatClient.AciV1().RESTClient(), "snatpolicies",
			metav1.NamespaceAll, fields.Everything()))
}

func getsnat(snatfile string) (string, error) {
	raw, err := ioutil.ReadFile(snatfile)
	if err != nil {
		return "", err
	}
	return string(raw), err
}

func writeSnat(snatfile string, snat *OpflexSnatIp) (bool, error) {
	newdata, err := json.MarshalIndent(snat, "", "  ")
	if err != nil {
		return true, err
	}
	existingdata, err := ioutil.ReadFile(snatfile)
	if err == nil && reflect.DeepEqual(existingdata, newdata) {
		return false, nil
	}

	err = ioutil.WriteFile(snatfile, newdata, 0644)
	return true, err
}

func (agent *HostAgent) FormSnatFilePath(uuid string) string {
	return filepath.Join(agent.config.OpFlexSnatDir, uuid+".snat")
}

func SnatLocalInfoLogger(log *logrus.Logger, snat *snatlocal.SnatLocalInfo) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"namespace": snat.ObjectMeta.Namespace,
		"name":      snat.ObjectMeta.Name,
		"spec":      snat.Spec,
	})
}

func SnatGlobalInfoLogger(log *logrus.Logger, snat *snatglobal.SnatGlobalInfo) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"namespace": snat.ObjectMeta.Namespace,
		"name":      snat.ObjectMeta.Name,
		"spec":      snat.Spec,
	})
}

func opflexSnatIpLogger(log *logrus.Logger, snatip *OpflexSnatIp) *logrus.Entry {
	return log.WithFields(logrus.Fields{
		"uuid":           snatip.Uuid,
		"snat_ip":        snatip.SnatIp,
		"mac_address":    snatip.InterfaceMac,
		"port_range":     snatip.PortRange,
		"local":          snatip.Local,
		"interface-name": snatip.InterfaceName,
		"interfcae-vlan": snatip.InterfaceVlan,
		"remote":         snatip.Remote,
	})
}

func (agent *HostAgent) initSnatGlobalInformerBase(listWatch *cache.ListWatch) {
	agent.snatGlobalInformer = cache.NewSharedIndexInformer(
		listWatch,
		&snatglobal.SnatGlobalInfo{},
		controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	agent.snatGlobalInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			agent.snatGlobalInfoUpdate(obj)
		},
		UpdateFunc: func(_ interface{}, obj interface{}) {
			agent.snatGlobalInfoUpdate(obj)
		},
		DeleteFunc: func(obj interface{}) {
			agent.snatGlobalInfoDelete(obj)
		},
	})
	agent.log.Info("Initializing SnatGlobal Info Informers")
}

func (agent *HostAgent) initSnatPolicyInformerBase(listWatch *cache.ListWatch) {
	agent.snatPolicyInformer = cache.NewSharedIndexInformer(
		listWatch,
		&snatpolicy.SnatPolicy{}, controller.NoResyncPeriodFunc(),
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	agent.snatPolicyInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			agent.snatPolicyAdded(obj)
		},
		UpdateFunc: func(oldobj interface{}, newobj interface{}) {
			agent.snatPolicyUpdated(oldobj, newobj)
		},
		DeleteFunc: func(obj interface{}) {
			agent.snatPolicyDeleted(obj)
		},
	})
	agent.log.Info("Initializing Snat Policy Informers")
}

func (agent *HostAgent) snatPolicyAdded(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	agent.log.Info("Policy Info Added: ")
	policyinfo := obj.(*snatpolicy.SnatPolicy)
	policyinfokey, err := cache.MetaNamespaceKeyFunc(policyinfo)
	if err != nil {
		return
	}
	agent.log.Info("Policy Info Added: ", policyinfokey)
	agent.snatPolicyCache[policyinfo.ObjectMeta.Name] = policyinfo
	agent.handleSnatUpdate(policyinfo)
}

func (agent *HostAgent) snatPolicyUpdated(oldobj interface{}, newobj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	oldpolicyinfo := oldobj.(*snatpolicy.SnatPolicy)
	newpolicyinfo := newobj.(*snatpolicy.SnatPolicy)
	policyinfokey, err := cache.MetaNamespaceKeyFunc(newpolicyinfo)
	if err != nil {
		return
	}
	if reflect.DeepEqual(oldpolicyinfo, newpolicyinfo) {
		return
	}
	agent.snatPolicyCache[newpolicyinfo.ObjectMeta.Name] = newpolicyinfo
	agent.log.Debug("Policy Info Updated: ", policyinfokey)
	agent.handleSnatUpdate(newpolicyinfo)
}

func (agent *HostAgent) snatPolicyDeleted(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	policyinfo := obj.(*snatpolicy.SnatPolicy)
	policyinfokey, err := cache.MetaNamespaceKeyFunc(policyinfo)
	if err != nil {
		return
	}
	agent.log.Debug("Policy Info Deleted: ", policyinfokey)
	delete(agent.snatPolicyCache, policyinfo.ObjectMeta.Name)
	agent.handleSnatUpdate(policyinfo)
}

func (agent *HostAgent) handleSnatUpdate(policy *snatpolicy.SnatPolicy) {
	// First Parse the policy and check for applicability
	// list all the Pods based on labels and namespace
	agent.log.Info("Handle snatUpdate: ", policy)
	_, err := cache.MetaNamespaceKeyFunc(policy)
	if err != nil {
		return
	}
	if _, ok := agent.snatPolicyCache[policy.ObjectMeta.Name]; !ok {
		agent.deletePolicy(policy)
		return
	}
	// 1.List the targets matching the policy based on policy config
	// 2. find the pods policy is applicable update the pod's policy priority list
	// 3. check the policy is active then Update the NodeInfo with policy.
	uids := make(map[ResourceType][]string)
	if len(policy.Spec.SnatIp) == 0 {
		//handle policy for service pods
		var services []*v1.Service
		var poduids []string
		selector := labels.SelectorFromSet(labels.Set(policy.Spec.Selector.Labels))
		cache.ListAll(agent.serviceInformer.GetIndexer(), selector,
			func(servobj interface{}) {
				services = append(services, servobj.(*v1.Service))
			})
		// list the pods and apply the policy at service target
		for _, service := range services {
			uids, _ := agent.getPodsMatchingObjet(service, policy.Spec.Selector.Namespace, policy.ObjectMeta.Name)
			poduids = append(poduids, uids...)
		}
		uids[SERVICE] = poduids
	} else if reflect.DeepEqual(policy.Spec.Selector, snatpolicy.PodSelector{}) {
		// This Policy will be applied at cluster level
		var poduids []string
		// handle policy for cluster
		for k, _ := range agent.opflexEps {
			poduids = append(poduids, k)
		}
		uids[CLUSTER] = poduids
	} else if len(policy.Spec.Selector.Labels) == 0 {
		// This is namespace based policy
		var poduids []string
		cache.ListAllByNamespace(agent.podInformer.GetIndexer(), policy.Spec.Selector.Namespace, labels.Everything(),
			func(podobj interface{}) {
				pod := podobj.(*v1.Pod)
				if pod.Spec.NodeName == agent.config.NodeName {
					poduids = append(poduids, string(pod.ObjectMeta.UID))
				}
			})
		uids[NAMESPACE] = poduids
	} else {
		poduids, deppoduids, nspoduids :=
			agent.getPodUidsMatchingLabel(policy.Spec.Selector.Namespace, policy.Spec.Selector.Labels, policy.ObjectMeta.Name)
		uids[POD] = poduids
		uids[DEPLOYMENT] = deppoduids
		uids[NAMESPACE] = nspoduids
	}
	for res, poduids := range uids {
		agent.applyPolicy(poduids, res, policy.GetName())

	}
}

func (agent *HostAgent) getPodUidsMatchingLabel(namespace string, label map[string]string, policyname string) (poduids []string,
	deppoduids []string, nspoduids []string) {
	// Get all pods matching the label
	// Get all deployments matching the label
	// Get all the namespaces matching the policy label
	selector := labels.SelectorFromSet(labels.Set(label))
	cache.ListAll(agent.podInformer.GetIndexer(), selector,
		func(podobj interface{}) {
			pod := podobj.(*v1.Pod)
			if pod.Spec.NodeName == agent.config.NodeName {
				poduids = append(poduids, string(pod.ObjectMeta.UID))
			}
		})
	cache.ListAll(agent.depInformer.GetIndexer(), selector,
		func(depobj interface{}) {
			dep := depobj.(*appsv1.Deployment)
			uids, _ := agent.getPodsMatchingObjet(dep, namespace, policyname)
			deppoduids = append(deppoduids, uids...)
		})
	cache.ListAll(agent.nsInformer.GetIndexer(), selector,
		func(nsobj interface{}) {
			ns := nsobj.(*v1.Namespace)
			uids, _ := agent.getPodsMatchingObjet(ns, namespace, policyname)
			nspoduids = append(nspoduids, uids...)
		})
	return
}

func (agent *HostAgent) applyPolicy(poduids []string, res ResourceType, snatPolicyName string) {
	nodeUpdate := false
	if _, ok := agent.snatPods[snatPolicyName]; !ok {
		agent.snatPods[snatPolicyName] = make(map[string]ResourceType)
		nodeUpdate = true
	}
	for _, uid := range poduids {
		_, ok := agent.opflexSnatLocalInfos[uid]
		if !ok {
			var localinfo OpflexSnatLocalInfo
			localinfo.Snatpolicies = make(map[ResourceType][]string)
			localinfo.Snatpolicies[res] = append(localinfo.Snatpolicies[res], snatPolicyName)
			agent.opflexSnatLocalInfos[uid] = &localinfo
			agent.snatPods[snatPolicyName][uid] |= res

		} else {
			agent.opflexSnatLocalInfos[uid].Snatpolicies[res] =
				append(agent.opflexSnatLocalInfos[uid].Snatpolicies[res], snatPolicyName)
			agent.snatPods[snatPolicyName][uid] |= res
			// trigger update  the epfile
		}
	}
	agent.log.Info("applypolicy: ", agent.snatPods[snatPolicyName])
	if nodeUpdate == true {
		agent.scheduleSyncNodeInfo()
	} else {
		agent.updateEpFiles(poduids)
	}
	return
}

func (agent *HostAgent) syncSnatNodeInfo() bool {
	snatPolicyNames := make(map[string]bool)
	for key, _ := range agent.snatPods {
		snatPolicyNames[key] = true
	}
	env := agent.env.(*K8sEnvironment)
	if env == nil {
		return false
	}
	// send nodeupdate as the policy is active
	if agent.InformNodeInfo(env.nodeInfo, snatPolicyNames) == false {
		return true
	}
	return false
}

func (agent *HostAgent) deletePolicy(policy *snatpolicy.SnatPolicy) {
	pods, ok := agent.snatPods[policy.GetName()]
	var poduids []string
	if !ok {
		return
	}
	for uuid, res := range pods {
		agent.deleteSnatLocalInfo(uuid, res, policy.GetName())
		poduids = append(poduids, uuid)
	}
	agent.updateEpFiles(poduids)
	return
}

func (agent *HostAgent) deleteSnatLocalInfo(poduid string, res ResourceType, plcyname string) {
	localinfo, ok := agent.opflexSnatLocalInfos[poduid]
	if ok {
		i := uint(0)
		j := uint(0)
		for i < uint(res) {
			i = 1 << j
			j = j + 1
			if i&uint(res) == i {
				policeis := localinfo.Snatpolicies[ResourceType(i)]
				for k, name := range policeis {
					if name == plcyname {
						localinfo.Snatpolicies[ResourceType(i)] = append(localinfo.Snatpolicies[ResourceType(i)][:k],
							localinfo.Snatpolicies[ResourceType(i)][k+1:]...)
					}
				}
				if len(localinfo.Snatpolicies[res]) == 0 {
					delete(localinfo.Snatpolicies, res)
					agent.snatPods[plcyname][poduid] &= ^(res) // clear the bit
				}
			}
		}
		// remove policy stack.
	}
}

func (agent *HostAgent) snatGlobalInfoUpdate(obj interface{}) {
	agent.indexMutex.Lock()
	defer agent.indexMutex.Unlock()
	snat := obj.(*snatglobal.SnatGlobalInfo)
	key, err := cache.MetaNamespaceKeyFunc(snat)
	if err != nil {
		SnatGlobalInfoLogger(agent.log, snat).
			Error("Could not create key:" + err.Error())
		return
	}
	agent.log.Info("Snat Global Object added/Updated ", snat)
	agent.doUpdateSnatGlobalInfo(key)
}

func (agent *HostAgent) doUpdateSnatGlobalInfo(key string) {
	snatobj, exists, err :=
		agent.snatGlobalInformer.GetStore().GetByKey(key)
	if err != nil {
		agent.log.Error("Could not lookup snat for " +
			key + ": " + err.Error())
		return
	}
	if !exists || snatobj == nil {
		return
	}
	snat := snatobj.(*snatglobal.SnatGlobalInfo)
	logger := SnatGlobalInfoLogger(agent.log, snat)
	agent.snaGlobalInfoChanged(snatobj, logger)
}

func fileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

func (agent *HostAgent) snaGlobalInfoChanged(snatobj interface{}, logger *logrus.Entry) {
	snat := snatobj.(*snatglobal.SnatGlobalInfo)
	syncSnat := false
	updateLocalInfo := false
	if logger == nil {
		logger = agent.log.WithFields(logrus.Fields{})
	}
	logger.Debug("Snat Global info Changed...")
	globalInfo := snat.Spec.GlobalInfos
	// This case is possible when all the pods will be deleted from that node
	if len(globalInfo) < len(agent.opflexSnatGlobalInfos) {
		for nodename, _ := range agent.opflexSnatGlobalInfos {
			if _, ok := globalInfo[nodename]; !ok {
				delete(agent.opflexSnatGlobalInfos, nodename)
				syncSnat = true
			}
		}
	}
	for nodename, val := range globalInfo {
		var newglobalinfos []*OpflexSnatGlobalInfo
		for _, v := range val {
			portrange := make([]OpflexPortRange, 1)
			portrange[0].Start = v.PortRanges[0].Start
			portrange[0].End = v.PortRanges[0].End
			nodeInfo := &OpflexSnatGlobalInfo{
				SnatIp:         v.SnatIp,
				MacAddress:     v.MacAddress,
				PortRange:      portrange,
				SnatIpUid:      v.SnatIpUid,
				SnatPolicyName: v.SnatPolicyName,
			}
			newglobalinfos = append(newglobalinfos, nodeInfo)
		}
		existing, ok := agent.opflexSnatGlobalInfos[nodename]
		if (ok && !reflect.DeepEqual(existing, newglobalinfos)) || !ok {
			agent.opflexSnatGlobalInfos[nodename] = newglobalinfos
			syncSnat = true
			if nodename == agent.config.NodeName {
				updateLocalInfo = true
			}
		}
	}

	snatFileName := SnatService + ".service"
	filePath := filepath.Join(agent.config.OpFlexServiceDir, snatFileName)
	file_exists := fileExists(filePath)
	if len(agent.opflexSnatGlobalInfos) > 0 {
		// if more than one global infos, create snat ext file
		as := &opflexService{
			Uuid:              SnatService,
			DomainPolicySpace: agent.config.AciVrfTenant,
			DomainName:        agent.config.AciVrf,
			ServiceMode:       "loadbalancer",
			ServiceMappings:   make([]opflexServiceMapping, 0),
			InterfaceName:     agent.config.UplinkIface,
			InterfaceVlan:     uint16(agent.config.ServiceVlan),
			ServiceMac:        agent.serviceEp.Mac,
			InterfaceIp:       agent.serviceEp.Ipv4.String(),
		}
		sm := &opflexServiceMapping{
			Conntrack: true,
		}
		as.ServiceMappings = append(as.ServiceMappings, *sm)
		agent.opflexServices[SnatService] = as
		if !file_exists {
			wrote, err := writeAs(filePath, as)
			if err != nil {
				agent.log.Debug("Unable to write snat ext service file")
			} else if wrote {
				agent.log.Debug("Created snat ext service file")
			}

		}
	} else {
		delete(agent.opflexServices, SnatService)
		// delete snat service file if no global infos exist
		if file_exists {
			err := os.Remove(filePath)
			if err != nil {
				agent.log.Debug("Unable to delete snat ext service file")
			} else {
				agent.log.Debug("Deleted snat ext service file")
			}
		}
	}
	if syncSnat {
		agent.scheduleSyncSnats()
	}
	if updateLocalInfo {
		var poduids []string
		for _, v := range agent.opflexSnatGlobalInfos[agent.config.NodeName] {
			for uuid, _ := range agent.snatPods[v.SnatPolicyName] {
				poduids = append(poduids, uuid)
			}
		}
		agent.updateEpFiles(poduids)
	}
}

func (agent *HostAgent) snatGlobalInfoDelete(obj interface{}) {
	agent.log.Debug("Snat Global Info Obj Delete")
	snat := obj.(*snatglobal.SnatGlobalInfo)
	globalInfo := snat.Spec.GlobalInfos
	for nodename, _ := range globalInfo {
		if _, ok := agent.opflexSnatGlobalInfos[nodename]; ok {
			delete(agent.opflexSnatGlobalInfos, nodename)
		}
	}
}

func (agent *HostAgent) syncSnat() bool {
	if !agent.syncEnabled {
		return false
	}
	agent.log.Debug("Syncing snats")
	agent.indexMutex.Lock()
	opflexSnatIps := make(map[string]*OpflexSnatIp)
	remoteinfo := make(map[string][]OpflexSnatIpRemoteInfo)
	// set the remote info for every snatIp
	for nodename, v := range agent.opflexSnatGlobalInfos {
		for _, ginfo := range v {
			if nodename != agent.config.NodeName {
				var remote OpflexSnatIpRemoteInfo
				remote.MacAddress = ginfo.MacAddress
				remote.PortRange = ginfo.PortRange
				remoteinfo[ginfo.SnatIp] = append(remoteinfo[ginfo.SnatIp], remote)
			}
		}
	}
	agent.log.Debug("Remte: ", remoteinfo)
	// set the Opflex Snat IP information
	localportrange := make(map[string][]OpflexPortRange)
	ginfos, ok := agent.opflexSnatGlobalInfos[agent.config.NodeName]

	if ok {
		for _, ginfo := range ginfos {
			localportrange[ginfo.SnatIp] = ginfo.PortRange
		}
	}

	for _, v := range agent.opflexSnatGlobalInfos {
		for _, ginfo := range v {
			var snatinfo OpflexSnatIp
			// set the local portrange
			snatinfo.InterfaceName = agent.config.UplinkIface
			snatinfo.InterfaceVlan = agent.config.ServiceVlan
			snatinfo.Local = false
			if _, ok := localportrange[ginfo.SnatIp]; ok {
				snatinfo.PortRange = localportrange[ginfo.SnatIp]
				snatinfo.Local = true
			}
			snatinfo.SnatIp = ginfo.SnatIp
			snatinfo.Uuid = ginfo.SnatIpUid
			snatinfo.Zone = agent.config.Zone
			snatinfo.Remote = remoteinfo[ginfo.SnatIp]
			opflexSnatIps[ginfo.SnatIp] = &snatinfo
			agent.log.Debug("Opflex Snat data IP: ", opflexSnatIps[ginfo.SnatIp])
		}
	}
	agent.indexMutex.Unlock()
	files, err := ioutil.ReadDir(agent.config.OpFlexSnatDir)
	if err != nil {
		agent.log.WithFields(
			logrus.Fields{"SnatDir": agent.config.OpFlexSnatDir},
		).Error("Could not read directory " + err.Error())
		return true
	}
	seen := make(map[string]bool)
	for _, f := range files {
		uuid := f.Name()
		if strings.HasSuffix(uuid, ".snat") {
			uuid = uuid[:len(uuid)-5]
		} else {
			continue
		}

		snatfile := filepath.Join(agent.config.OpFlexSnatDir, f.Name())
		logger := agent.log.WithFields(
			logrus.Fields{"Uuid": uuid})
		existing, ok := opflexSnatIps[uuid]
		if ok {
			fmt.Printf("snatfile:%s\n", snatfile)
			wrote, err := writeSnat(snatfile, existing)
			if err != nil {
				opflexSnatIpLogger(agent.log, existing).Error("Error writing snat file: ", err)
			} else if wrote {
				opflexSnatIpLogger(agent.log, existing).Info("Updated snat")
			}
			seen[uuid] = true
		} else {
			logger.Info("Removing snat")
			os.Remove(snatfile)
		}
	}
	for _, snat := range opflexSnatIps {
		if seen[snat.Uuid] {
			continue
		}
		opflexSnatIpLogger(agent.log, snat).Info("Adding Snat")
		snatfile :=
			agent.FormSnatFilePath(snat.Uuid)
		_, err = writeSnat(snatfile, snat)
		if err != nil {
			opflexSnatIpLogger(agent.log, snat).
				Error("Error writing snat file: ", err)
		}
	}
	agent.log.Debug("Finished snat sync")
	return false
}

func (agent *HostAgent) getPodsMatchingObjet(obj interface{}, namespace string, policyname string) (poduids []string, res ResourceType) {
	switch obj.(type) {
	case *v1.Pod:
		pod, _ := obj.(*v1.Pod)
		if agent.isPolicyNameSpaceMatches(namespace, pod.ObjectMeta.Namespace) {
			poduids = append(poduids, string(pod.ObjectMeta.UID))
		}
		res = POD
	case *appsv1.Deployment:
		deployment, _ := obj.(*appsv1.Deployment)
		depkey, _ :=
			cache.MetaNamespaceKeyFunc(deployment)
		if agent.isPolicyNameSpaceMatches(policyname, deployment.ObjectMeta.Namespace) {
			for _, podkey := range agent.depPods.GetPodForObj(depkey) {
				podobj, exists, _ := agent.podInformer.GetStore().GetByKey(podkey)
				if exists && podobj != nil && podobj.(*v1.Pod).Spec.NodeName == agent.config.NodeName {
					poduids = append(poduids, string(podobj.(*v1.Pod).ObjectMeta.UID))
				}
			}
		}
		res = DEPLOYMENT
	case *v1.Service, *v1.Endpoints:
		key, _ :=
			cache.MetaNamespaceKeyFunc(obj)
		asobj, exists, err := agent.serviceInformer.GetStore().GetByKey(key)
		if err != nil {
			agent.log.Error("Could not lookup service for " +
				key + ": " + err.Error())
			return
		}
		if !exists || asobj == nil {
			return
		}
		service, _ := obj.(*v1.Service)
		selector := labels.SelectorFromSet(labels.Set(service.Spec.Selector))
		if agent.isPolicyNameSpaceMatches(policyname, service.ObjectMeta.Namespace) {
			cache.ListAllByNamespace(agent.podInformer.GetIndexer(), service.ObjectMeta.Namespace, selector,
				func(podobj interface{}) {
					pod := podobj.(*v1.Pod)
					if pod.Spec.NodeName == agent.config.NodeName {
						poduids = append(poduids, string(pod.ObjectMeta.UID))
					}
				})
		}
		res = SERVICE
	case *v1.Namespace:
		ns, _ := obj.(*v1.Namespace)
		if agent.isPolicyNameSpaceMatches(policyname, ns.ObjectMeta.Name) {
			cache.ListAllByNamespace(agent.podInformer.GetIndexer(), namespace, labels.Everything(),
				func(podobj interface{}) {
					pod := podobj.(*v1.Pod)
					if pod.Spec.NodeName == agent.config.NodeName {
						poduids = append(poduids, string(pod.ObjectMeta.UID))
					}
				})
		}
		res = NAMESPACE
	default:
	}
	return
}

func (agent *HostAgent) updateEpFiles(poduids []string) {
	for _, uid := range poduids {
		localinfo, ok := agent.opflexSnatLocalInfos[uid]
		if !ok {
			continue
		}
		agent.log.Info("local info:####", agent.opflexSnatLocalInfos[uid])
		var i uint = 1
		var pos uint = 1
		var policystack []string
		for ; i <= uint(CLUSTER); i = 1 << pos {
			pos = pos + 1
			visted := make(map[string]bool)
			policies, ok := localinfo.Snatpolicies[ResourceType(i)]
			if ok {
				for _, name := range policies {
					if _, ok := visted[name]; !ok {
						visted[name] = true
					} else {
						continue
					}
				}
				sort.Slice(policies, func(i, j int) bool { return agent.comapare(policies[i], policies[j]) })
			}
			policystack = append(policystack, policies...)
		}
		var uids []string
		for _, name := range policystack {
			for _, val := range agent.opflexSnatGlobalInfos[agent.config.NodeName] {
				if val.SnatPolicyName == name {
					uids = append(uids, val.SnatIpUid)
				}
			}
		}
		if !reflect.DeepEqual(agent.opflexSnatLocalInfos[uid].PlcyUuids, uids) {
			agent.opflexSnatLocalInfos[uid].PlcyUuids = uids
			agent.log.Info("Update EpFile", uids)
			agent.scheduleSyncEps()
		}

	}
}

func (agent *HostAgent) comapare(plcy1, plcy2 string) bool {
	a := agent.snatPolicyCache[plcy1].Spec.DestIp[0] // currently handling one IP needs to be extended to list of IP's
	b := agent.snatPolicyCache[plcy2].Spec.DestIp[0]
	ipA, _, _ := net.ParseCIDR(a)
	_, ipnetB, _ := net.ParseCIDR(b)
	if ipnetB.Contains(ipA) {
		return true
	} else {
		return false
	}
}

func (agent *HostAgent) getMatchingSnatPolicy(label map[string]string, namespace string) (snatPolicyNames map[string]bool) {
	for _, item := range agent.snatPolicyCache {
		if reflect.DeepEqual(item.Spec.Selector, snatpolicy.PodSelector{}) ||
			(len(item.Spec.Selector.Labels) == 0 && item.Spec.Selector.Namespace == namespace) {
			continue
		}
		if util.MatchLabels(item.Spec.Selector.Labels, label) {
			if (item.Spec.Selector.Namespace != "" && item.Spec.Selector.Namespace == namespace) ||
				(item.Spec.Selector.Namespace == "") {
				snatPolicyNames[item.ObjectMeta.Name] = true
			}
		}
	}
	return
}

func (agent *HostAgent) handleObjectUpdate(obj interface{}, label map[string]string, deleted bool) {
	objKey, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		return
	}
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return
	}
	plcynames, ok := agent.snatPolicyLabels[objKey]
	if !ok {
		plcynames := agent.getMatchingSnatPolicy(metadata.GetLabels(), metadata.GetNamespace())
		for name, _ := range plcynames {
			poduids, res := agent.getPodsMatchingObjet(obj, metadata.GetNamespace(), name)
			agent.applyPolicy(poduids, res, name)
			agent.snatPolicyLabels[objKey] = append(agent.snatPolicyLabels[objKey], name)
		}
		//get matching policy apply the policy
		// set the labels if it matches with label or namespace or cluster level

	} else {
		if deleted {
			for _, name := range plcynames {
				poduids, res := agent.getPodsMatchingObjet(obj, metadata.GetNamespace(), name)
				for _, uid := range poduids {
					agent.deleteSnatLocalInfo(uid, res, name)
				}
			}
			delete(agent.snatPolicyLabels, objKey)
		} else {
			matchnames := agent.getMatchingSnatPolicy(metadata.GetLabels(), metadata.GetNamespace())
			for _, name := range plcynames {
				if _, ok := matchnames[name]; !ok {
					poduids, res := agent.getPodsMatchingObjet(obj, metadata.GetNamespace(), name)
					for _, uid := range poduids {
						agent.deleteSnatLocalInfo(uid, res, name)
					}
				} else {
					matchnames[name] = false
				}
			}

			for name, val := range matchnames {
				if val == false {
					poduids, res := agent.getPodsMatchingObjet(obj, metadata.GetNamespace(), name)
					agent.applyPolicy(poduids, res, name)
					agent.snatPolicyLabels[objKey] = append(agent.snatPolicyLabels[objKey], name)
				}
			}
		}
	}
}

func (agent *HostAgent) isPolicyNameSpaceMatches(policyName string, namespace string) bool {
	policy, ok := agent.snatPolicyCache[policyName]
	if ok {
		if len(policy.Spec.Selector.Namespace) == 0 || (len(policy.Spec.Selector.Namespace) > 0 &&
			policy.Spec.Selector.Namespace == namespace) {
			return true
		}
	}
	return false
}
