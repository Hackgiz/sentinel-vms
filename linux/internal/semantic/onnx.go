package semantic

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"sentinel-linux/internal/ort"

	"sentinel-linux/internal/modelpath"
)

// Environment overrides for the model files; without them the files are
// read from <data>/models under the catalog names.
const (
	ImageModelEnv = "SENTINEL_CLIP_IMAGE_MODEL"
	TextModelEnv  = "SENTINEL_CLIP_TEXT_MODEL"
	VocabEnv      = "SENTINEL_CLIP_VOCAB"
	MergesEnv     = "SENTINEL_CLIP_MERGES"
)

// Catalog file names under <data>/models.
const (
	ImageModelFile = "clip_image.onnx"
	TextModelFile  = "clip_text.onnx"
	VocabFile      = "clip_vocab.json"
	MergesFile     = "clip_merges.txt"
)

// Files are where the four CLIP files are expected.
type Files struct {
	Image, Text, Vocab, Merges string
}

// Paths resolves the files from the environment or <data>/models.
func Paths(dataDir string) Files {
	pick := func(env, name string) string {
		if p := os.Getenv(env); p != "" {
			return p
		}
		return modelpath.Find(dataDir, name)
	}
	return Files{
		Image:  pick(ImageModelEnv, ImageModelFile),
		Text:   pick(TextModelEnv, TextModelFile),
		Vocab:  pick(VocabEnv, VocabFile),
		Merges: pick(MergesEnv, MergesFile),
	}
}

// Missing names the first file that is not there, or "".
func (f Files) Missing() (what, path string) {
	for _, c := range []struct{ what, path string }{
		{"image model", f.Image}, {"text model", f.Text}, {"tokenizer vocabulary", f.Vocab}, {"tokenizer merges", f.Merges},
	} {
		if fi, err := os.Stat(c.path); err != nil || !fi.Mode().IsRegular() {
			return c.what, c.path
		}
	}
	return "", ""
}

// ModelID is the model's identity in event_semantic.model: the first 16
// hex digits of the image model file's SHA-256, so replacing the file
// invalidates every stored vector.
func ModelID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// Xenova/clip-vit-base-patch32 exports: vision_model.onnx takes
// pixel_values [batch, 3, 224, 224] and gives image_embeds [batch, 512]
// (after the visual projection); text_model.onnx takes input_ids
// [batch, sequence] (int64, no attention_mask) and gives text_embeds
// [batch, 512]. Each has that one input and output.

type onnxImage struct{ sess *ort.Session }

func (o *onnxImage) Run(px []float32) ([]float32, error) {
	out, _, err := o.sess.Run(px, []int64{1, 3, ImageSize, ImageSize})
	return out, err
}

type onnxText struct{ sess *ort.Session }

func (o *onnxText) Run(ids []int64) ([]float32, error) {
	out, _, err := o.sess.RunInt64(ids, []int64{1, int64(len(ids))})
	return out, err
}

// Open loads the optional CLIP models. It returns (nil, reason) when
// semantic search is unavailable (a file missing, no ONNX Runtime, a model
// that fails to load or run); the caller surfaces the reason. It never
// panics on a bad model.
func Open(dataDir, ortLib string) (e Engine, reason string) {
	var closers []func()
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}
	defer func() {
		if r := recover(); r != nil {
			closeAll()
			e, reason = nil, fmt.Sprintf("semantic search models failed to start: %v", r)
		}
	}()
	files := Paths(dataDir)
	if what, path := files.Missing(); what != "" {
		return nil, "no semantic search " + what + " installed (" + path + ")"
	}
	if ortLib == "" {
		return nil, "ONNX Runtime library not found"
	}
	tok, err := LoadTokenizer(files.Vocab, files.Merges)
	if err != nil {
		return nil, "semantic search tokenizer files could not be read: " + err.Error()
	}
	id, err := ModelID(files.Image)
	if err != nil {
		return nil, "semantic image model could not be read: " + err.Error()
	}
	rt, err := ort.Load(ortLib)
	if err != nil {
		return nil, "ONNX Runtime unavailable: " + err.Error()
	}
	img, err := rt.NewSession(files.Image, 2)
	if err != nil {
		return nil, "semantic image model could not be loaded: " + err.Error()
	}
	closers = append(closers, img.Close)
	txt, err := rt.NewSession(files.Text, 1)
	if err != nil {
		closeAll()
		return nil, "semantic text model could not be loaded: " + err.Error()
	}
	closers = append(closers, txt.Close)
	// The first output must be the projected embedding: an export whose
	// first output is the transformer's hidden state or pooled output (no
	// projection) can't be compared across the two towers.
	for _, t := range []struct {
		name string
		sess *ort.Session
	}{{"image", img}, {"text", txt}} {
		if _, out := t.sess.IONames(); out == "last_hidden_state" || out == "pooler_output" {
			closeAll()
			return nil, fmt.Sprintf("semantic %s model's first output is %s; use an export that ends with the projection (%s_embeds)", t.name, out, t.name)
		}
	}
	return Verify(&onnxImage{img}, &onnxText{txt}, tok, id, closeAll)
}
