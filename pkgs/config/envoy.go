package config

import (
	"embed"
	"fmt"
	"html/template"
	"os"
	"strings"

	_ "github.com/cncf/xds/go/udpa/type/v1"
	admin "github.com/envoyproxy/go-control-plane/envoy/admin/v3"
	envoy_config_endpoint_v3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoy_config_route_v3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/file/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/cors/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/fault/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/grpc_stats/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/lua/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/wasm/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/http_inspector/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/original_dst/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/listener/tls_inspector/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoy_extensions_filters_network_http_connection_manager_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/request_id/uuid/v3"
	_ "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	"google.golang.org/protobuf/encoding/protojson"
	_ "istio.io/api/envoy/config/filter/http/alpn/v2alpha1"
	_ "istio.io/api/envoy/config/filter/network/metadata_exchange"
)

//go:embed sankey.gohtml
var senkeyFS embed.FS

func init() {

}

type nodeMap map[string][]*Node

func (nm nodeMap) insert(parentName, name string) {
	nm[parentName] = append(nm[parentName], &Node{Name: name, Value: 1})
}

func Decode(configBytes []byte) error {
	cd := new(admin.ConfigDump)
	if err := protojson.Unmarshal(configBytes, cd); err != nil {
		return err
	}

	lMap := make(nodeMap)
	rMap := make(nodeMap)
	cMap := make(nodeMap)
	for _, config := range cd.Configs {
		lcd := new(admin.ListenersConfigDump)
		rcd := new(admin.RoutesConfigDump)
		ecd := new(admin.EndpointsConfigDump)
		if config.MessageIs(lcd) {
			_ = config.UnmarshalTo(lcd)
			for _, listener := range lcd.GetDynamicListeners() {
				l := new(envoy_config_listener_v3.Listener)
				_ = listener.ActiveState.Listener.UnmarshalTo(l)

				for _, chain := range l.GetFilterChains() {
					if !strings.HasPrefix(l.Name, "0.0.0.0_") {
						continue
					}

					if !strings.HasPrefix(l.Name, "0.0.0.0_8080") {
						continue
					}

					// if !strings.HasSuffix(l.Name, "_10912") {
					// 	continue
					// }

					for _, filter := range chain.Filters {
						hcm := new(envoy_extensions_filters_network_http_connection_manager_v3.HttpConnectionManager)
						if filter.GetTypedConfig().MessageIs(hcm) {
							filter.GetTypedConfig().UnmarshalTo(hcm)
							switch r := hcm.GetRouteSpecifier().(type) {
							case *envoy_extensions_filters_network_http_connection_manager_v3.HttpConnectionManager_RouteConfig:
								lMap.insert(l.Name, r.RouteConfig.Name)
							case *envoy_extensions_filters_network_http_connection_manager_v3.HttpConnectionManager_Rds:
								lMap.insert(l.Name, r.Rds.RouteConfigName)
							}
						}
					}
				}
			}
		} else if config.MessageIs(rcd) {
			_ = config.UnmarshalTo(rcd)
			for _, routeConfig := range rcd.GetDynamicRouteConfigs() {
				r := new(envoy_config_route_v3.RouteConfiguration)
				routeConfig.RouteConfig.UnmarshalTo(r)
				for _, host := range r.VirtualHosts {
					for _, route := range host.Routes {
						rMap.insert(r.Name, route.GetRoute().GetCluster())
					}
				}
			}
		} else if config.MessageIs(ecd) {
			_ = config.UnmarshalTo(ecd)
			for _, endpoint := range ecd.GetDynamicEndpointConfigs() {
				e := new(envoy_config_endpoint_v3.ClusterLoadAssignment)
				endpoint.EndpointConfig.UnmarshalTo(e)
				for _, endpoints := range e.Endpoints {
					for _, lbEndpoint := range endpoints.LbEndpoints {
						address := lbEndpoint.GetEndpoint().GetAddress().GetSocketAddress()
						ep := fmt.Sprintf("%s:%d", address.Address, address.GetPortValue())
						cMap.insert(e.ClusterName, ep)
					}
				}
			}
		}
	}

	// 生成根节点
	lNodes := make([]*Node, 0)
	for listener, rNodes := range lMap {
		lNodes = append(lNodes, &Node{Name: listener, Children: rNodes}) // route
	}

	// 根节点领养子节点
	for _, lNode := range lNodes {
		for _, rNode := range lNode.Children {
			rNode.Children = rMap[rNode.Name] // cluster

			for _, cNode := range rNode.Children {
				cNode.Children = cMap[cNode.Name] // endpoints
			}
		}
	}

	nodeMap := make(map[string]*Node)
	links := make([]Link, 0)
	for _, lNode := range lNodes {
		lNode.calcV()
		lNode.dump("")
		nodeMap[lNode.Name] = lNode

		for _, rNode := range lNode.Children {
			nodeMap[rNode.Name] = rNode
			links = append(links, Link{Source: lNode.Name, Target: rNode.Name, Value: lNode.Value})

			for _, cNode := range rNode.Children {
				nodeMap[cNode.Name] = cNode
				links = append(links, Link{Source: rNode.Name, Target: cNode.Name, Value: cNode.Value})

				for _, eNode := range cNode.Children {
					nodeMap[eNode.Name] = eNode
					links = append(links, Link{Source: cNode.Name, Target: eNode.Name, Value: eNode.Value})
				}
			}
		}
	}

	nodes := make([]*Node, 0)
	for _, node := range nodeMap {
		nodes = append(nodes, node)
	}

	// listener   =>   route  =>  cluster  => endpoint

	tpl := template.Must(template.ParseFS(senkeyFS, "sankey.gohtml"))
	f, err := os.OpenFile("senkey.html", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}

	return tpl.Execute(f, map[string]interface{}{
		"Nodes": nodes,
		"Links": links,
	})
}

type Node struct {
	Name     string  `json:"name"`
	Value    int     `json:"-"`
	Children []*Node `json:"-"`
}

func (n *Node) calcV() {
	if len(n.Children) == 0 {
		return
	}

	n.Value = 0
	for _, child := range n.Children {
		child.calcV()
		n.Value += child.Value
	}
}

func (n *Node) dump(tabs string) {
	fmt.Printf("%s|--%s[%d]\n", tabs, n.Name, n.Value)

	tabs += "    "
	for _, child := range n.Children {
		child.dump(tabs)
	}
}

type Link struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Value  int    `json:"value"`
}
