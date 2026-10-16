package onvif

// Scan and Probe orchestrate discovery for whichever server runs them: the
// dashboard's own (api/v1, internal/server) or a recording server asked over
// the control link (link.CmdDiscover / link.CmdProbe, internal/recorder).

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var hostnamePattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// ScanTimeout caps a whole scan; sweeps of a /24 fit well within it.
const ScanTimeout = 45 * time.Second

// ProbeTimeout caps one camera probe (ONVIF fetch plus port fallbacks).
const ProbeTimeout = 40 * time.Second

// Scan finds cameras on this machine's LAN: ONVIF WS-Discovery always, plus
// a TCP sweep of the local subnets when sweep is true. Partial failures
// come back as warnings, not an error.
func Scan(ctx context.Context, sweep bool) (devices []Device, subnets, warnings []string) {
	ctx, cancel := context.WithTimeout(ctx, ScanTimeout)
	defer cancel()

	found := map[string]Device{}
	var order []string
	add := func(d Device) {
		if prev, ok := found[d.Host]; ok {
			// ONVIF identity wins; keep the sweep's port list.
			if len(prev.OpenPorts) == 0 {
				prev.OpenPorts = d.OpenPorts
			}
			if prev.ServiceURL == "" {
				prev.ServiceURL = d.ServiceURL
			}
			found[d.Host] = prev
			return
		}
		found[d.Host] = d
		order = append(order, d.Host)
	}

	devs, onvifErr := Discover(ctx, 3*time.Second)
	for _, d := range devs {
		add(d)
	}
	var sweepErr error
	if sweep {
		var swept []Device
		swept, subnets, sweepErr = Sweep(ctx)
		for _, d := range swept {
			add(d)
		}
	}
	devices = make([]Device, 0, len(order))
	for _, h := range order {
		devices = append(devices, found[h])
	}
	if onvifErr != nil {
		warnings = append(warnings, "ONVIF multicast: "+onvifErr.Error())
	}
	if sweepErr != nil {
		warnings = append(warnings, "Network sweep: "+sweepErr.Error())
	}
	return devices, subnets, warnings
}

// ValidateProbeTarget checks a probe's host and optional ONVIF service URL
// before any connection is made: the host must be an IP or hostname, and the
// service URL http(s) on that same host — never an arbitrary URL.
func ValidateProbeTarget(host, serviceURL string) error {
	if net.ParseIP(host) == nil && !hostnamePattern.MatchString(host) {
		return errors.New("enter the camera's IP address or hostname")
	}
	if serviceURL != "" {
		u, err := url.Parse(serviceURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || !strings.EqualFold(u.Hostname(), host) {
			return errors.New("ONVIF address must be http(s) on the same host")
		}
	}
	return nil
}

// Probe signs in to one camera and returns its details with the recommended
// main (recording) and sub (viewing) stream picks. The caller validates the
// target first (ValidateProbeTarget).
func Probe(ctx context.Context, host, serviceURL, username, password string) (d *Details, mainURL, subURL string, err error) {
	ctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	if serviceURL != "" {
		d, err = FetchDetails(ctx, serviceURL, username, password)
		if err != nil && !errors.Is(err, ErrAuth) {
			// The advertised port may be wrong (NAT, multi-homed); try the usual ones.
			if d2, err2 := FetchByHost(ctx, host, username, password); err2 == nil {
				d, err = d2, nil
			}
		}
	} else {
		d, err = FetchByHost(ctx, host, username, password)
	}
	if err != nil {
		return nil, "", "", err
	}
	main, sub := ChooseStreams(d.Profiles)
	if main != nil {
		mainURL = main.RTSPURL
	}
	if sub != nil {
		subURL = sub.RTSPURL
	}
	return d, mainURL, subURL, nil
}

// SuggestedRTSPURL is the address offered for hand-editing when ONVIF fails.
func SuggestedRTSPURL(host string) string {
	return "rtsp://" + net.JoinHostPort(host, "554") + "/"
}
