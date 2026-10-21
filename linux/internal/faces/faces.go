// Package faces is face recognition for Person events (WAVE16-DETECTION.md,
// stream C): YuNet finds faces (box, score and five landmarks) in an event
// thumbnail, SFace turns each face, aligned by its landmarks, into a
// unit-length 128-d vector, and two faces of the same person score at least
// MatchThreshold under cosine similarity.
//
// Like appearance search (internal/reid) the models are optional: Open says
// why the engine is unavailable and the server runs as before; tests use a
// fake Engine.
package faces

import (
	"errors"
	"image"
	"math"

	"sentinel-linux/internal/reid"
)

// MatchThreshold is SFace's published cosine-similarity threshold (OpenCV
// FaceRecognizerSF, FR_COSINE): two faces at or above it are the same
// person.
const MatchThreshold = 0.363

// MinFacePx is the smallest face (its box's shorter side, in thumbnail
// pixels) worth embedding: smaller faces don't hold enough detail to tell
// people apart.
const MinFacePx = 40

// Dim is SFace's vector length.
const Dim = 128

// Face is one face found in an image. Box and Landmarks are normalized to
// the image (0..1); landmarks are, in image order, the right eye (the one
// on the image's left), left eye, nose tip, right and left mouth corners.
type Face struct {
	Box       [4]float64 // x1, y1, x2, y2
	Score     float64    // detector confidence
	Landmarks [5][2]float64
}

// MinSidePx is the face box's shorter side in pixels of a w×h image
// (rounded to 1/1000 px, so 0.7−0.5 of 200 px is 40, not 39.99…).
func (f Face) MinSidePx(w, h int) float64 {
	side := min((f.Box[2]-f.Box[0])*float64(w), (f.Box[3]-f.Box[1])*float64(h))
	return math.Round(side*1000) / 1000
}

// Engine is the seam the two models sit behind. Implementations must be
// safe for concurrent use.
type Engine interface {
	// DetectModel and EmbedModel name the models (file SHA-256 prefixes).
	// Vectors from different embedding models are never compared.
	DetectModel() string
	EmbedModel() string
	// Find returns the faces in img, best first.
	Find(img image.Image) ([]Face, error)
	// Embed aligns face (found in img) and returns its L2-normalized vector.
	Embed(img image.Image, face Face) ([]float32, error)
}

// ErrBadVector re-exports reid's: an empty, zero or non-finite model output.
var ErrBadVector = reid.ErrBadVector

// ErrNoFace is an image too small to hold a face.
var ErrNoFace = errors.New("no face")

// Matches reports whether a cosine similarity is a match.
func Matches(sim float64) bool { return sim >= MatchThreshold }

// Enrolled is one gallery face.
type Enrolled struct {
	PersonID string
	Vec      []float32
}

// Best finds the best-matching person for vec among the gallery: the
// person with the highest similarity over any of their faces, if that is a
// match. Vectors of another length are skipped.
func Best(vec []float32, gallery []Enrolled) (personID string, sim float64, ok bool) {
	sim = -1
	for _, g := range gallery {
		if len(g.Vec) != len(vec) {
			continue
		}
		if s := reid.Cosine(vec, g.Vec); s > sim {
			personID, sim = g.PersonID, s
		}
	}
	if personID == "" || !Matches(sim) {
		return "", sim, false
	}
	return personID, sim, true
}
