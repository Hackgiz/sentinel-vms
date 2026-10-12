// Package hwinfo reports the acceleration hardware a Linux box has: GPU render
// nodes (VAAPI/QSV decode, OpenVINO), Intel NPUs, Coral Edge TPUs and Hailo
// accelerators, plus the board model (Raspberry Pi 5 has no H.264 decoder).
//
// Everything is read through FS so tests can fake /dev, /sys and /proc.
// Presence only: whether a device is usable is decided by trying it.
package hwinfo

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS is the slice of the filesystem Probe reads.
type FS interface {
	ReadFile(name string) ([]byte, error)
	Glob(pattern string) ([]string, error)
	Readlink(name string) (string, error)
}

// OS reads the real filesystem.
type OS struct{}

func (OS) ReadFile(name string) ([]byte, error)  { return os.ReadFile(name) }
func (OS) Glob(pattern string) ([]string, error) { return filepath.Glob(pattern) }
func (OS) Readlink(name string) (string, error)  { return os.Readlink(name) }

// RenderNode is one /dev/dri/renderD* GPU node.
type RenderNode struct {
	Path   string `json:"path"`   // /dev/dri/renderD128
	Driver string `json:"driver"` // kernel driver: i915, xe, amdgpu, v3d, vc4, nouveau …
	Vendor string `json:"vendor"` // Intel | AMD | NVIDIA | Broadcom | "" (unknown)
}

// Accelerator is an AI accelerator device that is physically present.
type Accelerator struct {
	Kind string `json:"kind"` // intel-npu | coral-pcie | coral-usb | hailo
	Name string `json:"name"`
	Path string `json:"path"`
}

type Info struct {
	OS           string        `json:"os,omitempty"`    // runtime.GOOS, set by ProbeHost ("" in tests = Linux)
	Board        string        `json:"board,omitempty"` // device-tree model, e.g. "Raspberry Pi 5 Model B Rev 1.0"
	CPU          string        `json:"cpu,omitempty"`
	RenderNodes  []RenderNode  `json:"renderNodes"`
	Accelerators []Accelerator `json:"accelerators"`
}

var pciVendors = map[string]string{"0x8086": "Intel", "0x1002": "AMD", "0x10de": "NVIDIA"}

var driverVendors = map[string]string{
	"i915": "Intel", "xe": "Intel", "amdgpu": "AMD", "radeon": "AMD",
	"nvidia": "NVIDIA", "nouveau": "NVIDIA", "v3d": "Broadcom", "vc4": "Broadcom",
}

// Probe inspects fsys (use OS{} for the real machine).
func Probe(fsys FS) Info {
	info := Info{RenderNodes: []RenderNode{}, Accelerators: []Accelerator{}}
	for _, p := range []string{"/proc/device-tree/model", "/sys/firmware/devicetree/base/model"} {
		if b, err := fsys.ReadFile(p); err == nil {
			info.Board = strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
			break
		}
	}
	if b, err := fsys.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok && (strings.TrimSpace(k) == "model name" || (info.CPU == "" && strings.TrimSpace(k) == "Model")) {
				info.CPU = strings.TrimSpace(v)
				if strings.TrimSpace(k) == "model name" {
					break
				}
			}
		}
	}

	nodes, _ := fsys.Glob("/dev/dri/renderD*")
	sort.Strings(nodes)
	for _, n := range nodes {
		rn := RenderNode{Path: n}
		sys := "/sys/class/drm/" + filepath.Base(n) + "/device"
		if l, err := fsys.Readlink(sys + "/driver"); err == nil {
			rn.Driver = filepath.Base(l)
		}
		if b, err := fsys.ReadFile(sys + "/vendor"); err == nil {
			rn.Vendor = pciVendors[strings.TrimSpace(string(b))]
		}
		if rn.Vendor == "" {
			rn.Vendor = driverVendors[rn.Driver]
		}
		info.RenderNodes = append(info.RenderNodes, rn)
	}

	// Intel NPU (Core Ultra): /dev/accel/accel* bound to intel_vpu.
	accels, _ := fsys.Glob("/dev/accel/accel*")
	for _, a := range accels {
		if l, err := fsys.Readlink("/sys/class/accel/" + filepath.Base(a) + "/device/driver"); err == nil && filepath.Base(l) == "intel_vpu" {
			info.Accelerators = append(info.Accelerators, Accelerator{Kind: "intel-npu", Name: "Intel NPU", Path: a})
		}
	}
	// Coral M.2 / PCIe (gasket/apex driver).
	apex, _ := fsys.Glob("/dev/apex_*")
	for _, a := range apex {
		info.Accelerators = append(info.Accelerators, Accelerator{Kind: "coral-pcie", Name: "Coral Edge TPU (PCIe)", Path: a})
	}
	// Coral USB: 1a6e:089a before its firmware is loaded, 18d1:9302 after.
	usb, _ := fsys.Glob("/sys/bus/usb/devices/*/idVendor")
	sort.Strings(usb)
	for _, v := range usb {
		dir := filepath.Dir(v)
		vendor, _ := fsys.ReadFile(v)
		product, _ := fsys.ReadFile(dir + "/idProduct")
		id := strings.TrimSpace(string(vendor)) + ":" + strings.TrimSpace(string(product))
		if id == "1a6e:089a" || id == "18d1:9302" {
			info.Accelerators = append(info.Accelerators, Accelerator{Kind: "coral-usb", Name: "Coral USB Accelerator", Path: dir})
		}
	}
	// Hailo-8 / Hailo-8L (Raspberry Pi AI Kit / AI HAT+), hailo_pci driver.
	hailo, _ := fsys.Glob("/dev/hailo*")
	for _, h := range hailo {
		info.Accelerators = append(info.Accelerators, Accelerator{Kind: "hailo", Name: "Hailo AI accelerator", Path: h})
	}
	return info
}

// IsRaspberryPi5 reports a Pi 5 (no H.264 hardware decoder; its HEVC decoder
// needs the Raspberry Pi fork of ffmpeg).
func (i Info) IsRaspberryPi5() bool { return strings.Contains(i.Board, "Raspberry Pi 5") }

// IsRaspberryPi reports any Raspberry Pi board.
func (i Info) IsRaspberryPi() bool { return strings.Contains(i.Board, "Raspberry Pi") }

// VideoNode picks the render node to decode on: the first Intel or AMD GPU
// (VAAPI drivers exist for those), else the first node, else "".
func (i Info) VideoNode() RenderNode {
	for _, n := range i.RenderNodes {
		if n.Vendor == "Intel" || n.Vendor == "AMD" {
			return n
		}
	}
	if len(i.RenderNodes) > 0 {
		return i.RenderNodes[0]
	}
	return RenderNode{}
}

// MapFS is an in-memory FS for tests: Files maps paths to contents, Links
// maps symlink paths to targets. Glob matches with path.Match semantics.
type MapFS struct {
	Files map[string]string
	Links map[string]string
}

func (m MapFS) ReadFile(name string) ([]byte, error) {
	if s, ok := m.Files[name]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (m MapFS) Readlink(name string) (string, error) {
	if s, ok := m.Links[name]; ok {
		return s, nil
	}
	return "", fs.ErrNotExist
}

func (m MapFS) Glob(pattern string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, set := range []map[string]string{m.Files, m.Links} {
		for p := range set {
			if ok, _ := filepath.Match(pattern, p); ok && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
