package store

import (
	"context"
	"testing"
)

func TestDecodeAudio(t *testing.T) {
	for _, c := range []struct {
		detect, thr int
		on          bool
		db          int
	}{{0, 0, false, 25}, {1, 0, true, 25}, {1, 30, true, 30}, {0, 40, false, 40}} {
		if on, db := DecodeAudio(c.detect, c.thr); on != c.on || db != c.db {
			t.Errorf("decode(%d,%d) = %v,%d", c.detect, c.thr, on, db)
		}
	}
}

// A wave-8 database packed the threshold into audio_detect (+N on at N dB,
// -N off remembering N). Opening the store moves it to audio_threshold_db.
func TestAudioThresholdMigratesLegacyRows(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, name := range []string{"on30", "off40", "default", "on25"} {
		c, err := s.SaveCamera(ctx, "", CameraInput{Name: name, RTSPURL: "rtsp://10.0.0.5/" + name})
		if err != nil {
			t.Fatal(err)
		}
		ids[name] = c.ID
	}
	legacy := map[string]int{"on30": 30, "off40": -40, "default": 0, "on25": 25}
	for name, raw := range legacy {
		if _, err := s.DB.Exec(`UPDATE cameras SET audio_detect = ?, audio_threshold_db = 0 WHERE id = ?`, raw, ids[name]); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	type want struct {
		detect, thr int
		on          bool
		db          int
	}
	wants := map[string]want{
		"on30":    {1, 30, true, 30},
		"off40":   {0, 40, false, 40},
		"default": {0, 0, false, 25},
		"on25":    {1, 25, true, 25},
	}
	for round := 1; round <= 2; round++ { // the second open must change nothing
		s, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		for name, w := range wants {
			var detect, thr int
			if err := s.DB.QueryRow(`SELECT audio_detect, audio_threshold_db FROM cameras WHERE id = ?`, ids[name]).Scan(&detect, &thr); err != nil {
				t.Fatal(err)
			}
			if detect != w.detect || thr != w.thr {
				t.Errorf("round %d %s: columns %d,%d want %d,%d", round, name, detect, thr, w.detect, w.thr)
			}
			c, err := s.Camera(ctx, ids[name])
			if err != nil || c.AudioDetect != w.on || c.AudioThresholdDB != w.db {
				t.Errorf("round %d %s: camera %v,%d %v want %v,%d", round, name, c.AudioDetect, c.AudioThresholdDB, err, w.on, w.db)
			}
		}
		if round == 1 {
			// Switching the migrated off camera back on keeps its threshold.
			yes := true
			if on, db, err := s.SetCameraAudio(ctx, ids["off40"], &yes, nil); err != nil || !on || db != 40 {
				t.Fatalf("re-enable migrated: %v %d %v", on, db, err)
			}
			no := false
			if _, _, err := s.SetCameraAudio(ctx, ids["off40"], &no, nil); err != nil {
				t.Fatal(err)
			}
		}
		s.Close()
	}
}

func TestSetCameraAudio(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	cam, err := s.SaveCamera(ctx, "", CameraInput{Name: "Door", RTSPURL: "rtsp://10.0.0.5/s"})
	if err != nil {
		t.Fatal(err)
	}
	if cam.AudioDetect || cam.AudioThresholdDB != AudioDefaultThresholdDB {
		t.Fatalf("new camera: %+v", cam)
	}
	yes, thr, bad := true, 35, 3
	if _, _, err := s.SetCameraAudio(ctx, cam.ID, &yes, &bad); err == nil {
		t.Fatal("threshold 3 accepted")
	}
	if on, db, err := s.SetCameraAudio(ctx, cam.ID, &yes, &thr); err != nil || !on || db != 35 {
		t.Fatalf("enable: %v %d %v", on, db, err)
	}
	// Editing the camera elsewhere leaves audio alone.
	if _, err := s.SaveCamera(ctx, cam.ID, CameraInput{Name: "Door2", RTSPURL: "rtsp://10.0.0.5/s"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Camera(ctx, cam.ID)
	if !got.AudioDetect || got.AudioThresholdDB != 35 {
		t.Fatalf("after edit: %+v", got)
	}
	no := false
	if on, db, err := s.SetCameraAudio(ctx, cam.ID, &no, nil); err != nil || on || db != 35 {
		t.Fatalf("disable: %v %d %v", on, db, err)
	}
	if on, db, _ := s.SetCameraAudio(ctx, cam.ID, &yes, nil); !on || db != 35 {
		t.Fatalf("threshold not remembered: %v %d", on, db)
	}
	if _, _, err := s.SetCameraAudio(ctx, "nope", &yes, nil); err != ErrNotFound {
		t.Fatalf("missing camera: %v", err)
	}
}
