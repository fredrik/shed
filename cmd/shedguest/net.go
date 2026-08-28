//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
	"github.com/vishvananda/netlink"
)

// networkUp brings up lo and eth0, acquires a DHCP lease from macOS's
// bootpd on the vmnet shared subnet, and installs address + default route.
func networkUp(ctx context.Context) (net.IP, error) {
	if lo, err := netlink.LinkByName("lo"); err == nil {
		netlink.LinkSetUp(lo)
	}

	link, err := netlink.LinkByName("eth0")
	if err != nil {
		return nil, fmt.Errorf("eth0: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return nil, fmt.Errorf("eth0 up: %w", err)
	}

	// macOS bootpd occasionally drops a DISCOVER/OFFER; on this sub-ms
	// bridge the default 5s retransmit turns one lost packet into a 5s
	// boot stall. Retransmit fast instead, within the same overall budget.
	client, err := nclient4.New("eth0",
		nclient4.WithTimeout(100*time.Millisecond), nclient4.WithRetry(90))
	if err != nil {
		return nil, fmt.Errorf("dhcp client: %w", err)
	}
	defer client.Close()

	dhcpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, err := client.Request(dhcpCtx)
	if err != nil {
		return nil, fmt.Errorf("dhcp request: %w", err)
	}
	ack := lease.ACK

	ip := ack.YourIPAddr
	mask := ack.SubnetMask()
	if mask == nil {
		mask = ip.DefaultMask()
	}
	addr := &netlink.Addr{IPNet: &net.IPNet{IP: ip, Mask: mask}}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return nil, fmt.Errorf("addr add %s: %w", addr, err)
	}

	if routers := ack.Router(); len(routers) > 0 {
		route := &netlink.Route{
			LinkIndex: link.Attrs().Index,
			Gw:        routers[0],
		}
		if err := netlink.RouteAdd(route); err != nil {
			return nil, fmt.Errorf("route add: %w", err)
		}
	}

	if dns := ack.DNS(); len(dns) > 0 {
		writeResolvConf(dns)
	}

	fmt.Printf("shedguest: eth0 %s via dhcp\n", ip)
	return ip, nil
}

func writeResolvConf(servers []net.IP) {
	f, err := os.OpenFile("/etc/resolv.conf", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, s := range servers {
		fmt.Fprintf(f, "nameserver %s\n", s)
	}
}
