import collections
import json
import requests

requests.packages.urllib3.disable_warnings()


class Apic(object):
    def __init__(self, addr, username, password, ssl=True, verify=False):
        self.addr = addr
        self.ssl = ssl
        self.username = username
        self.password = password
        self.cookies = None
        self.verify = verify
        self.debug = False
        self.login()

    def url(self, path):
        if self.ssl:
            return 'https://%s%s' % (self.addr, path)
        return 'http://%s%s' % (self.addr, path)

    def get(self, path, data=None):
        args = dict(data=data, cookies=self.cookies, verify=self.verify)
        return requests.get(self.url(path), **args)

    def post(self, path, data):
        args = dict(data=data, cookies=self.cookies, verify=self.verify)
        return requests.post(self.url(path), **args)

    def delete(self, path, data=None):
        args = dict(data=data, cookies=self.cookies, verify=self.verify)
        return requests.delete(self.url(path), **args)

    def login(self):
        data = '{"aaaUser":{"attributes":{"name": "%s", "pwd": "%s"}}}' % \
            (self.username, self.password)
        path = '/api/aaaLogin.json'
        req = requests.post(self.url(path), data=data, verify=False)
        if req.status_code == 200:
            resp = json.loads(req.text)
            token = resp["imdata"][0]["aaaLogin"]["attributes"]["token"]
            self.cookies = {'APIC-Cookie': token}
        return req

    def get_infravlan(self):
        # TODO: Need to find the model to get this
        return 4093

    def provision(self, data):
        for path in data:
            try:
                if data[path] is not None:
                    resp = self.post(path, data[path])
                    if self.debug:
                        print path, resp.text
            except Exception as e:
                # print it, otherwise ignore it
                print "Error in provisioning %s: %s" % (path, str(e))

    def unprovision(self, data):
        for path in data:
            try:
                if path not in ["/api/node/mo/uni/tn-common.json"]:
                    resp = self.delete(path)
                    if self.debug:
                        print path, resp.text
            except Exception as e:
                # print it, otherwise ignore it
                print "Error in un-provisioning %s: %s" % (path, str(e))


class ApicKubeConfig(object):
    def __init__(self, config):
        self.config = config

    def get_config(self):
        def update(data, x):
            if x:
                data[x[0]] = json.dumps(x[1], sort_keys=True)

        data = collections.OrderedDict()
        update(data, self.vlan_pool())
        update(data, self.mcast_pool())
        update(data, self.phys_dom())
        update(data, self.kube_dom())
        update(data, self.kube_tn())
        return data

    def vlan_pool(self):
        pool_name = self.config["aci_config"]["physical_domain"]["vlan_pool"]
        kubeapi_vlan = self.config["net_config"]["kubeapi_vlan"]
        service_vlan = self.config["net_config"]["service_vlan"]

        path = "/api/mo/uni/infra/vlanns-[%s]-static.json" % pool_name
        data = {
            "fvnsVlanInstP": {
                "attributes": {
                    "name": pool_name,
                    "allocMode": "static"
                },
                "children": [
                    {
                        "fvnsEncapBlk": {
                            "attributes": {
                                "allocMode": "static",
                                "from": "vlan-%s" % kubeapi_vlan,
                                "to": "vlan-%s" % kubeapi_vlan
                            }
                        }
                    },
                    {
                        "fvnsEncapBlk": {
                            "attributes": {
                                "allocMode": "static",
                                "from": "vlan-%s" % service_vlan,
                                "to": "vlan-%s" % service_vlan
                            }
                        }
                    }
                ]
            }
        }
        return path, data

    def mcast_pool(self):
        mpool_name = self.config["aci_config"]["vmm_domain"]["mcast_pool"]
        mcast_start = self.config["aci_config"]["vmm_domain"]["mcast_range"]["start"]
        mcast_end = self.config["aci_config"]["vmm_domain"]["mcast_range"]["end"]

        path = "/api/mo/uni/infra/maddrns-%s.json" % mpool_name
        data = {
            "fvnsMcastAddrInstP": {
                "attributes": {
                    "name": mpool_name,
                    "dn": "uni/infra/maddrns-%s" % mpool_name
                },
                "children": [
                    {
                        "fvnsMcastAddrBlk": {
                            "attributes": {
                                "from": mcast_start,
                                "to": mcast_end
                            }
                        }
                    }
                ]
            }
        }
        return path, data

    def phys_dom(self):
        phys_name = self.config["aci_config"]["physical_domain"]["domain"]
        pool_name = self.config["aci_config"]["physical_domain"]["vlan_pool"]

        path = "/api/mo/uni/phys-%s.json" % phys_name
        data = {
            "physDomP": {
                "attributes": {
                    "dn": "uni/phys-%s" % phys_name,
                    "name": phys_name
                },
                "children": [
                    {
                        "infraRsVlanNs": {
                            "attributes": {
                                "tDn": "uni/infra/vlanns-[%s]-static" % pool_name
                            }
                        }
                    }
                ]
            }
        }
        return path, data

    def kube_dom(self):
        vmm_name = self.config["aci_config"]["vmm_domain"]["domain"]
        encap_type = self.config["aci_config"]["vmm_domain"]["encap_type"]
        mcast_fabric = self.config["aci_config"]["vmm_domain"]["mcast_fabric"]
        mpool_name = self.config["aci_config"]["vmm_domain"]["mcast_pool"]
        kube_controller = self.config["kube_config"]["controller"]

        path = "/api/mo/uni/vmmp-Kubernetes/dom-%s.json" % vmm_name
        data = {
            "vmmDomP": {
                "attributes": {
                    "name": vmm_name,
                    "mode": "k8s",
                    "enfPref": "sw",
                    "encapMode": encap_type,
                    "prefEncapMode": encap_type,
                    "mcastAddr": mcast_fabric,
                },
                "children": [
                    {
                        "vmmCtrlrP": {
                            "attributes": {
                                "name": vmm_name,
                                "mode": "k8s",
                                "scope": "kubernetes",
                                "hostOrIp": kube_controller,
                            },
                            "children": [
                                {
                                    "vmmInjectedCont": {
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vmmRsDomMcastAddrNs": {
                            "attributes": {
                                "tDn": "uni/infra/maddrns-%s" % mpool_name
                            }
                        }
                    }
                ]
            }
        }
        return path, data

    def kube_tn(self):
        tn_name = self.config["aci_config"]["cluster_tenant"]
        vmm_name = self.config["aci_config"]["vmm_domain"]["domain"]
        phys_name = self.config["aci_config"]["physical_domain"]["domain"]
        kubeapi_vlan = self.config["net_config"]["kubeapi_vlan"]
        kube_vrf = self.config["aci_config"]["vrf"]["name"]
        kube_l3out = self.config["aci_config"]["l3out"]["name"]
        node_subnet = self.config["net_config"]["node_subnet"]
        pod_subnet = self.config["net_config"]["pod_subnet"]

        path = "/api/mo/uni/tn-%s.json" % tn_name
        data = {
            "fvTenant": {
                "attributes": {
                    "name": tn_name,
                    "dn": "uni/tn-%s" % tn_name
                },
                "children": [
                    {
                        "fvAp": {
                            "attributes": {
                                "name": "kubernetes"
                            },
                            "children": [
                                {
                                    "fvAEPg": {
                                        "attributes": {
                                            "name": "kube-default"
                                        },
                                        "children": [
                                            {
                                                "fvRsDomAtt": {
                                                    "attributes": {
                                                        "tDn": "uni/vmmp-Kubernetes/dom-%s" % vmm_name
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "dns"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "l3out-allow-all"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "arp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "icmp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsBd": {
                                                    "attributes": {
                                                        "tnFvBDName": "kube-pod-bd"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                },
                                {
                                    "fvAEPg": {
                                        "attributes": {
                                            "name": "kube-system"
                                        },
                                        "children": [
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "arp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "dns"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "icmp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "health-check"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "icmp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "kube-api"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "l3out-allow-all"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsDomAtt": {
                                                    "attributes": {
                                                        "tDn": "uni/vmmp-Kubernetes/dom-%s" % vmm_name
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsBd": {
                                                    "attributes": {
                                                        "tnFvBDName": "kube-pod-bd"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                },
                                {
                                    "fvAEPg": {
                                        "attributes": {
                                            "name": "kube-nodes"
                                        },
                                        "children": [
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "kube-api"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsProv": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "icmp"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "health-check"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsCons": {
                                                    "attributes": {
                                                        "tnVzBrCPName": "l3out-allow-all"
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsDomAtt": {
                                                    "attributes": {
                                                        "encap": "vlan-%s" % kubeapi_vlan,
                                                        "tDn": "uni/phys-%s" % phys_name
                                                    }
                                                }
                                            },
                                            {
                                                "fvRsBd": {
                                                    "attributes": {
                                                        "tnFvBDName": "kube-node-bd"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                },
                            ]
                        }
                    },
                    {
                        "fvBD": {
                            "attributes": {
                                "name": "kube-node-bd"
                            },
                            "children": [
                                {
                                    "fvSubnet": {
                                        "attributes": {
                                            "ip": node_subnet,
                                            "scope": "public"
                                        }
                                    }
                                },
                                {
                                    "fvRsCtx": {
                                        "attributes": {
                                            "tnFvCtxName": kube_vrf
                                        }
                                    }
                                },
                                {
                                    "fvRsBDToOut": {
                                        "attributes": {
                                            "tnL3extOutName": kube_l3out
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "fvBD": {
                            "attributes": {
                                "name": "kube-pod-bd"
                            },
                            "children": [
                                {
                                    "fvSubnet": {
                                        "attributes": {
                                            "ip": pod_subnet
                                        }
                                    }
                                },
                                {
                                    "fvRsCtx": {
                                        "attributes": {
                                            "tnFvCtxName": kube_vrf
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "arp-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "arp",
                                            "etherT": "arp"
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "icmp-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "icmp",
                                            "etherT": "ip",
                                            "prot": "icmp"
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "health-check-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "health-check",
                                            "etherT": "ip",
                                            "prot": "tcp",
                                            "dFromPort": "8000",
                                            "dToPort": "11000",
                                            "stateful": "no",
                                            "tcpRules": ""
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "dns-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "dns-udp",
                                            "etherT": "ip",
                                            "prot": "udp",
                                            "dFromPort": "dns",
                                            "dToPort": "dns"
                                        }
                                    }
                                },
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "dns-tcp",
                                            "etherT": "ip",
                                            "prot": "tcp",
                                            "dFromPort": "dns",
                                            "dToPort": "dns",
                                            "stateful": "no",
                                            "tcpRules": ""
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "kube-api-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "kube-api",
                                            "etherT": "ip",
                                            "prot": "tcp",
                                            "dFromPort": "6443",
                                            "dToPort": "6443",
                                            "stateful": "no",
                                            "tcpRules": ""
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzFilter": {
                            "attributes": {
                                "name": "allow-all-filter"
                            },
                            "children": [
                                {
                                    "vzEntry": {
                                        "attributes": {
                                            "name": "allow-all"
                                        }
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "arp"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "arp-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "arp-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "kube-api"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "kube-api-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "kube-api-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "health-check"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "health-check-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "health-check-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "l3out-allow-all"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "allow-all-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "allow-all-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "dns"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "dns-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "dns-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    },
                    {
                        "vzBrCP": {
                            "attributes": {
                                "name": "icmp"
                            },
                            "children": [
                                {
                                    "vzSubj": {
                                        "attributes": {
                                            "name": "icmp-subj",
                                            "consMatchT": "AtleastOne",
                                            "provMatchT": "AtleastOne"
                                        },
                                        "children": [
                                            {
                                                "vzRsSubjFiltAtt": {
                                                    "attributes": {
                                                        "tnVzFilterName": "icmp-filter"
                                                    }
                                                }
                                            }
                                        ]
                                    }
                                }
                            ]
                        }
                    }
                ]
            }
        }
        return path, data