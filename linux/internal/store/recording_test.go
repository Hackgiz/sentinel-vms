package store

import (
	"context"
	"testing"

	"sentinel-linux/internal/link"
)

func TestRecordingModeMigrationAndCompat(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	on, err := st.SaveCamera(ctx, "", CameraInput{Name: "On", RTSPURL: "rtsp://h/1", Recording: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := st.SaveCamera(ctx, "", CameraInput{Name: "Off", RTSPURL: "rtsp://h/2"})
	if err != nil {
		t.Fatal(err)
	}
	if on.RecordingMode != link.RecordDefault || on.EffectiveRecordingMode != link.RecordContinuous || !on.Recording ||
		off.RecordingMode != link.RecordOff || off.Recording || on.RecordTrigger != link.TriggerDefault {
		t.Fatalf("new cameras: %+v / %+v", on, off)
	}
	// Rows from before wave 22 have no mode: reads derive it, Open migrates.
	if _, err := st.DB.Exec(`UPDATE cameras SET recording_mode = ''`); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Camera(ctx, off.ID); c.RecordingMode != link.RecordOff {
		t.Fatalf("legacy read: %+v", c)
	}
	st.Close()
	for i := 0; i < 2; i++ { // idempotent
		if st, err = Open(dir); err != nil {
			t.Fatal(err)
		}
		modes := map[string]string{}
		rows, _ := st.DB.Query(`SELECT id, recording_mode FROM cameras`)
		for rows.Next() {
			var id, m string
			_ = rows.Scan(&id, &m)
			modes[id] = m
		}
		rows.Close()
		if modes[on.ID] != link.RecordDefault || modes[off.ID] != link.RecordOff {
			t.Fatalf("migrated modes %v", modes)
		}
		if i == 0 {
			st.Close()
		}
	}
	defer st.Close()

	// Recording switch without a mode: on keeps a non-off mode, off sets off.
	events := link.RecordEvents
	in := CameraInput{Name: "On", RTSPURL: "rtsp://h/1", Recording: true, RecordingMode: &events}
	c, err := st.SaveCamera(ctx, on.ID, in)
	if err != nil || c.RecordingMode != link.RecordEvents || c.EffectiveRecordingMode != link.RecordEvents {
		t.Fatalf("set events: %+v %v", c, err)
	}
	in.RecordingMode = nil
	if c, _ = st.SaveCamera(ctx, on.ID, in); c.RecordingMode != link.RecordEvents {
		t.Fatalf("recording=true reset the mode: %+v", c)
	}
	in.Recording = false
	if c, _ = st.SaveCamera(ctx, on.ID, in); c.RecordingMode != link.RecordOff || c.Recording {
		t.Fatalf("recording=false: %+v", c)
	}
	in.Recording = true
	if c, _ = st.SaveCamera(ctx, on.ID, in); c.RecordingMode != link.RecordDefault || !c.Recording {
		t.Fatalf("recording=true from off: %+v", c)
	}

	// Server default dual: a camera without a sub-stream does events.
	d := st.RecordingDefaults(ctx)
	if d != link.DefaultRecordingSettings() {
		t.Fatalf("defaults %+v", d)
	}
	d.DefaultMode = link.RecordDual
	if err := st.SetRecordingDefaults(ctx, d); err != nil {
		t.Fatal(err)
	}
	if c, _ = st.Camera(ctx, on.ID); c.EffectiveRecordingMode != link.RecordEvents {
		t.Fatalf("dual without sub: %+v", c)
	}
	in.SubRTSPURL = "rtsp://h/1s"
	if c, _ = st.SaveCamera(ctx, on.ID, in); c.EffectiveRecordingMode != link.RecordDual {
		t.Fatalf("dual with sub: %+v", c)
	}
	// Defaults off: "default" cameras report recording=false.
	d.DefaultMode = link.RecordOff
	_ = st.SetRecordingDefaults(ctx, d)
	if c, _ = st.Camera(ctx, on.ID); c.Recording || c.EffectiveRecordingMode != link.RecordOff {
		t.Fatalf("default off: %+v", c)
	}
}
