// Package fillers holds filler definitions for decorative patterns. It's also
// able to:
// - Generate geometric signatures for each tile based on vertex angles.
// - Matches tiles against a library of decorative filler patterns.
package fillers

import (
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/irfansharif/zellij/internal/geom"
)

const dataFilePath = "data/fillers.json"

// Global library. Maps signature strings (e.g., "LCLCL...") to patterns.
var Library = make(map[string][]Pattern)

func init() {
	if err := load(); err != nil {
		log.Fatalf("cannot load fillers: %v", err)
	}
}

// Shape represents a decorative polygon with a color and explicit point
// coordinates. The Path field contains the polygon vertices as geom.Point
// objects.
type Shape struct {
	Colour int          // color index into the palette
	Path   []geom.Point // polygon vertices as explicit points
}

// Pattern represents a collection of shapes with reference bounds for
// alignment. The Bounds field contains reference points used to align the
// pattern to a tile. The first two points form the reference segment for
// alignment.
type Pattern struct {
	Bounds []geom.Point
	Shapes []Shape
}

func load() error {
	type rawShape struct {
		Colour int       `json:"colour"`
		Path   []float64 `json:"path"` // flat [x0,y0,x1,y1,...]
	}

	type rawPattern struct {
		Bounds []float64  `json:"bounds"` // flat [x0,y0,x1,y1,...]
		Shapes []rawShape `json:"shapes"`
	}

	convertFlatToPoints := func(flat []float64) []geom.Point {
		if len(flat)%2 != 0 {
			// (Handle odd-length arrays by truncating the last element.)
			flat = flat[:len(flat)-1]
		}

		points := make([]geom.Point, len(flat)/2)
		for i := 0; i < len(flat); i += 2 {
			points[i/2] = geom.MakePoint(flat[i], flat[i+1])
		}
		return points
	}

	convertRawShape := func(raw rawShape) Shape {
		return Shape{
			Colour: raw.Colour,
			Path:   convertFlatToPoints(raw.Path),
		}
	}

	convertRawPattern := func(raw rawPattern) Pattern {
		shapes := make([]Shape, len(raw.Shapes))
		for i, rawShape := range raw.Shapes {
			shapes[i] = convertRawShape(rawShape)
		}
		return Pattern{
			Bounds: convertFlatToPoints(raw.Bounds),
			Shapes: shapes,
		}
	}

	abs, _ := filepath.Abs(dataFilePath)
	b, err := os.ReadFile(abs)
	if err != nil {
		return err
	}

	var rawLib map[string][]rawPattern
	if err := json.Unmarshal(b, &rawLib); err != nil {
		return err
	}

	for k, rawPatterns := range rawLib {
		patterns := make([]Pattern, len(rawPatterns))
		for i, p := range rawPatterns {
			patterns[i] = convertRawPattern(p)
		}
		Library[k] = patterns
	}

	return nil
}

// Signature computes the geometric signature of a polygon for filler pattern
// matching. It tries all rotational variations of the path to find a matching
// pattern in the library, and returns the specific matching path if found.
//
// The signature is a string where each character represents the turn angle at each vertex:
//   - 'L': Right angle (90°) - sharp corner
//   - 'I': Straight/Inline (180°) - straight edge continuation
//   - 'V': Convex turn (< 180°) - outward bulge
//   - 'C': Concave turn (> 180°) - inward dent
//
// Algorithm:
// For each vertex B with previous vertex A and next vertex C:
// 1. Compute vectors BA and BC (from B to A and B to C)
// 2. Calculate dot product: cos(θ) = (BA • BC) / (|BA| * |BC|)
// 3. Since vectors are normalized by construction, cos(θ) ≈ dot product
// 4. Classify angle based on cos(θ) value:
//   - cos(90°) = 0 → 'L' (right angle)
//   - cos(180°) = -1 → 'I' (straight)
//   - cos(θ) > 0 → 'V' (convex, acute angle)
//   - cos(θ) < 0 → 'C' (concave, obtuse angle)
//
// This signature enables matching tiles to decorative filler patterns that fit
// their geometric structure.
func Signature(path []geom.Point) (string, []geom.Point, bool) {
	if len(path) == 0 {
		return "", nil, false
	}

	// Create working copy of path for rotation.
	alignedPath := make([]geom.Point, len(path))
	copy(alignedPath, path)

	// Try all rotational variations to find a matching pattern.
	for rotation := 0; rotation < len(alignedPath); rotation++ {
		signature := computeSignature(alignedPath)
		if _, exists := Library[signature]; exists {
			return signature, alignedPath, true
		}

		// Rotate path for next iteration.
		alignedPath = append(alignedPath[1:], alignedPath[0])
	}

	return "", nil, false
}

const epsilon = 1e-4

// computeSignature computes the geometric signature of a polygon.
// This is the original signature computation logic extracted into a helper function.
func computeSignature(path []geom.Point) string {
	pathLen := len(path)
	signature := make([]byte, 0, pathLen)

	for i := 0; i < pathLen; i++ {
		// Get three consecutive vertices: previous, current, next.
		prevVertex := path[(i+pathLen-1)%pathLen] // wrap around for cyclic polygon
		currVertex := path[i]
		nextVertex := path[(i+1)%pathLen]

		// Compute vectors from current vertex to adjacent vertices.
		vectorToPrev := prevVertex.Sub(currVertex) // BA vector
		vectorToNext := nextVertex.Sub(currVertex) // BC vector

		// Calculate normalized dot product to determine angle.
		// Positive: acute angle (convex), Negative: obtuse angle (concave)
		lenPrev := math.Sqrt(geom.Dot(vectorToPrev, vectorToPrev))
		lenNext := math.Sqrt(geom.Dot(vectorToNext, vectorToNext))
		cosAngle := 0.0
		if lenPrev > epsilon && lenNext > epsilon {
			cosAngle = geom.Dot(vectorToPrev, vectorToNext) / (lenPrev * lenNext)
			// Clamp to [-1,1] to avoid classification errors from floating
			// point rounding.
			if cosAngle > 1 {
				cosAngle = 1
			} else if cosAngle < -1 {
				cosAngle = -1
			}
		}

		// Classify the angle with small epsilon for floating point comparison.
		switch {
		case math.Abs(cosAngle) < epsilon:
			signature = append(signature, 'L') // cos(90°) ≈ 0: right angle
		case math.Abs(1+cosAngle) < epsilon:
			signature = append(signature, 'I') // cos(180°) ≈ -1: straight line (180° turn)
		case cosAngle > 0:
			signature = append(signature, 'V') // cos(θ) > 0: convex turn (θ < 90°)
		default:
			signature = append(signature, 'C') // cos(θ) < 0: concave turn (θ > 90°)
		}
	}

	return string(signature)
}
