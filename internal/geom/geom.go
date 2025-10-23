// Package geom provides 2D geometric primitives and affine transformations:
// - 2D affine transformations (translation, rotation, scaling)
// - Bounding box operations
// - Point arithmetic and vector operations
// - Transform composition and inversion
package geom

import (
	"fmt"
	"log"
	"math"
)

// Point represents a 2D point or vector in Cartesian coordinates.
type Point struct {
	X float64
	Y float64
}

// Box represents an axis-aligned rectangle.
type Box struct {
	X float64
	Y float64
	W float64
	H float64
}

// Affine represents a 2D affine transform in row-major form:
// [ a b c ]
// [ d e f ]
// where (x', y') = (a*x + b*y + c, d*x + e*y + f)
type Affine struct {
	A float64
	B float64
	C float64
	D float64
	E float64
	F float64
}

func MakePoint(x, y float64) Point               { return Point{X: x, Y: y} }
func MakeBox(x, y, w, h float64) Box             { return Box{X: x, Y: y, W: w, H: h} }
func MakeAffine(a, b, c, d, e, f float64) Affine { return Affine{A: a, B: b, C: c, D: d, E: e, F: f} }

func (p Point) Add(q Point) Point     { return Point{p.X + q.X, p.Y + q.Y} }
func (p Point) Sub(q Point) Point     { return Point{p.X - q.X, p.Y - q.Y} }
func (p Point) Scale(s float64) Point { return Point{p.X * s, p.Y * s} }

func Dot(p, q Point) float64 { return p.X*q.X + p.Y*q.Y }

func Dist(p, q Point) float64 {
	dx := p.X - q.X
	dy := p.Y - q.Y
	return math.Sqrt(dx*dx + dy*dy)
}

// MulPoint applies the affine transform to a point.
func (t Affine) MulPoint(p Point) Point {
	return Point{
		X: t.A*p.X + t.B*p.Y + t.C,
		Y: t.D*p.X + t.E*p.Y + t.F,
	}
}

// Mul composes two affine transforms (applies u then t).
func (t Affine) Mul(u Affine) Affine {
	return MakeAffine(
		t.A*u.A+t.B*u.D,
		t.A*u.B+t.B*u.E,
		t.A*u.C+t.B*u.F+t.C,
		t.D*u.A+t.E*u.D,
		t.D*u.B+t.E*u.E,
		t.D*u.C+t.E*u.F+t.F,
	)
}

// Inv returns the inverse of the affine transform.
// Returns an error if the transform is not invertible (determinant is zero).
func (t Affine) Inv() (Affine, error) {
	det := t.A*t.E - t.B*t.D
	if math.Abs(det) < 1e-10 {
		return Affine{}, fmt.Errorf("affine transform is not invertible (determinant ≈ 0)")
	}
	return MakeAffine(
		t.E/det, -t.B/det, (t.B*t.F-t.C*t.E)/det,
		-t.D/det, t.A/det, (t.C*t.D-t.A*t.F)/det,
	), nil
}

// MatchSeg constructs a transform mapping segment pq to the x-axis unit segment.
// The resulting transform maps p to (0,0) and q to (1,0).
func MatchSeg(p, q Point) Affine {
	return MakeAffine(
		q.X-p.X, p.Y-q.Y, p.X,
		q.Y-p.Y, q.X-p.X, p.Y,
	)
}

// MatchTwoSegs returns the transform T such that T*(p1->q1) == (p2->q2).
func MatchTwoSegs(p1, q1, p2, q2 Point) Affine {
	inv, err := MatchSeg(p1, q1).Inv()
	if err != nil {
		log.Fatalf("degenerate segment in MatchTwoSegs: %v->%v: %v", p1, q1, err)
	}
	return MatchSeg(p2, q2).Mul(inv)
}

// FillBox returns a transform that maps box b1 into b2, optionally allowing a
// 90-degree rotation.
func FillBox(b1, b2 Box, allowRotate bool) Affine {
	if b1.W <= 0 || b1.H <= 0 {
		log.Fatalf("source box must have positive width and height, got W=%v H=%v", b1.W, b1.H)
	}
	if b2.W <= 0 || b2.H <= 0 {
		log.Fatalf("destination box must have positive width and height, got W=%v H=%v", b2.W, b2.H)
	}

	sc := math.Min(b2.W/b1.W, b2.H/b1.H)
	rsc := math.Min(b2.W/b1.H, b2.H/b1.W)
	centerDst := MakeAffine(1, 0, b2.X+0.5*b2.W, 0, 1, b2.Y+0.5*b2.H)
	centerSrc := MakeAffine(1, 0, -(b1.X + 0.5*b1.W), 0, 1, -(b1.Y + 0.5*b1.H))
	if !allowRotate || sc > rsc {
		return centerDst.Mul(MakeAffine(sc, 0, 0, 0, sc, 0)).Mul(centerSrc)
	}
	rot := MakeAffine(0, -1, 0, 1, 0, 0)
	return centerDst.Mul(MakeAffine(rsc, 0, 0, 0, rsc, 0)).Mul(rot).Mul(centerSrc)
}
