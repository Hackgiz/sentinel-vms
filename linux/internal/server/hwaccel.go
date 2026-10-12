package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"sentinel-linux/internal/detect"
	"sentinel-linux/internal/hwinfo"
	"sentinel-linux/internal/link"
)

// Hardware acceleration status and the Auto/Off setting ("hwaccel").

type cameraDecodeView struct {
	detect.CameraDecode
	Name string `json:"name"`
}

type acceleratorView struct {
	hwinfo.Accelerator
	Supported bool   `json:"supported"`
	Note      string `json:"note"`
}

func (s *Server) hwAccelMode(r *http.Request) string {
	if s.Store.Setting(r.Context(), "hwaccel", detect.AccelAuto) == detect.AccelOff {
		return detect.AccelOff
	}
	return detect.AccelAuto
}

func (s *Server) handleGetHWAccel(w http.ResponseWriter, r *http.Request) {
	mode := s.hwAccelMode(r)
	resp := map[string]any{"mode": mode}

	var caps detect.DecodeCaps
	cams := []cameraDecodeView{}
	var hw link.HWAccelStatus
	rid, _ := s.defaultRecorderID(r.Context()) // v1's page shows one server
	_ = s.command(r.Context(), rid, link.CmdHWAccelStatus, nil, &hw)
	if hw.Available {
		var status []detect.CameraDecode
		_ = json.Unmarshal(hw.Decode, &caps)
		_ = json.Unmarshal(hw.Cameras, &status)
		names := map[string]string{}
		if list, err := s.Store.Cameras(r.Context()); err == nil {
			for _, c := range list {
				names[c.ID] = c.Name
			}
		}
		for _, c := range status {
			if n, ok := names[c.CameraID]; ok {
				cams = append(cams, cameraDecodeView{CameraDecode: c, Name: n})
			}
		}
	}
	if caps.Hwaccels == nil {
		caps.Hwaccels, caps.Usable, caps.Notes = []string{}, []string{}, []string{}
		caps.Hardware = hwinfo.Info{RenderNodes: []hwinfo.RenderNode{}, Accelerators: []hwinfo.Accelerator{}}
	}
	resp["decode"] = caps
	resp["cameras"] = cams

	var info detect.DetectorInfo
	if len(hw.Detection) > 0 && string(hw.Detection) != "null" && json.Unmarshal(hw.Detection, &info) == nil {
		resp["detection"] = info
		// The detector's engine is chosen at startup.
		resp["restartRequired"] = (mode == detect.AccelOff && info.Engine != detect.EngineCPU) ||
			(mode == detect.AccelAuto && info.Note == detect.NoteAccelOff)
	} else {
		resp["detection"] = nil
		resp["restartRequired"] = false
	}

	accels := []acceleratorView{}
	for _, a := range caps.Hardware.Accelerators {
		v := acceleratorView{Accelerator: a}
		switch a.Kind {
		case "intel-npu":
			v.Supported = true
			v.Note = "Usable for detection through OpenVINO: set SENTINEL_ORT_LIB to an OpenVINO-enabled ONNX Runtime and SENTINEL_OPENVINO_DEVICE=NPU."
		case "coral-pcie", "coral-usb":
			v.Note = "Not supported yet: Coral needs an Edge TPU-compiled TensorFlow Lite model and libedgetpu. Detection runs on the CPU/GPU instead."
		case "hailo":
			v.Note = "Not supported yet: Hailo needs a model compiled to HEF and HailoRT. Detection runs on the CPU/GPU instead."
		}
		accels = append(accels, v)
	}
	resp["accelerators"] = accels
	writeJSON(w, http.StatusOK, resp)
}

// handlePutHWAccel switches hardware acceleration between auto and off.
// Decode switches immediately; the detection engine on the next restart.
func (s *Server) handlePutHWAccel(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Mode != detect.AccelAuto && in.Mode != detect.AccelOff {
		writeError(w, http.StatusBadRequest, `mode must be "auto" or "off"`)
		return
	}
	old := s.hwAccelMode(r)
	if err := s.Store.SetSetting(r.Context(), "hwaccel", in.Mode); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if old != in.Mode {
		s.audit(r, "System", "Changed hardware acceleration", old+" → "+in.Mode)
		// Sent to the recording server with the configuration.
		if err := s.ApplyMedia(r.Context()); err != nil {
			slog.Error("media apply failed", "err", err)
		}
	}
	s.handleGetHWAccel(w, r)
}
