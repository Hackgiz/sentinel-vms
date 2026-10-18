package server

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"strings"
	"testing"

	"sentinel-linux/internal/store"
)

const (
	zoneSquare   = `[[0.1,0.1],[0.5,0.1],[0.5,0.5],[0.1,0.5]]`
	zoneBowtie   = `[[0.1,0.1],[0.9,0.9],[0.9,0.1],[0.1,0.9]]`
	zoneTriangle = `[[0.6,0.6],[0.9,0.6],[0.75,0.9]]`
)

func zonesURL(id string) string { return "/api/v2/cameras/" + id + "/zones" }

func zonePut(zones ...string) string { return `{"zones":[` + strings.Join(zones, ",") + `]}` }

func TestZonesAPIPermissionsScopeAndValidation(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	body := zonePut(
		fmt.Sprintf(`{"kind":"detect","name":"Drive","points":%s}`, zoneSquare),
		fmt.Sprintf(`{"kind":"mask","name":"Neighbour","points":%s,"enabled":true}`, zoneTriangle),
		fmt.Sprintf(`{"kind":"mask","name":"Off","points":%s,"enabled":false}`, zoneSquare))

	// Permissions: ManageCameras only for PUT; a scoped operator may read.
	if code, _ := v2Call(t, h, "PUT", zonesURL(f.front.ID), f.op, body); code != 403 {
		t.Fatalf("operator PUT: %d", code)
	}
	if code, _ := v2Call(t, h, "PUT", zonesURL(f.front.ID), "", body); code != 401 {
		t.Fatalf("anonymous PUT: %d", code)
	}
	// Scope: the operator sees only Front; Back is a 404 on GET and PUT.
	if code, m := v2Call(t, h, "GET", zonesURL(f.back.ID), f.op, ""); code != 404 || errCode(m) != "not_found" {
		t.Fatalf("out-of-scope GET: %d %v", code, m)
	}
	if code, _ := v2Call(t, h, "PUT", zonesURL(f.back.ID), f.op, body); code != 404 && code != 403 {
		t.Fatalf("out-of-scope PUT: %d", code)
	}
	if code, _ := v2Call(t, h, "GET", zonesURL("nope"), f.admin, ""); code != 404 {
		t.Fatalf("missing camera GET: %d", code)
	}
	if code, _ := v2Call(t, h, "PUT", zonesURL("nope"), f.admin, body); code != 404 {
		t.Fatalf("missing camera PUT: %d", code)
	}

	// Validation (each answers 400 and stores nothing).
	for name, bad := range map[string]string{
		"bowtie":      zonePut(fmt.Sprintf(`{"kind":"mask","points":%s}`, zoneBowtie)),
		"two points":  zonePut(`{"kind":"mask","points":[[0,0],[1,1]]}`),
		"out of 0..1": zonePut(`{"kind":"mask","points":[[0,0],[2,0],[0,1]]}`),
		"bad kind":    zonePut(fmt.Sprintf(`{"kind":"blur","points":%s}`, zoneSquare)),
		"unknown key": `{"zones":[],"extra":1}`,
		"not a list":  `{"zones":{"a":1}}`,
	} {
		if code, m := v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, bad); code != 400 {
			t.Fatalf("%s: %d %v", name, code, m)
		}
	}
	if zs, _ := f.s.Store.CameraZones(context.Background(), f.front.ID); len(zs) != 0 {
		t.Fatalf("rejected PUTs stored %d zones", len(zs))
	}

	// A good PUT stores, returns and audits.
	code, m := v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, body)
	items, _ := m["items"].([]any)
	if code != 200 || len(items) != 3 {
		t.Fatalf("PUT: %d %v", code, m)
	}
	z0 := items[0].(map[string]any)
	if z0["kind"] != "detect" || z0["name"] != "Drive" || z0["enabled"] != true || z0["id"] == "" || len(z0["points"].([]any)) != 4 {
		t.Fatalf("zone 0: %v", z0)
	}
	if items[2].(map[string]any)["enabled"] != false {
		t.Fatalf("enabled:false lost: %v", items[2])
	}
	if got := auditActions(t, f.s, "Cameras"); !got["Set camera zones"] {
		t.Fatalf("audit %v", got)
	}
	// Scoped operator reads them; the other camera's set stays empty.
	if code, m := v2Call(t, h, "GET", zonesURL(f.front.ID), f.op, ""); code != 200 || len(m["items"].([]any)) != 3 {
		t.Fatalf("operator GET: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "GET", zonesURL(f.back.ID), f.admin, ""); code != 200 || len(m["items"].([]any)) != 0 {
		t.Fatalf("back camera zones: %d %v", code, m)
	}

	// PUT replaces the whole set: keep one by id, drop the rest.
	id0 := z0["id"].(string)
	code, m = v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, zonePut(fmt.Sprintf(`{"id":%q,"kind":"detect","name":"Renamed","points":%s}`, id0, zoneSquare)))
	items, _ = m["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["id"] != id0 || items[0].(map[string]any)["name"] != "Renamed" {
		t.Fatalf("replace: %d %v", code, m)
	}
	if code, m := v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, zonePut()); code != 200 || len(m["items"].([]any)) != 0 {
		t.Fatalf("clear: %d %v", code, m)
	}
}

func TestZonesMasksOnCameraPayloadAndRecorderSnapshot(t *testing.T) {
	f := newScopeFixture(t)
	h := f.h
	body := zonePut(
		fmt.Sprintf(`{"kind":"detect","points":%s}`, zoneSquare),
		fmt.Sprintf(`{"kind":"mask","points":%s}`, zoneTriangle),
		fmt.Sprintf(`{"kind":"mask","enabled":false,"points":%s}`, zoneSquare))
	if code, m := v2Call(t, h, "PUT", zonesURL(f.front.ID), f.admin, body); code != 200 {
		t.Fatalf("PUT: %d %v", code, m)
	}
	masks := func(m map[string]any) []any { v, _ := m["masks"].([]any); return v }

	// Only the enabled mask polygon rides on the camera, on single and list.
	code, m := v2Call(t, h, "GET", "/api/v2/cameras/"+f.front.ID, f.admin, "")
	if code != 200 || len(masks(m)) != 1 || len(masks(m)[0].([]any)) != 3 {
		t.Fatalf("camera masks: %d %v", code, m["masks"])
	}
	code, m = v2Call(t, h, "GET", "/api/v2/cameras/"+f.back.ID, f.admin, "")
	if code != 200 || m["masks"] == nil || len(masks(m)) != 0 {
		t.Fatalf("camera without masks must send []: %d %v", code, m["masks"])
	}
	code, m = v2Call(t, h, "GET", "/api/v2/cameras", f.admin, "")
	found := false
	for _, it := range m["items"].([]any) {
		c := it.(map[string]any)
		if c["id"] == f.front.ID {
			found = len(masks(c)) == 1
		}
	}
	if code != 200 || !found {
		t.Fatalf("camera list masks: %d %v", code, m)
	}

	// The recording server's configuration carries the enabled zones only.
	cams, _ := f.s.Store.Cameras(context.Background())
	snap := f.s.snapshot(context.Background(), store.LocalRecorderID, cams)
	seen := false
	for _, c := range snap.Cameras {
		switch c.ID {
		case f.front.ID:
			seen = true
			if len(c.Zones) != 2 || c.Zones[0].Kind != "detect" || c.Zones[1].Kind != "mask" {
				t.Fatalf("front zones in snapshot: %+v", c.Zones)
			}
		case f.back.ID:
			if len(c.Zones) != 0 {
				t.Fatalf("back zones in snapshot: %+v", c.Zones)
			}
		}
	}
	if !seen {
		t.Fatal("front camera not in the local snapshot")
	}
}

func TestMaskCameraJPEG(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var src bytes.Buffer
	_ = jpeg.Encode(&src, img, nil)

	// No masks: untouched bytes.
	if out, ok := f.s.MaskCameraJPEG(ctx, f.front.ID, src.Bytes()); !ok || !bytes.Equal(out, src.Bytes()) {
		t.Fatal("a camera without masks must pass the JPEG through")
	}
	if _, err := f.s.Store.SetCameraZones(ctx, f.front.ID, []store.ZoneInput{
		{Kind: "mask", Points: [][2]float64{{0, 0}, {0.5, 0}, {0.5, 0.5}, {0, 0.5}}, Enabled: true},
		{Kind: "mask", Points: [][2]float64{{0.5, 0.5}, {1, 0.5}, {1, 1}, {0.5, 1}}, Enabled: false}, // disabled: not burned
	}); err != nil {
		t.Fatal(err)
	}
	out, ok := f.s.MaskCameraJPEG(ctx, f.front.ID, src.Bytes())
	if !ok {
		t.Fatal("masking failed")
	}
	dec, _ := jpeg.Decode(bytes.NewReader(out))
	lum := func(x, y int) uint32 { r, _, _, _ := dec.At(x, y).RGBA(); return r >> 8 }
	if lum(20, 20) > 40 || lum(80, 80) < 200 || lum(80, 20) < 200 {
		t.Fatalf("enabled mask black / rest white: %d %d %d", lum(20, 20), lum(80, 80), lum(80, 20))
	}
	// An unreadable image on a masked camera is dropped, not passed on.
	if out, ok := f.s.MaskCameraJPEG(ctx, f.front.ID, []byte("junk")); ok || out != nil {
		t.Fatal("unmaskable image must be dropped")
	}
	// ...but the other camera has no masks, so junk is not its problem.
	if _, ok := f.s.MaskCameraJPEG(ctx, f.back.ID, []byte("junk")); !ok {
		t.Fatal("no masks: pass through")
	}
}
