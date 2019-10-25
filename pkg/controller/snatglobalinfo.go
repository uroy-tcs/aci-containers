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
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	uuid "github.com/google/uuid"
	nodeinfo "github.com/noironetworks/aci-containers/pkg/nodeinfo/apis/aci.snat/v1"
	nodeinfoclset "github.com/noironetworks/aci-containers/pkg/nodeinfo/clientset/versioned"
	snatglobalinfo "github.com/noironetworks/aci-containers/pkg/snatglobalinfo/apis/aci.snat/v1"
	"github.com/noironetworks/aci-containers/pkg/util"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/tools/cache"
	"net"
	"reflect"
)

type set map[string]bool
type ContSnatNodeInfo struct {
	nodeInfo nodeinfo.NodeInfoSpec
}

type ContSnatGlobalInfo struct {
	SnatIp        string
	MacAddress    string
	SnatPortRange snatglobalinfo.PortRange
	SnatIpUid     string
}

func (cont *AciController) initSnatNodeInformerFromClient(
	snatClient *nodeinfoclset.Clientset) {
	cont.initSnatNodeInformerBase(
		cache.NewListWatchFromClient(
			snatClient.AciV1().RESTClient(), "nodeinfo",
			metav1.NamespaceAll, fields.Everything()))
}

func (cont *AciController) initSnatNodeInformerBase(listWatch *cache.ListWatch) {
	cont.snatNodeInfoIndexer, cont.snatNodeInformer = cache.NewIndexerInformer(
		listWatch,
		&nodeinfo.NodeInfo{}, 0,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				cont.snatNodeInfoAdded(obj)
			},
			UpdateFunc: func(oldiobj interface{}, newobj interface{}) {
				cont.snatNodeInfoUpdated(oldiobj, newobj)
			},
			DeleteFunc: func(obj interface{}) {
				cont.snatNodeInfoDeleted(obj)
			},
		},
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
	)
	cont.log.Debug("Initializing Node Informers: ")
}

func (cont *AciController) snatNodeInfoAdded(obj interface{}) {
	nodeinfo := obj.(*nodeinfo.NodeInfo)
	nodeinfokey, err := cache.MetaNamespaceKeyFunc(nodeinfo)
	if err != nil {
		return
	}
	cont.log.Debug("Node Info Added: ", nodeinfokey)
	var info ContSnatNodeInfo
	info.nodeInfo = nodeinfo.Spec
	cont.snatNodeInfoCache[nodeinfo.ObjectMeta.Name] = &info
	cont.queueNodeInfoUpdateByKey(nodeinfokey)
}

func (cont *AciController) queueNodeInfoUpdateByKey(key string) {
	cont.log.Debug("Node key Queued: ", key)
	cont.snatNodeInfoQueue.Add(key)
}

func (cont *AciController) snatNodeInfoUpdated(oldobj interface{}, newobj interface{}) {
	oldnodeinfo := oldobj.(*nodeinfo.NodeInfo)
	newnodeinfo := newobj.(*nodeinfo.NodeInfo)
	if reflect.DeepEqual(oldnodeinfo.Spec, newnodeinfo.Spec) {
		return
	}
	nodeinfokey, err := cache.MetaNamespaceKeyFunc(newnodeinfo)
	if err != nil {
		return
	}
	if reflect.DeepEqual(oldnodeinfo.Spec, newnodeinfo.Spec) {
		return
	}
	cont.snatNodeInfoCache[newnodeinfo.ObjectMeta.Name].nodeInfo = newnodeinfo.Spec
	cont.queueNodeInfoUpdateByKey(nodeinfokey)
}

func (cont *AciController) snatNodeInfoDeleted(obj interface{}) {
	nodeinfo := obj.(*nodeinfo.NodeInfo)
	nodeinfokey, err := cache.MetaNamespaceKeyFunc(nodeinfo)
	if err != nil {
		return
	}
	delete(cont.snatNodeInfoCache, nodeinfo.ObjectMeta.Name)
	cont.queueNodeInfoUpdateByKey(nodeinfokey)
}

func (cont *AciController) handleSnatNodeInfo(nodeinfo *nodeinfo.NodeInfo) bool {
	nodename := nodeinfo.ObjectMeta.Name
	nodeinfocache, ok := cont.snatNodeInfoCache[nodename]
	env := cont.env.(*K8sEnvironment)
	globalcl := env.snatGlobalClient
	cont.log.Debug("handle Node Info: ", nodeinfo)
	updated := false
	// Cache needs to be updated
	globalInfos := []snatglobalinfo.GlobalInfo{}
	if !ok {
		snatglobalInfo, err := util.GetGlobalInfoCR(*globalcl)
		cont.deleteNodeinfoFromGlInfoCache(nodename, snatglobalInfo.Spec.GlobalInfos[nodename])
		delete(snatglobalInfo.Spec.GlobalInfos, nodename)
		err = util.UpdateGlobalInfoCR(*globalcl, snatglobalInfo)
		if err != nil {
			return true
		}
	} else {
		for name, _ := range nodeinfo.Spec.SnatPolicyNames {
			createglinfo := false
			if len(cont.snatGlobalInfoCache) == 0 {
				createglinfo = true
			}
			snatIp, portrange, updated := cont.getIpAndPortRange(nodename, name)
			if !updated {
				continue
			}
			cont.log.Debug("SnatIP and Port range: ", snatIp, portrange)
			cont.UpdateGlobaInfoCache(snatIp, nodename, portrange)
			portlist := []snatglobalinfo.PortRange{}
			portlist = append(portlist, portrange)
			ip := net.ParseIP(snatIp)
			snatIpUuid, _ := uuid.FromBytes(ip)
			temp := snatglobalinfo.GlobalInfo{
				MacAddress:     nodeinfo.Spec.Macaddress,
				PortRanges:     portlist,
				SnatIp:         snatIp,
				SnatIpUid:      snatIpUuid.String(),
				SnatPolicyName: name,
			}
			if createglinfo {
				globalInfos = append(globalInfos, temp)
				tempMap := make(map[string][]snatglobalinfo.GlobalInfo)
				tempMap[nodename] = globalInfos
				spec := snatglobalinfo.SnatGlobalInfoSpec{
					GlobalInfos: tempMap,
				}
				if globalcl == nil {
					return false
				}
				err := util.CreateSnatGlobalInfoCR(*globalcl, spec)
				if err != nil {
					return true
				}
				return false
			} else {
				globalInfos = append(globalInfos, temp)
				updated = true
			}
		}
		if updated {
			if globalcl == nil {
				return false
			}
			snatglobalInfo, err := util.GetGlobalInfoCR(*globalcl)
			if err != nil {
				return true
			}
			if snatglobalInfo.Spec.GlobalInfos == nil {
				tempMap := make(map[string][]snatglobalinfo.GlobalInfo)
				tempMap[nodename] = globalInfos
				snatglobalInfo.Spec.GlobalInfos = tempMap
			} else {
				snatglobalInfo.Spec.GlobalInfos[nodename] = globalInfos
			}
			err = util.UpdateGlobalInfoCR(*globalcl, snatglobalInfo)
			if err != nil {
				return true
			}
		}
	}
	cont.log.Debug("nodeinfocache", nodeinfocache)
	return false
}

func (cont *AciController) UpdateGlobaInfoCache(snatip string, nodename string, portrange snatglobalinfo.PortRange) {
	cont.indexMutex.Lock()
	if _, ok := cont.snatGlobalInfoCache[snatip]; !ok {
		cont.snatGlobalInfoCache[snatip] = make(map[string]*ContSnatGlobalInfo)
	}
	var glinfo ContSnatGlobalInfo
	glinfo.SnatIp = snatip
	glinfo.SnatPortRange = portrange
	cont.snatGlobalInfoCache[snatip][nodename] = &glinfo
	cont.indexMutex.Unlock()
}

func (cont *AciController) getIpAndPortRange(nodename string, snatpolicyname string) (string,
	snatglobalinfo.PortRange, bool) {
	snatpolicy, ok := cont.snatPolicyCache[snatpolicyname]
	if !ok {
		return "", snatglobalinfo.PortRange{}, false
	}
	if len(snatpolicy.SnatIp) != 0 {
		snatIps := cont.snatPolicyCache[snatpolicyname].ExpandedSnatIps
		expandedsnatports := cont.snatPolicyCache[snatpolicyname].ExpandedSnatPorts
		if len(snatIps) == 0 {
			return "", snatglobalinfo.PortRange{}, false
		}
		for _, snatip := range snatIps {
			if _, ok := cont.snatGlobalInfoCache[snatip]; !ok {
				return snatip, expandedsnatports[0], true
			} else if len(cont.snatGlobalInfoCache[snatip]) < len(expandedsnatports) {
				globalInfo, _ := cont.snatGlobalInfoCache[snatip]
				if _, ok := globalInfo[nodename]; !ok {
					m := make(map[int]int)
					for _, val := range globalInfo {
						m[val.SnatPortRange.Start] = val.SnatPortRange.End
					}
					for i, Val2 := range expandedsnatports {
						if _, ok := m[Val2.Start]; !ok {
							return snatip, expandedsnatports[i], true
						}
					}
				} else {
					return globalInfo[nodename].SnatIp, globalInfo[nodename].SnatPortRange, true
				}
			}
		}
	}
	return "", snatglobalinfo.PortRange{}, false
}

func (cont *AciController) deleteNodeinfoFromGlInfoCache(nodename string, globalinfos []snatglobalinfo.GlobalInfo) {
	cont.indexMutex.Lock()
	for _, glinfo := range globalinfos {
		delete(cont.snatGlobalInfoCache[glinfo.SnatIp], nodename)
		if len(cont.snatGlobalInfoCache[glinfo.SnatIp]) == 0 {
			delete(cont.snatGlobalInfoCache, glinfo.SnatIp)
		}
	}
	cont.indexMutex.Unlock()
}
