// fakeonvif runs a pretend ONVIF camera for trying out network discovery
// without real hardware. It answers WS-Discovery on -iface and serves the
// ONVIF SOAP API on -listen, handing out -main/-sub as the stream addresses.
// With -events it also reports a camera-analytics detection (alternately a
// person and a line crossing) at that interval, like a Profile M camera.
//
//	fakeonvif -iface eth0 -listen :18080 -main rtsp://127.0.0.1:28554/test -events 45s
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"sentinel-linux/internal/onvif/fakecam"
)

func main() {
	iface := flag.String("iface", "", "interface to answer WS-Discovery on (default: system default)")
	listen := flag.String("listen", ":18080", "ONVIF HTTP listen address")
	advertise := flag.String("advertise", "", "host[:port] to advertise in discovery replies (default: first IPv4 of -iface)")
	user := flag.String("user", "admin", "ONVIF username")
	pass := flag.String("pass", "camera123", "ONVIF password")
	name := flag.String("name", "Fake Lobby Cam", "camera name in discovery scopes")
	mainURL := flag.String("main", "rtsp://127.0.0.1:28554/test", "main stream RTSP URL")
	subURL := flag.String("sub", "", "optional sub-stream RTSP URL")
	events := flag.Duration("events", 0, "emit a camera-analytics event this often (0 = never)")
	flag.Parse()

	_, port, _ := net.SplitHostPort(*listen)
	host := *advertise
	if host == "" {
		host = firstIPv4(*iface) + ":" + port
	}
	cam := &fakecam.Camera{User: *user, Pass: *pass, Manufacturer: "Sentinel", Model: "FakeCam 1080",
		Streams: []fakecam.Stream{{Token: "main", Name: "MainStream", Encoding: "H264", Width: 1280, Height: 720, FPS: 15, RTSPURL: *mainURL}}}
	if *subURL != "" {
		cam.Streams = append(cam.Streams, fakecam.Stream{Token: "sub", Name: "SubStream", Encoding: "H264", Width: 640, Height: 360, FPS: 15, RTSPURL: *subURL})
	}
	svc := "http://" + host + "/onvif/device_service"
	if _, err := fakecam.AnswerProbes(*iface, svc, *name, "FakeCam 1080", "Sentinel"); err != nil {
		log.Fatal("ws-discovery: ", err)
	}
	log.Printf("fake ONVIF camera %q at %s (user %s)", *name, svc, *user)
	if *events > 0 {
		go func() {
			for i := 0; ; i++ {
				time.Sleep(*events)
				if i%2 == 0 {
					cam.Emit("tns1:RuleEngine/ObjectDetection/Object", "Changed", map[string]string{"Rule": "Lobby"}, map[string]string{"ClassTypes": "Human", "State": "true"})
				} else {
					cam.Emit("tns1:RuleEngine/LineDetector/Crossed", "", map[string]string{"Rule": "Entrance"}, map[string]string{"ClassTypes": "Human"})
				}
				log.Printf("emitted camera-analytics event %d", i+1)
			}
		}()
	}
	log.Fatal(http.ListenAndServe(*listen, cam))
}

func firstIPv4(iface string) string {
	var addrs []net.Addr
	if iface != "" {
		if ifc, err := net.InterfaceByName(iface); err == nil {
			addrs, _ = ifc.Addrs()
		}
	} else {
		addrs, _ = net.InterfaceAddrs()
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
			return ipn.IP.String()
		}
	}
	return strings.TrimSpace("127.0.0.1")
}
