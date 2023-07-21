// Copyright 2017 Microsoft. All rights reserved.
// MIT License

package snat

import (
	"fmt"
	"net"
	"strings"

	"github.com/Azure/azure-container-networking/cni/log"
	"github.com/Azure/azure-container-networking/ebtables"
	"github.com/Azure/azure-container-networking/iptables"
	"github.com/Azure/azure-container-networking/netlink"
	"github.com/Azure/azure-container-networking/network/networkutils"
	"github.com/Azure/azure-container-networking/platform"
	"github.com/pkg/errors"
	"go.uber.org/zap"
)

const (
	azureSnatIfName     = "eth1"
	SnatBridgeName      = "azSnatbr"
	ImdsIP              = "169.254.169.254/32"
	vlanDropDeleteRule  = "ebtables -t nat -D PREROUTING -p 802_1Q -j DROP"
	vlanDropAddRule     = "ebtables -t nat -A PREROUTING -p 802_1Q -j DROP"
	vlanDropMatch       = "-p 802_1Q -j DROP"
	l2PreroutingEntries = "ebtables -t nat -L PREROUTING"
)

var errorSnatClient = errors.New("SnatClient Error")

func newErrorSnatClient(errStr string) error {
	return fmt.Errorf("%w : %s", errorSnatClient, errStr)
}

type Client struct {
	hostSnatVethName       string
	hostPrimaryMac         string
	containerSnatVethName  string
	localIP                string
	SnatBridgeIP           string
	SkipAddressesFromBlock []string
	enableProxyArpOnBridge bool
	netlink                netlink.NetlinkInterface
	plClient               platform.ExecClient
}

func NewSnatClient(hostIfName string,
	contIfName string,
	localIP string,
	snatBridgeIP string,
	hostPrimaryMac string,
	skipAddressesFromBlock []string,
	enableProxyArpOnBridge bool,
	nl netlink.NetlinkInterface,
	plClient platform.ExecClient,
) Client {
	log.Logger.Info("Initialize new snat client")
	snatClient := Client{
		hostSnatVethName:       hostIfName,
		containerSnatVethName:  contIfName,
		localIP:                localIP,
		SnatBridgeIP:           snatBridgeIP,
		hostPrimaryMac:         hostPrimaryMac,
		enableProxyArpOnBridge: enableProxyArpOnBridge,
		netlink:                nl,
		plClient:               plClient,
	}

	snatClient.SkipAddressesFromBlock = append(snatClient.SkipAddressesFromBlock, skipAddressesFromBlock...)

	log.Logger.Info("Initialize new snat client", zap.Any("snatClient", snatClient))

	return snatClient
}

func (client *Client) CreateSnatEndpoint() error {
	// Create linux Bridge for outbound connectivity
	if err := client.createSnatBridge(client.SnatBridgeIP, client.hostPrimaryMac); err != nil {
		log.Logger.Error("creating snat bridge failed with", zap.Any("error:", err))
		return err
	}

	nuc := networkutils.NewNetworkUtils(client.netlink, client.plClient)
	// Enabling proxy arp on bridge allows bridge to respond to arp requests it receives with its own mac otherwise arp requests are not getting forwarded and responded.
	if client.enableProxyArpOnBridge {
		// Enable proxy arp on bridge
		if err := nuc.SetProxyArp(SnatBridgeName); err != nil {
			log.Logger.Error("Enabling proxy arp failed with", zap.Any("error:", err))
			return errors.Wrap(err, "")
		}
	}

	// SNAT Rule to masquerade packets destined to non-vnet ip
	if err := client.addMasqueradeRule(client.SnatBridgeIP); err != nil {
		log.Logger.Error("Adding snat rule failed with", zap.Any("error:", err))
		return err
	}

	// Drop all vlan packets coming via linux bridge.
	if err := client.addVlanDropRule(); err != nil {
		log.Logger.Error("Adding vlan drop rule failed with", zap.Any("error:", err))
		return err
	}

	// Create veth pair to tie one end to container and other end to linux bridge
	if err := nuc.CreateEndpoint(client.hostSnatVethName, client.containerSnatVethName, nil); err != nil {
		log.Logger.Error("Creating Snat Endpoint failed with", zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	err := client.netlink.SetLinkMaster(client.hostSnatVethName, SnatBridgeName)
	if err != nil {
		return newErrorSnatClient(err.Error())
	}
	return nil
}

// AllowIPAddressesOnSnatBridge adds iptables rules  that allows only specific Private IPs via linux bridge
func (client *Client) AllowIPAddressesOnSnatBridge() error {
	if err := networkutils.AllowIPAddresses(SnatBridgeName, client.SkipAddressesFromBlock, iptables.Insert); err != nil {
		log.Logger.Error("AllowIPAddresses failed with", zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	return nil
}

// BlockIPAddressesOnSnatBridge adds iptables rules  that blocks all private IPs flowing via linux bridge
func (client *Client) BlockIPAddressesOnSnatBridge() error {
	if err := networkutils.BlockIPAddresses(SnatBridgeName, iptables.Append); err != nil {
		log.Logger.Error("AllowIPAddresses failed with", zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	return nil
}

// Move container veth inside container network namespace
func (client *Client) MoveSnatEndpointToContainerNS(netnsPath string, nsID uintptr) error {
	log.Logger.Info("Setting link netns", zap.String("containerSnatVethName", client.containerSnatVethName),
		zap.Any("netnsPath", netnsPath), zap.String("component", "snat"))
	err := client.netlink.SetLinkNetNs(client.containerSnatVethName, nsID)
	if err != nil {
		return newErrorSnatClient(err.Error())
	}
	return nil
}

// Configure Routes and setup name for container veth
func (client *Client) SetupSnatContainerInterface() error {
	epc := networkutils.NewNetworkUtils(client.netlink, client.plClient)
	if err := epc.SetupContainerInterface(client.containerSnatVethName, azureSnatIfName); err != nil {
		return newErrorSnatClient(err.Error())
	}

	client.containerSnatVethName = azureSnatIfName

	return nil
}

func getNCLocalAndGatewayIP(client *Client) (brIP, contIP net.IP) {
	bridgeIP, _, _ := net.ParseCIDR(client.SnatBridgeIP)
	containerIP, _, _ := net.ParseCIDR(client.localIP)
	return bridgeIP, containerIP
}

// This function adds iptables rules that allows only host to NC communication and not the other way
func (client *Client) AllowInboundFromHostToNC() error {
	bridgeIP, containerIP := getNCLocalAndGatewayIP(client)

	// Create CNI Output chain
	if err := iptables.CreateChain(iptables.V4, iptables.Filter, iptables.CNIOutputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Creating failed with", zap.Any("CNIOutputChain", iptables.CNIOutputChain), zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	// Forward traffic from Ouptut chain to CNI Output chain
	if err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.Output, "", iptables.CNIOutputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting forward rule to failed with", zap.Any("CNIOutputChain", iptables.CNIOutputChain), zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	// Allow connection from Host to NC
	matchCondition := fmt.Sprintf("-s %s -d %s", bridgeIP.String(), containerIP.String())
	err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.CNIOutputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting output rule failed with ", zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	// Create cniinput chain
	if err := iptables.CreateChain(iptables.V4, iptables.Filter, iptables.CNIInputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Creating failed with", zap.Any("CNIOutputChain", iptables.CNIOutputChain), zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	// Forward from Input to cniinput chain
	if err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.Input, "", iptables.CNIInputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting forward rule to failed with", zap.Any("CNIOutputChain", iptables.CNIOutputChain), zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	// Accept packets from NC only if established connection
	matchCondition = fmt.Sprintf(" -i %s -m state --state %s,%s", SnatBridgeName, iptables.Established, iptables.Related)
	err = iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.CNIInputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting input rule failed with", zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	snatContainerVeth, _ := net.InterfaceByName(client.containerSnatVethName)

	// Add static arp entry for localIP to prevent arp going out of VM
	log.Logger.Info("Adding static arp entry for ip", zap.Any("containerIP", containerIP),
		zap.String("HardwareAddr", snatContainerVeth.HardwareAddr.String()))
	linkInfo := netlink.LinkInfo{
		Name:       SnatBridgeName,
		IPAddr:     containerIP,
		MacAddress: snatContainerVeth.HardwareAddr,
	}

	err = client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.ADD, netlink.NUD_PERMANENT)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Error adding static arp entry for ip", zap.Any("containerIP", containerIP),
			zap.String("HardwareAddr", snatContainerVeth.HardwareAddr.String()), zap.Any("error:", err))
		return newErrorSnatClient(err.Error())
	}

	return nil
}

func (client *Client) DeleteInboundFromHostToNC() error {
	bridgeIP, containerIP := getNCLocalAndGatewayIP(client)

	// Delete allow connection from Host to NC
	matchCondition := fmt.Sprintf("-s %s -d %s", bridgeIP.String(), containerIP.String())
	err := iptables.DeleteIptableRule(iptables.V4, iptables.Filter, iptables.CNIOutputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("DeleteInboundFromHostToNC: Error removing output rule", zap.Any("error:", err))
	}

	// Remove static arp entry added for container local IP
	log.Logger.Info("Removing static arp entry for ip", zap.Any("containerIP", containerIP))
	linkInfo := netlink.LinkInfo{
		Name:       SnatBridgeName,
		IPAddr:     containerIP,
		MacAddress: nil,
	}

	err = client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.REMOVE, netlink.NUD_INCOMPLETE)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Error removing static arp entry for ip", zap.Any("containerIP", containerIP),
			zap.Any("error:", err))
	}

	return err
}

// This function adds iptables rules that allows only NC to Host communication and not the other way
func (client *Client) AllowInboundFromNCToHost() error {
	bridgeIP, containerIP := getNCLocalAndGatewayIP(client)

	// Create CNI Input chain
	if err := iptables.CreateChain(iptables.V4, iptables.Filter, iptables.CNIInputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Creating failed with", zap.String("CNIInputChain", iptables.CNIInputChain),
			zap.Any("error:", err))
		return err
	}

	// Forward traffic from Input to cniinput chain
	if err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.Input, "", iptables.CNIInputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting forward rule to failed with", zap.String("CNIInputChain", iptables.CNIInputChain),
			zap.Any("error:", err))
		return err
	}

	// Allow NC to Host connection
	matchCondition := fmt.Sprintf("-s %s -d %s", containerIP.String(), bridgeIP.String())
	err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.CNIInputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting output rule failed with", zap.Any("error:", err))
		return err
	}

	// Create CNI output chain
	if err := iptables.CreateChain(iptables.V4, iptables.Filter, iptables.CNIOutputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Creating failed with", zap.String("CNIInputChain", iptables.CNIInputChain),
			zap.Any("error:", err))
		return err
	}

	// Forward traffic from Output to CNI Output chain
	if err := iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.Output, "", iptables.CNIOutputChain); err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting forward rule to failed with", zap.String("CNIInputChain", iptables.CNIInputChain),
			zap.Any("error:", err))
		return err
	}

	// Accept packets from Host only if established connection
	matchCondition = fmt.Sprintf(" -o %s -m state --state %s,%s", SnatBridgeName, iptables.Established, iptables.Related)
	err = iptables.InsertIptableRule(iptables.V4, iptables.Filter, iptables.CNIOutputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("AllowInboundFromHostToNC: Inserting input rule failed with", zap.Any("error:", err))
		return err
	}

	snatContainerVeth, _ := net.InterfaceByName(client.containerSnatVethName)

	// Add static arp entry for localIP to prevent arp going out of VM
	log.Logger.Info("Adding static arp entry for ip", zap.Any("containerIP", containerIP), zap.String("HardwareAddr", snatContainerVeth.HardwareAddr.String()))
	linkInfo := netlink.LinkInfo{
		Name:       SnatBridgeName,
		IPAddr:     containerIP,
		MacAddress: snatContainerVeth.HardwareAddr,
	}

	err = client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.ADD, netlink.NUD_PERMANENT)
	if err != nil {
		log.Logger.Error("AllowInboundFromNCToHost: Error adding static arp entry for ip", zap.Any("containerIP", containerIP),
			zap.String("HardwareAddr", snatContainerVeth.HardwareAddr.String()), zap.Any("error:", err))
	}

	return err
}

func (client *Client) DeleteInboundFromNCToHost() error {
	bridgeIP, containerIP := getNCLocalAndGatewayIP(client)

	// Delete allow NC to Host connection
	matchCondition := fmt.Sprintf("-s %s -d %s", containerIP.String(), bridgeIP.String())
	err := iptables.DeleteIptableRule(iptables.V4, iptables.Filter, iptables.CNIInputChain, matchCondition, iptables.Accept)
	if err != nil {
		log.Logger.Error("DeleteInboundFromNCToHost: Error removing output rule", zap.Any("error:", err))
	}

	// Remove static arp entry added for container local IP
	log.Logger.Info("Removing static arp entry for ip ", zap.Any("containerIP", containerIP))
	linkInfo := netlink.LinkInfo{
		Name:       SnatBridgeName,
		IPAddr:     containerIP,
		MacAddress: nil,
	}

	err = client.netlink.SetOrRemoveLinkAddress(linkInfo, netlink.REMOVE, netlink.NUD_INCOMPLETE)
	if err != nil {
		log.Logger.Error("DeleteInboundFromNCToHost: Error removing static arp entry for ip",
			zap.Any("containerIP", containerIP), zap.Any("error:", err))
	}

	return err
}

// Configures Local IP Address for container Veth
func (client *Client) ConfigureSnatContainerInterface() error {
	log.Logger.Info("Adding IP address", zap.String("localIP", client.localIP),
		zap.String("containerSnatVethName", client.containerSnatVethName), zap.String("component", "snat"))
	ip, intIpAddr, _ := net.ParseCIDR(client.localIP)
	err := client.netlink.AddIPAddress(client.containerSnatVethName, ip, intIpAddr)
	if err != nil {
		return newErrorSnatClient(err.Error())
	}
	return nil
}

func (client *Client) DeleteSnatEndpoint() error {
	log.Logger.Info("Deleting snat veth pair", zap.String("hostSnatVethName", client.hostSnatVethName),
		zap.String("component", "snat"))
	err := client.netlink.DeleteLink(client.hostSnatVethName)
	if err != nil {
		log.Logger.Error("Failed to delete veth pair", zap.String("hostSnatVethName", client.hostSnatVethName),
			zap.Any("error:", err), zap.String("component", "snat"))
		return newErrorSnatClient(err.Error())
	}

	return nil
}

func (client *Client) setBridgeMac(hostPrimaryMac string) error {
	hwAddr, err := net.ParseMAC(hostPrimaryMac)
	if err != nil {
		log.Logger.Error("Error while parsing host primary mac", zap.String("hostPrimaryMac", hostPrimaryMac),
			zap.Any("error:", err))
		return err
	}

	if err = client.netlink.SetLinkAddress(SnatBridgeName, hwAddr); err != nil {
		log.Logger.Error("Error while setting macaddr on bridge", zap.String("hwAddr", hwAddr.String()),
			zap.Any("error:", err))
	}
	return err
}

func (client *Client) DropArpForSnatBridgeApipaRange(snatBridgeIP, azSnatVethIfName string) error {
	var err error
	_, ipCidr, _ := net.ParseCIDR(snatBridgeIP)
	if err = ebtables.SetArpDropRuleForIpCidr(ipCidr.String(), azSnatVethIfName); err != nil {
		log.Logger.Error("Error setting arp drop rule for snatbridge ip", zap.String("snatBridgeIP", snatBridgeIP))
	}

	return err
}

// This function creates linux bridge which will be used for outbound connectivity by NCs
func (client *Client) createSnatBridge(snatBridgeIP, hostPrimaryMac string) error {
	_, err := net.InterfaceByName(SnatBridgeName)
	if err == nil {
		log.Logger.Info("Snat Bridge already exists")
	} else {
		log.Logger.Info("Creating Snat bridge", zap.String("SnatBridgeName", SnatBridgeName), zap.String("component", "net"))

		link := netlink.BridgeLink{
			LinkInfo: netlink.LinkInfo{
				Type: netlink.LINK_TYPE_BRIDGE,
				Name: SnatBridgeName,
			},
		}

		if err := client.netlink.AddLink(&link); err != nil {
			return newErrorSnatClient(err.Error())
		}
	}

	log.Logger.Info("Setting snat bridge mac", zap.String("hostPrimaryMac", hostPrimaryMac))
	if err := client.setBridgeMac(hostPrimaryMac); err != nil {
		return err
	}

	nuc := networkutils.NewNetworkUtils(client.netlink, client.plClient)
	//nolint
	if err = nuc.DisableRAForInterface(SnatBridgeName); err != nil {
		return err
	}

	log.Logger.Info("Assigning on snat bridge", zap.String("snatBridgeIP", snatBridgeIP))

	ip, addr, _ := net.ParseCIDR(snatBridgeIP)
	err = client.netlink.AddIPAddress(SnatBridgeName, ip, addr)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "file exists") {
		log.Logger.Error("Failed to add IP address", zap.Any("addr", addr), zap.Any("error:", err), zap.String("component", "net"))
		return newErrorSnatClient(err.Error())
	}

	if err = client.netlink.SetLinkState(SnatBridgeName, true); err != nil {
		return newErrorSnatClient(err.Error())
	}

	return nil
}

// This function adds iptable rules that will snat all traffic that has source ip in apipa range and coming via linux bridge
func (client *Client) addMasqueradeRule(snatBridgeIPWithPrefix string) error {
	_, ipNet, _ := net.ParseCIDR(snatBridgeIPWithPrefix)
	matchCondition := fmt.Sprintf("-s %s", ipNet.String())
	return iptables.InsertIptableRule(iptables.V4, iptables.Nat, iptables.Postrouting, matchCondition, iptables.Masquerade)
}

// Drop all vlan traffic on linux bridge
func (client *Client) addVlanDropRule() error {
	out, err := client.plClient.ExecuteCommand(l2PreroutingEntries)
	if err != nil {
		log.Logger.Error("Error while listing ebtable rules", zap.String("component", "net"))
		return err
	}

	out = strings.TrimSpace(out)
	if strings.Contains(out, vlanDropMatch) {
		log.Logger.Info("vlan drop rule already exists")
		return nil
	}

	log.Logger.Info("Adding ebtable rule to drop vlan traffic on snat bridge", zap.String("vlanDropAddRule", vlanDropAddRule))
	_, err = client.plClient.ExecuteCommand(vlanDropAddRule)
	return err
}
