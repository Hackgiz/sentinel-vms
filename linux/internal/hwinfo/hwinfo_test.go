package hwinfo

import "testing"

func TestProbeIntelN100(t *testing.T) {
	info := Probe(MapFS{
		Files: map[string]string{
			"/proc/cpuinfo":                           "processor\t: 0\nmodel name\t: Intel(R) N100\n",
			"/dev/dri/card0":                          "",
			"/dev/dri/renderD128":                     "",
			"/sys/class/drm/renderD128/device/vendor": "0x8086\n",
			"/sys/bus/usb/devices/2-1/idVendor":       "18d1\n",
			"/sys/bus/usb/devices/2-1/idProduct":      "9302\n",
			"/sys/bus/usb/devices/1-1/idVendor":       "046d\n",
			"/sys/bus/usb/devices/1-1/idProduct":      "c52b\n",
		},
		Links: map[string]string{"/sys/class/drm/renderD128/device/driver": "../../../bus/pci/drivers/i915"},
	})
	if info.CPU != "Intel(R) N100" || info.Board != "" {
		t.Fatalf("cpu/board: %+v", info)
	}
	if len(info.RenderNodes) != 1 || info.RenderNodes[0] != (RenderNode{Path: "/dev/dri/renderD128", Driver: "i915", Vendor: "Intel"}) {
		t.Fatalf("render nodes: %+v", info.RenderNodes)
	}
	if n := info.VideoNode(); n.Path != "/dev/dri/renderD128" {
		t.Fatalf("video node %+v", n)
	}
	if len(info.Accelerators) != 1 || info.Accelerators[0].Kind != "coral-usb" {
		t.Fatalf("accelerators: %+v", info.Accelerators)
	}
	if info.IsRaspberryPi5() {
		t.Fatal("not a Pi")
	}
}

func TestProbeRaspberryPi5WithHailo(t *testing.T) {
	info := Probe(MapFS{
		Files: map[string]string{
			"/proc/device-tree/model": "Raspberry Pi 5 Model B Rev 1.0\x00",
			"/proc/cpuinfo":           "processor\t: 0\nModel\t\t: Raspberry Pi 5 Model B Rev 1.0\n",
			"/dev/dri/renderD128":     "",
			"/dev/hailo0":             "",
		},
		Links: map[string]string{"/sys/class/drm/renderD128/device/driver": "../../../bus/platform/drivers/v3d"},
	})
	if !info.IsRaspberryPi5() || !info.IsRaspberryPi() || info.Board != "Raspberry Pi 5 Model B Rev 1.0" {
		t.Fatalf("board %q", info.Board)
	}
	if info.RenderNodes[0].Vendor != "Broadcom" {
		t.Fatalf("v3d vendor: %+v", info.RenderNodes)
	}
	if len(info.Accelerators) != 1 || info.Accelerators[0].Kind != "hailo" {
		t.Fatalf("accelerators: %+v", info.Accelerators)
	}
}

func TestVideoNodePrefersIntelOrAMD(t *testing.T) {
	info := Probe(MapFS{
		Files: map[string]string{
			"/dev/dri/renderD128":                     "",
			"/dev/dri/renderD129":                     "",
			"/sys/class/drm/renderD128/device/vendor": "0x10de",
			"/sys/class/drm/renderD129/device/vendor": "0x1002",
			"/dev/accel/accel0":                       "",
			"/dev/apex_0":                             "",
		},
		Links: map[string]string{"/sys/class/accel/accel0/device/driver": "../../../bus/pci/drivers/intel_vpu"},
	})
	if n := info.VideoNode(); n.Path != "/dev/dri/renderD129" || n.Vendor != "AMD" {
		t.Fatalf("video node %+v", n)
	}
	kinds := map[string]bool{}
	for _, a := range info.Accelerators {
		kinds[a.Kind] = true
	}
	if !kinds["intel-npu"] || !kinds["coral-pcie"] {
		t.Fatalf("accelerators %+v", info.Accelerators)
	}
	if (Info{}).VideoNode().Path != "" {
		t.Fatal("no nodes should give empty path")
	}
}
