package onvif

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Device is one camera found on the network.
type Device struct {
	Host         string `json:"host"`
	Name         string `json:"name"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	ServiceURL   string `json:"serviceURL"`          // ONVIF device service; "" if unknown
	Source       string `json:"source"`              // "onvif" or "sweep"
	OpenPorts    []int  `json:"openPorts,omitempty"` // sweep only
}

var wsDiscoveryGroup = &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func probeMessage() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope" xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<e:Header><w:MessageID>uuid:` + newUUID() + `</w:MessageID><w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To><w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action></e:Header>
<e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body>
</e:Envelope>`)
}

// LocalIPv4 is one usable interface address on this machine.
type LocalIPv4 struct {
	Interface string
	IP        net.IP
	Net       *net.IPNet
}

// virtualPrefixes are container/VPN bridges where no camera lives; probing
// them only slows the scan down.
var virtualPrefixes = []string{"docker", "br-", "veth", "virbr", "lxc", "lxd", "cni", "flannel", "cali", "tun", "tap", "wg", "zt", "utun", "awdl", "llw", "bridge", "vmnet", "anpi", "ap1", "gif", "stf"}

// LocalIPv4s lists the up, multicast-capable, non-loopback IPv4 addresses.
func LocalIPv4s() []LocalIPv4 {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []LocalIPv4
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || ifc.Flags&net.FlagMulticast == 0 {
			continue
		}
		if hasAnyPrefix(ifc.Name, virtualPrefixes) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, LocalIPv4{Interface: ifc.Name, IP: ip4, Net: &net.IPNet{IP: ip4.Mask(ipn.Mask), Mask: ipn.Mask}})
		}
	}
	return out
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// Discover sends a WS-Discovery probe out of every LAN interface and collects
// ONVIF cameras that answer within timeout. Cameras on other subnets/VLANs
// won't hear multicast — use Sweep or a manual IP for those.
func Discover(ctx context.Context, timeout time.Duration) ([]Device, error) {
	locals := LocalIPv4s()
	if len(locals) == 0 {
		return nil, fmt.Errorf("no active network interface")
	}
	var (
		mu    sync.Mutex
		found = map[string]Device{}
		wg    sync.WaitGroup
		errs  []error
	)
	for _, l := range locals {
		wg.Add(1)
		go func(l LocalIPv4) {
			defer wg.Done()
			devs, err := probeOn(ctx, l.IP, timeout)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", l.Interface, err))
			}
			for _, d := range devs {
				if _, seen := found[d.Host]; !seen {
					found[d.Host] = d
				}
			}
		}(l)
	}
	wg.Wait()
	if len(found) == 0 && len(errs) == len(locals) {
		return nil, errs[0]
	}
	return sortedDevices(found), nil
}

func sortedDevices(m map[string]Device) []Device {
	out := make([]Device, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return ipLess(out[i].Host, out[j].Host) })
	return out
}

func ipLess(a, b string) bool {
	ia, ib := net.ParseIP(a).To4(), net.ParseIP(b).To4()
	if ia == nil || ib == nil {
		return a < b
	}
	for k := 0; k < 4; k++ {
		if ia[k] != ib[k] {
			return ia[k] < ib[k]
		}
	}
	return false
}

func probeOn(ctx context.Context, local net.IP, timeout time.Duration) ([]Device, error) {
	addr := [4]byte(local.To4())
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			// Send the multicast out of THIS interface, not just the default route.
			serr = unix.SetsockoptInet4Addr(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_IF, addr)
		}); err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(ctx, "udp4", net.JoinHostPort(local.String(), "0"))
	if err != nil {
		return nil, err
	}
	defer pc.Close()

	// UDP multicast is lossy; WS-Discovery recommends repeating the probe.
	msg := probeMessage()
	for i := 0; i < 2; i++ {
		if _, err := pc.WriteTo(msg, wsDiscoveryGroup); err != nil {
			return nil, err
		}
		if i == 0 {
			time.Sleep(150 * time.Millisecond)
		}
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = pc.SetReadDeadline(deadline)
	found := map[string]Device{}
	buf := make([]byte, 64*1024)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			break // deadline reached
		}
		sender := ""
		if ua, ok := from.(*net.UDPAddr); ok {
			sender = ua.IP.String()
		}
		for _, d := range parseProbeMatches(buf[:n], sender) {
			if _, seen := found[d.Host]; !seen {
				found[d.Host] = d
			}
		}
	}
	return sortedDevices(found), nil
}

// parseProbeMatches turns a WS-Discovery ProbeMatches reply into devices.
func parseProbeMatches(data []byte, sender string) []Device {
	root := parseXML(data)
	var out []Device
	for _, m := range root.all("ProbeMatch") {
		service := pickXAddr(strings.Fields(m.value("XAddrs")), sender)
		if service == "" {
			continue
		}
		u, err := url.Parse(service)
		if err != nil || u.Hostname() == "" {
			continue
		}
		scopes := strings.Fields(m.value("Scopes"))
		d := Device{
			Host:         u.Hostname(),
			ServiceURL:   service,
			Source:       "onvif",
			Name:         scopeValue(scopes, "name"),
			Manufacturer: firstNonEmpty(scopeValue(scopes, "mfr"), scopeValue(scopes, "manufacturer")),
			Model:        scopeValue(scopes, "hardware"),
		}
		if d.Name == "" {
			d.Name = firstNonEmpty(d.Model, "ONVIF camera")
		}
		out = append(out, d)
	}
	return out
}

// pickXAddr prefers the address matching the host that actually answered (some
// cameras list several, including unreachable internal ones), then any IPv4.
func pickXAddr(xaddrs []string, sender string) string {
	var firstV4 string
	for _, x := range xaddrs {
		u, err := url.Parse(x)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		if sender != "" && u.Hostname() == sender {
			return x
		}
		if firstV4 == "" && net.ParseIP(u.Hostname()).To4() != nil {
			firstV4 = x
		}
	}
	if firstV4 != "" {
		return firstV4
	}
	if len(xaddrs) > 0 {
		return xaddrs[0]
	}
	return ""
}

// scopeValue reads e.g. onvif://www.onvif.org/name/Front_Door → "Front Door".
func scopeValue(scopes []string, key string) string {
	marker := "/" + key + "/"
	for _, s := range scopes {
		i := strings.Index(strings.ToLower(s), marker)
		if i < 0 {
			continue
		}
		v := s[i+len(marker):]
		if dec, err := url.PathUnescape(v); err == nil {
			v = dec
		}
		v = strings.TrimSpace(strings.ReplaceAll(v, "_", " "))
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
