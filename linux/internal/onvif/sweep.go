package onvif

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"time"
)

// rtspPorts mark a host as a camera candidate; onvifPorts are where cameras
// commonly serve ONVIF (2020 = TP-Link Tapo, 8000/8899 = many OEM boards).
var (
	rtspPorts  = []int{554, 8554}
	onvifPorts = []int{80, 2020, 8000, 8080, 8899, 8999}
)

// maxSweepHosts bounds a sweep to a /22 per interface — larger LANs get the
// /24 around our own address instead of tens of thousands of dials.
const maxSweepHosts = 1022

// Sweep TCP-probes every address on the local subnets for an open RTSP port.
// It finds cameras that don't answer WS-Discovery. It generates real network
// traffic, so it only runs when the user asks for it.
func Sweep(ctx context.Context) ([]Device, []string, error) {
	var targets []net.IP
	var subnets []string
	seen := map[string]bool{}
	self := map[string]bool{}
	for _, l := range LocalIPv4s() {
		self[l.IP.String()] = true
	}
	for _, l := range LocalIPv4s() {
		n := l.Net
		if ones, _ := n.Mask.Size(); ones < 22 {
			n = &net.IPNet{IP: l.IP.Mask(net.CIDRMask(24, 32)), Mask: net.CIDRMask(24, 32)}
		}
		if seen[n.String()] {
			continue
		}
		seen[n.String()] = true
		subnets = append(subnets, n.String()+" ("+l.Interface+")")
		for _, ip := range hostsIn(n) {
			if !self[ip.String()] {
				targets = append(targets, ip)
			}
		}
	}
	if len(targets) == 0 {
		return nil, subnets, errors.New("no local IPv4 subnet to scan")
	}

	var (
		mu    sync.Mutex
		found = map[string]Device{}
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 128)
	)
	for _, ip := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip net.IP) {
			defer wg.Done()
			defer func() { <-sem }()
			// Check RTSP first; only probe the other ports on hosts that have it.
			open := openPorts(ctx, ip.String(), rtspPorts, 500*time.Millisecond)
			if len(open) == 0 {
				return
			}
			open = append(open, openPorts(ctx, ip.String(), onvifPorts, 500*time.Millisecond)...)
			sort.Ints(open)
			d := Device{Host: ip.String(), Name: "Camera at " + ip.String(), Source: "sweep", OpenPorts: open}
			d.ServiceURL = guessServiceURL(ip.String(), open)
			mu.Lock()
			found[d.Host] = d
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	return sortedDevices(found), subnets, ctx.Err()
}

func hostsIn(n *net.IPNet) []net.IP {
	ones, bits := n.Mask.Size()
	if bits != 32 || ones > 30 {
		return nil
	}
	base := binary.BigEndian.Uint32(n.IP.To4())
	size := uint32(1) << (32 - ones)
	var out []net.IP
	for i := uint32(1); i < size-1 && len(out) < maxSweepHosts; i++ {
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, base+i)
		out = append(out, ip)
	}
	return out
}

func openPorts(ctx context.Context, host string, ports []int, timeout time.Duration) []int {
	var open []int
	d := net.Dialer{Timeout: timeout}
	for _, p := range ports {
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			c.Close()
			open = append(open, p)
		}
	}
	return open
}

func guessServiceURL(host string, open []int) string {
	for _, p := range []int{2020, 80, 8000, 8080, 8899, 8999} {
		for _, o := range open {
			if o == p {
				if p == 80 {
					return "http://" + host + "/onvif/device_service"
				}
				return fmt.Sprintf("http://%s:%d/onvif/device_service", host, p)
			}
		}
	}
	return ""
}

// FetchByHost is for cameras added by IP: it tries the usual ONVIF ports on
// that host until one answers. An auth failure stops the search (right port,
// wrong password) so the user gets the useful error.
func FetchByHost(ctx context.Context, host, user, pass string) (*Details, error) {
	open := openPorts(ctx, host, onvifPorts, 1500*time.Millisecond)
	if len(open) == 0 {
		return nil, fmt.Errorf("%s has no ONVIF service on ports 80, 2020, 8000, 8080, 8899 or 8999", host)
	}
	var lastErr error
	for _, p := range []int{2020, 80, 8000, 8080, 8899, 8999} {
		if !contains(open, p) {
			continue
		}
		svc := guessServiceURL(host, []int{p})
		d, err := FetchDetails(ctx, svc, user, pass)
		if err == nil {
			return d, nil
		}
		if errors.Is(err, ErrAuth) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
