package network

import (
	"fmt"
	"net"

	"github.com/Azure/azure-container-networking/cni/log"
	"github.com/Azure/azure-container-networking/ebtables"
	"github.com/Azure/azure-container-networking/netio"
	"github.com/Azure/azure-container-networking/netlink"
	"github.com/Azure/azure-container-networking/network/networkutils"
	"github.com/Azure/azure-container-networking/platform"
	"go.uber.org/zap"
)

const (
	defaultV6VnetCidr = "2001:1234:5678:9abc::/64"
	defaultV6HostGw   = "fe80::1234:5678:9abc"
	defaultHostGwMac  = "12:34:56:78:9a:bc"
)

type LinuxBridgeEndpointClient struct {
	bridgeName        string
	hostPrimaryIfName string
	hostVethName      string
	containerVethName string
	hostPrimaryMac    net.HardwareAddr
	containerMac      net.HardwareAddr
	hostIPAddresses   []*net.IPNet
	mode              string
	netlink           netlink.NetlinkInterface
	plClient          platform.ExecClient
	netioshim         netio.NetIOInterface
	nuc               networkutils.NetworkUtils
}

func NewLinuxBridgeEndpointClient(
	extIf *externalInterface,
	hostVethName string,
	containerVethName string,
	mode string,
	nl netlink.NetlinkInterface,
	plc platform.ExecClient,
) *LinuxBridgeEndpointClient {
	client := &LinuxBridgeEndpointClient{
		bridgeName:        extIf.BridgeName,
		hostPrimaryIfName: extIf.Name,
		hostVethName:      hostVethName,
		containerVethName: containerVethName,
		hostPrimaryMac:    extIf.MacAddress,
		hostIPAddresses:   []*net.IPNet{},
		mode:              mode,
		netlink:           nl,
		plClient:          plc,
		netioshim:         &netio.NetIO{},
	}

	client.hostIPAddresses = append(client.hostIPAddresses, extIf.IPAddresses...)
	client.nuc = networkutils.NewNetworkUtils(nl, plc)
	return client
}

func (client *LinuxBridgeEndpointClient) AddEndpoints(epInfo *EndpointInfo) error {
	if err := client.nuc.CreateEndpoint(client.hostVethName, client.containerVethName, nil); err != nil {
		return err
	}

	containerIf, err := net.InterfaceByName(client.containerVethName)
	if err != nil {
		return err
	}

	client.containerMac = containerIf.HardwareAddr
	return nil
}

func (client *LinuxBridgeEndpointClient) AddEndpointRules(epInfo *EndpointInfo) error {
	var err error

	log.Logger.Info("Setting link master", zap.String("linkName", client.hostVethName), zap.String("bridgeName", client.bridgeName), zap.String("component", "net"))
	if err := client.netlink.SetLinkMaster(client.hostVethName, client.bridgeName); err != nil {
		return err
	}

	for _, ipAddr := range epInfo.IPAddresses {
		if ipAddr.IP.To4() != nil {
			// Add ARP reply rule.
			log.Logger.Info("Adding ARP reply rule for IP address", zap.String("address", ipAddr.String()), zap.String("component", "net"))
			if err = ebtables.SetArpReply(ipAddr.IP, client.getArpReplyAddress(client.containerMac), ebtables.Append); err != nil {
				return err
			}
		}

		// Add MAC address translation rule.
		log.Logger.Info("Adding MAC DNAT rule for IP address", zap.String("address", ipAddr.String()), zap.String("component", "net"))
		if err := ebtables.SetDnatForIPAddress(client.hostPrimaryIfName, ipAddr.IP, client.containerMac, ebtables.Append); err != nil {
			return err
		}

		if client.mode != opModeTunnel && ipAddr.IP.To4() != nil {
			log.Logger.Info("Adding static arp for IP address and MAC in VM", zap.String("address", ipAddr.String()), zap.String("MAC", client.containerMac.String()), zap.String("component", "net"))
			linkInfo := netlink.LinkInfo{
				Name:       client.bridgeName,
				IPAddr:     ipAddr.IP,
				MacAddress: client.containerMac,
			}

			if err := client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.ADD, netlink.NUD_PERMANENT); err != nil {
				log.Logger.Info("Failed setting arp in vm with", zap.Any("error:", err), zap.String("component", "net"))
			}
		}
	}

	addRuleToRouteViaHost(epInfo)

	log.Logger.Info("Setting hairpin for ", zap.String("hostveth", client.hostVethName), zap.String("component", "net"))
	if err := client.netlink.SetLinkHairpin(client.hostVethName, true); err != nil {
		log.Logger.Info("Setting up hairpin failed for interface error", zap.String("interfaceName", client.hostVethName), zap.Any("error:", err), zap.String("component", "net"))
		return err
	}

	return nil
}

func (client *LinuxBridgeEndpointClient) DeleteEndpointRules(ep *endpoint) {
	// Delete rules for IP addresses on the container interface.
	for _, ipAddr := range ep.IPAddresses {
		if ipAddr.IP.To4() != nil {
			// Delete ARP reply rule.
			log.Logger.Info("[net] Deleting ARP reply rule for IP address on", zap.String("address", ipAddr.String()), zap.String("Id", ep.Id), zap.String("component", "net"))
			err := ebtables.SetArpReply(ipAddr.IP, client.getArpReplyAddress(ep.MacAddress), ebtables.Delete)
			if err != nil {
				log.Logger.Error("Failed to delete ARP reply rule for IP address", zap.String("address", ipAddr.String()), zap.Any("error:", err), zap.String("component", "net"))
			}
		}

		// Delete MAC address translation rule.
		log.Logger.Info("Deleting MAC DNAT rule for IP address on", zap.String("address", ipAddr.String()), zap.String("Id", ep.Id), zap.String("component", "net"))
		err := ebtables.SetDnatForIPAddress(client.hostPrimaryIfName, ipAddr.IP, ep.MacAddress, ebtables.Delete)
		if err != nil {
			log.Logger.Error("Failed to delete MAC DNAT rule for IP address", zap.String("address", ipAddr.String()), zap.Any("error:", err), zap.String("component", "net"))
		}

		if client.mode != opModeTunnel && ipAddr.IP.To4() != nil {
			log.Logger.Info("Removing static arp for IP address and MAC from VM", zap.String("address", ipAddr.String()), zap.String("MAC", ep.MacAddress.String()), zap.String("component", "net"))
			linkInfo := netlink.LinkInfo{
				Name:       client.bridgeName,
				IPAddr:     ipAddr.IP,
				MacAddress: ep.MacAddress,
			}
			err := client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.REMOVE, netlink.NUD_INCOMPLETE)
			if err != nil {
				log.Logger.Info("Failed removing arp from vm with", zap.Any("error:", err), zap.String("component", "net"))
			}
		}
	}
}

// getArpReplyAddress returns the MAC address to use in ARP replies.
func (client *LinuxBridgeEndpointClient) getArpReplyAddress(epMacAddress net.HardwareAddr) net.HardwareAddr {
	var macAddress net.HardwareAddr

	if client.mode == opModeTunnel {
		// In tunnel mode, resolve all IP addresses to the virtual MAC address for hairpinning.
		macAddress, _ = net.ParseMAC(virtualMacAddress)
	} else {
		// Otherwise, resolve to actual MAC address.
		macAddress = epMacAddress
	}

	return macAddress
}

func (client *LinuxBridgeEndpointClient) MoveEndpointsToContainerNS(epInfo *EndpointInfo, nsID uintptr) error {
	// Move the container interface to container's network namespace.
	log.Logger.Info("Setting link netns", zap.String("containerVethName", client.containerVethName), zap.String("NetNsPath", epInfo.NetNsPath), zap.String("component", "net"))
	if err := client.netlink.SetLinkNetNs(client.containerVethName, nsID); err != nil {
		return newErrorLinuxBridgeClient(err.Error())
	}

	return nil
}

func (client *LinuxBridgeEndpointClient) SetupContainerInterfaces(epInfo *EndpointInfo) error {
	if err := client.nuc.SetupContainerInterface(client.containerVethName, epInfo.IfName); err != nil {
		return err
	}

	client.containerVethName = epInfo.IfName

	return nil
}

func (client *LinuxBridgeEndpointClient) ConfigureContainerInterfacesAndRoutes(epInfo *EndpointInfo) error {
	if err := client.nuc.AssignIPToInterface(client.containerVethName, epInfo.IPAddresses); err != nil {
		return err
	}

	if err := addRoutes(client.netlink, client.netioshim, client.containerVethName, epInfo.Routes); err != nil {
		return err
	}

	if err := client.setupIPV6Routes(epInfo); err != nil {
		return err
	}

	if err := client.setIPV6NeighEntry(epInfo); err != nil {
		return err
	}

	return nil
}

func (client *LinuxBridgeEndpointClient) DeleteEndpoints(ep *endpoint) error {
	log.Logger.Info("Deleting veth pair", zap.String("hostIfName", ep.HostIfName), zap.String("interfaceName", ep.IfName), zap.String("component", "net"))
	err := client.netlink.DeleteLink(ep.HostIfName)
	if err != nil {
		log.Logger.Error("Failed to delete veth pair", zap.String("hostIfName", ep.HostIfName), zap.Any("error:", err), zap.String("component", "net"))
		return err
	}

	return nil
}

func addRuleToRouteViaHost(epInfo *EndpointInfo) error {
	for _, ipAddr := range epInfo.IPsToRouteViaHost {
		tableName := "broute"
		chainName := "BROUTING"
		rule := fmt.Sprintf("-p IPv4 --ip-dst %s -j redirect", ipAddr)

		// Check if EB rule exists
		log.Logger.Info("Checking if EB rule already exists in table chain", zap.String("rule", rule), zap.String("tableName", tableName), zap.String("chainName", chainName), zap.String("component", "net"))
		exists, err := ebtables.EbTableRuleExists(tableName, chainName, rule)
		if err != nil {
			log.Logger.Error("Failed to check if EB table rule exists with", zap.Any("error:", err), zap.String("component", "net"))
			return err
		}

		if exists {
			// EB rule already exists.
			log.Logger.Info("EB rule already exists in table chain", zap.String("rule", rule), zap.String("tableName", tableName), zap.String("chainName", chainName), zap.String("component", "net"))
		} else {
			// Add EB rule to route via host.
			log.Logger.Info("Adding EB rule to route via host for IP", zap.Any("address", ipAddr), zap.String("component", "net"))
			if err := ebtables.SetBrouteAccept(ipAddr, ebtables.Append); err != nil {
				log.Logger.Error("Failed to add EB rule to route via host with", zap.Any("error:", err), zap.String("component", "net"))
				return err
			}
		}
	}

	return nil
}

func (client *LinuxBridgeEndpointClient) setupIPV6Routes(epInfo *EndpointInfo) error {
	if epInfo.IPV6Mode != "" {
		if epInfo.VnetCidrs == "" {
			epInfo.VnetCidrs = defaultV6VnetCidr
		}

		routes := []RouteInfo{}
		_, v6IpNet, _ := net.ParseCIDR(epInfo.VnetCidrs)
		v6Gw := net.ParseIP(defaultV6HostGw)
		vnetRoute := RouteInfo{
			Dst:      *v6IpNet,
			Gw:       v6Gw,
			Priority: 101,
		}

		var vmV6Route RouteInfo

		for _, ipAddr := range client.hostIPAddresses {
			if ipAddr.IP.To4() == nil {
				vmV6Route = RouteInfo{
					Dst:      *ipAddr,
					Priority: 100,
				}
			}
		}

		_, defIPNet, _ := net.ParseCIDR("::/0")
		defaultV6Route := RouteInfo{
			Dst: *defIPNet,
			Gw:  v6Gw,
		}

		routes = append(routes, vnetRoute)
		routes = append(routes, vmV6Route)
		routes = append(routes, defaultV6Route)

		log.Logger.Info("Adding ipv6 routes in", zap.Any("container", routes), zap.String("component", "net"))
		if err := addRoutes(client.netlink, client.netioshim, client.containerVethName, routes); err != nil {
			return nil
		}
	}

	return nil
}

func (client *LinuxBridgeEndpointClient) setIPV6NeighEntry(epInfo *EndpointInfo) error {
	if epInfo.IPV6Mode != "" {
		log.Logger.Info("Add neigh entry for host gw ip", zap.String("component", "net"))
		hardwareAddr, _ := net.ParseMAC(defaultHostGwMac)
		hostGwIp := net.ParseIP(defaultV6HostGw)
		linkInfo := netlink.LinkInfo{
			Name:       client.containerVethName,
			IPAddr:     hostGwIp,
			MacAddress: hardwareAddr,
		}
		if err := client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.ADD, netlink.NUD_PERMANENT); err != nil {
			log.Logger.Error("Failed setting neigh entry in", zap.Any("container", err.Error()), zap.String("component", "net"))
			return err
		}
	}

	return nil
}
