package detect

// VideoToolbox decode (macOS).
//
// Same design as VAAPI: `-hwaccel videotoolbox` without
// -hwaccel_output_format, so Apple's media engine decodes H.264/HEVC and
// ffmpeg copies each frame back to system memory for the unchanged software
// filter graph. Streams VideoToolbox can't handle make libavcodec fall back
// to software ("Failed setup for format videotoolbox_vld"), which the
// per-camera fallback treats like a VAAPI rejection.

import (
	"context"
	"time"
)

// DecodeVideoToolbox is Apple's hardware decoder (macOS).
const DecodeVideoToolbox = "videotoolbox"

// videoToolboxDevice is what the System page shows as the decode "device".
const videoToolboxDevice = "VideoToolbox"

// VideoToolboxTrialArgs creates a VideoToolbox device and pushes one tiny
// frame through ffmpeg, like VAAPITrialArgs.
func VideoToolboxTrialArgs() []string {
	return []string{"-hide_banner", "-loglevel", "verbose", "-init_hw_device", "videotoolbox=vt",
		"-f", "lavfi", "-i", "nullsrc=s=64x64:d=0.2", "-frames:v", "1", "-f", "null", "-"}
}

// probeVideoToolbox fills caps on macOS (hwaccels already listed).
func probeVideoToolbox(ctx context.Context, ffmpeg string, caps *DecodeCaps, run Runner) {
	has := false
	for _, h := range caps.Hwaccels {
		if h == DecodeVideoToolbox {
			has = true
		}
	}
	if !has {
		caps.Notes = append(caps.Notes, "this ffmpeg was built without VideoToolbox; detection decodes on the CPU (use the ffmpeg bundled with Sentinel, or set SENTINEL_FFMPEG to one that lists videotoolbox in `ffmpeg -hwaccels`)")
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	out, err := run(tctx, ffmpeg, VideoToolboxTrialArgs()...)
	cancel()
	if err != nil {
		caps.Notes = append(caps.Notes, "VideoToolbox didn't open: "+explainFailure(string(out), err))
		return
	}
	caps.Device = videoToolboxDevice
	caps.Driver = caps.Hardware.CPU // e.g. "Apple M2 Pro": the media engine is part of the SoC/T2
	caps.Usable = append(caps.Usable, DecodeVideoToolbox)
}
