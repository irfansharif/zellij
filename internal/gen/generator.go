// Package gen implements the core procedural generation algorithm based on
// https://isohedral.ca/generative-zellij/.
//
// The algorithm works in several stages:
//  - Create a random set of lines in 4 orientations (horizontal, vertical,
//    ±45°) on an integer lattice.
//  - Find all intersection points where 2+ lines meet.
//  - Extract polygonal tiles by tracing edges around each intersection.
//  - Merge tiles in designated "focus" regions to create larger shapes.
//
// The generator produces abstract tile compositions in unit grid coordinates
// that can be mapped to screen space and filled with decorative patterns by the
// render package.
package gen

import (
	"math"
	"math/rand"

	"github.com/irfansharif/zellij/internal/geom"
)

// Features represents the configuration for the generator.
type Features struct {
	LineDensity int
	NumLines    int
	GridSide    int
	Focus       string // None, Eight, Sixteen
	Shimmer     int    // -1 or >=2
}

// Composition carries the generated tiles and boundary and mapping info.
type Composition struct {
	Tiles    []Tile // polygons in unit grid coordinates
	Boundary []geom.Point
	GridSide int
	Shimmer  int
}

// 
type Tile struct {
	Vertex geom.Point
	Path   []geom.Point
}

// Generator implements the pattern generation algorithm.
type Generator struct {
	Features Features
}

func NewGenerator() *Generator {
	return &Generator{}
}

// initFeatures initializes the features of the generator.
func (g *Generator) initFeatures(rng *rand.Rand) {
	v := rng.Float64()
	if v < 0.7 {
		g.Features.LineDensity = 10
		g.Features.NumLines = 25
	} else if v < 0.9 {
		g.Features.LineDensity = 6
		g.Features.NumLines = 9
	} else {
		g.Features.LineDensity = 20
		g.Features.NumLines = 40
	}

	v = rng.Float64()
	if v < 0.75 {
		g.Features.Focus = "None"
	} else if v < 0.95 {
		g.Features.Focus = "Eight"
	} else {
		g.Features.Focus = "Sixteen"
	}

	v = rng.Float64()
	if v < 0.75 {
		g.Features.Shimmer = -1
	} else {
		g.Features.Shimmer = int(rng.Float64()*3) + 2
	}

	g.Features.GridSide = 2*g.Features.LineDensity + 1
}

// SetFeaturesForComplexity sets features based on the given complexity level.
// If complexity is nil, uses default randomization.
func (g *Generator) SetFeaturesForComplexity(rng *rand.Rand, complexity *int) {
	if complexity == nil {
		g.initFeatures(rng)
		return
	}

	// Set NumLines based on complexity
	g.Features.NumLines = *complexity

	// Set LineDensity based on complexity
	if *complexity <= 10 {
		g.Features.LineDensity = 6
	} else if *complexity <= 30 {
		g.Features.LineDensity = 12
	} else {
		g.Features.LineDensity = 20
	}

	// Set Focus based on complexity
	if *complexity <= 5 {
		g.Features.Focus = "None"
	} else if *complexity <= 20 {
		g.Features.Focus = "Eight"
	} else {
		g.Features.Focus = "Sixteen"
	}

	// Set Shimmer based on complexity
	if *complexity <= 10 {
		g.Features.Shimmer = -1
	} else {
		g.Features.Shimmer = min(4, 2+(*complexity-10)/10) // increases with complexity
	}

	g.Features.GridSide = 2*g.Features.LineDensity + 1
}

// Generate creates a new procedural Islamic geometric pattern composition.
//
// The generation process:
//  1. Randomly select generation parameters (line density, focus mode, etc.)
//  2. Create random lines in 4 orientations (horizontal, vertical, ±45°)
//  3. Mark grid cells where lines pass through
//  4. Find all intersection points (where 2+ lines meet)
//  5. Extract polygonal tiles by tracing edges around each intersection
//  6. Merge tiles in designated "focus" regions to create visual focal points
func (g *Generator) Generate(seed int64, complexity *int) Composition {
	rng := rand.New(rand.NewSource(seed))
	g.SetFeaturesForComplexity(rng, complexity)

	lines, groups := g.createLines(g.Features.NumLines, rng)
	grid := g.buildGrid(lines)
	tiles, boundary := getAllTiles(grid)
	tiles = g.mergeGroups(tiles, grid, groups)

	return Composition{
		Tiles:    tiles,
		Boundary: boundary,
		GridSide: g.Features.GridSide,
		Shimmer:  g.Features.Shimmer,
	}
}

// buildGrid creates and populates a grid with lines
func (g *Generator) buildGrid(lines []line) *Grid {
	grid := newGrid(g.Features.GridSide)
	grid.markLines(lines)
	return grid
}

// mergeGroups mirrors JS buildDesign group handling
func (g *Generator) mergeGroups(tiles []Tile, grid *Grid, groups [][]geom.Point) []Tile {
	for idx := 0; idx < len(groups); idx++ {
		for _, pt := range groups[idx] {
			grid.setGroup(pt, idx)
		}
		var grouptiles [][]geom.Point
		for tidx := len(tiles) - 1; tidx >= 0; tidx-- {
			t := tiles[tidx]
			if grid.getGroup(t.Vertex) == idx {
				grouptiles = append(grouptiles, t.Path)
				tiles = append(tiles[:tidx], tiles[tidx+1:]...)
			}
		}
		newtile := groupTiles(grouptiles)
		tiles = append(tiles, Tile{Vertex: geom.MakePoint(0, 0), Path: newtile})
	}
	return tiles
}

// Below: a subset port of JS functions. Implementation is verbose for clarity.

type line struct{ pos, dir geom.Point }

type Grid struct {
	Side  int
	cells []cell
}

type cell struct {
	users []line
	drawn bool
	group int
}

func newGrid(side int) *Grid {
	cells := make([]cell, side*side)
	for i := range cells {
		cells[i].group = -1
	}
	return &Grid{Side: side, cells: cells}
}

func (g *Grid) idx(p geom.Point) int { return int(p.Y)*g.Side + int(p.X) }
func (g *Grid) in(p geom.Point) bool {
	return p.X >= 0 && p.Y >= 0 && int(p.X) < g.Side && int(p.Y) < g.Side
}
func (g *Grid) getCell(p geom.Point) *cell    { return &g.cells[g.idx(p)] }
func (g *Grid) addUser(p geom.Point, l line)  { c := g.getCell(p); c.users = append(c.users, l) }
func (g *Grid) numUsers(p geom.Point) int     { return len(g.getCell(p).users) }
func (g *Grid) isDrawn(p geom.Point) bool     { return g.getCell(p).drawn }
func (g *Grid) setDrawn(p geom.Point)         { g.getCell(p).drawn = true }
func (g *Grid) getGroup(p geom.Point) int     { return g.getCell(p).group }
func (g *Grid) setGroup(p geom.Point, id int) { g.getCell(p).group = id }

func (g *Grid) markRay(l line, pos, dir geom.Point) {
	for g.in(pos) {
		g.addUser(pos, l)
		pos = pos.Add(dir)
	}
}
func (g *Grid) markLine(l line) {
	g.markRay(l, l.pos, l.dir)
	g.markRay(l, l.pos.Sub(l.dir), l.dir.Scale(-1))
}
func (g *Grid) markLines(lines []line) {
	for _, l := range lines {
		g.markLine(l)
	}
}

var orderedDirs = []int{5, 2, 1, 0, 3, 6, 7, 8}
var r22 = math.Sqrt(2.0) * 0.5
var intDirVecs = []geom.Point{
	{X: -1, Y: -1}, {X: 0, Y: -1}, {X: 1, Y: -1},
	{X: -1, Y: 0}, {X: 0, Y: 0}, {X: 1, Y: 0},
	{X: -1, Y: 1}, {X: 0, Y: 1}, {X: 1, Y: 1},
}
var dirVecs = []geom.Point{
	{X: -r22, Y: -r22}, {X: 0, Y: -1}, {X: r22, Y: -r22},
	{X: -1, Y: 0}, {X: 0, Y: 0}, {X: 1, Y: 0},
	{X: -r22, Y: r22}, {X: 0, Y: 1}, {X: r22, Y: r22},
}

func (g *Grid) findNeighbour(pt geom.Point, dir geom.Point) *geom.Point {
	p := pt.Add(dir)
	for g.in(p) {
		if g.numUsers(p) > 1 {
			return &p
		}
		p = p.Add(dir)
	}
	return nil
}

// stackItem represents an item in the tile extraction stack
type stackItem struct {
	pos *geom.Point // intersection point
	ap  *geom.Point // alignment point on previous tile
	aq  *geom.Point // alignment point on previous tile
}

// getAllTiles is an EXACT port of the JS getAllTiles function.
// Variable names (L, B, spt, us, pts, align_p, align_q, used_dirs, etc.)
// mirror the original JavaScript to ensure identical behavior.
func getAllTiles(grid *Grid) ([]Tile, []geom.Point) {
	var L []Tile
	var B []geom.Point
	var spt *geom.Point
	for y := 0; y < grid.Side && spt == nil; y++ {
		for x := 0; x < grid.Side; x++ {
			pt := geom.MakePoint(float64(x), float64(y))
			if grid.numUsers(pt) >= 2 {
				spt = &pt
				break
			}
		}
	}
	if spt == nil {
		return nil, nil
	}
	stack := []stackItem{{pos: spt, ap: nil, aq: nil}}

	for len(stack) > 0 {
		// pop
		a := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		pt := *a.pos

		if grid.isDrawn(pt) {
			continue
		}

		align_p := a.ap
		align_q := a.aq

		us := grid.getCell(pt).users
		var pts []geom.Point

		grid.setDrawn(pt)

		used_dirs := make(map[int]bool)

		for _, l := range us {
			d := l.dir
			used_dirs[int((d.Y+1)*3+(d.X+1))] = true
			used_dirs[int((-d.Y+1)*3+(-d.X+1))] = true
		}

		// First, compute the polygon we want to draw.
		last := geom.MakePoint(0, 0)
		for _, d := range orderedDirs {
			if used_dirs[d] {
				ddir := dirVecs[d]
				ppdir := geom.MakePoint(-ddir.Y, ddir.X)
				npt := last.Add(ppdir)
				pts = append(pts, last)
				last = npt
			}
		}

		// Now, figure out the translation vector we're going to use
		// for this polygon.  Find the edge whose vector matches
		// delt.
		translation := geom.MakePoint(0, 0)
		if align_p != nil {
			delt := align_p.Sub(*align_q)
			for idx := 0; idx < len(pts); idx++ {
				v := pts[(idx+1)%len(pts)].Sub(pts[idx])
				if geom.Dist(v, delt) < 1e-5 {
					translation = align_q.Sub(pts[idx])
					break
				}
			}
		}

		// Rewrite the points according to the translation.
		for idx := 0; idx < len(pts); idx++ {
			pts[idx] = pts[idx].Add(translation)
		}

		L = append(L, Tile{Vertex: pt, Path: pts})

		// Finally, recursively walk to your neighbours and tell them to
		// draw as well.
		vidx := 0
		for _, d := range orderedDirs {
			if used_dirs[d] {
				neigh := grid.findNeighbour(pt, intDirVecs[d])
				if neigh != nil {
					if !grid.isDrawn(*neigh) {
						stack = append(stack, stackItem{pos: neigh, ap: &pts[vidx], aq: &pts[(vidx+1)%len(pts)]})
					}
				} else {
					// No neighbour, so these points are part of the boundary.
					B = append(B, pts[vidx])
					B = append(B, pts[(vidx+1)%len(pts)])
				}
				vidx = vidx + 1
			}
		}
	}

	return L, B
}

// createLines mirrors JS createLines structure
func (g *Generator) createLines(num int, rng *rand.Rand) ([]line, [][]geom.Point) {
	all_lines := []line{}
	keep_lines := []line{}
	groups := [][]geom.Point{}

	makeLine := func(pos, dir geom.Point) line {
		return line{pos: pos, dir: dir}
	}

	makeAllLines := func(n int) {
		// Horizontal lines, emanating from left edge
		for i := 0; i < n+1; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(0, float64(2*i)), geom.MakePoint(1, 0)))
		}

		// Vertical lines, emanating from top edge
		for i := 0; i < n+1; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(float64(2*i), 0), geom.MakePoint(0, 1)))
		}

		// Slope -1 lines.  n pointing NW, n+1 pointing SE
		for i := 0; i < n+1; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(float64(2*n), float64(2*i)), geom.MakePoint(-1, -1)))
		}
		for i := 0; i < n; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(0, float64(2*i+2)), geom.MakePoint(1, 1)))
		}

		// Slope 1 lines.  n+1 pointing NE, n pointing SW
		for i := 0; i < n+1; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(0, float64(2*i)), geom.MakePoint(1, -1)))
		}
		for i := 0; i < n; i++ {
			all_lines = append(all_lines, makeLine(geom.MakePoint(float64(2*i+2), float64(2*n)), geom.MakePoint(1, -1)))
		}
	}

	n := g.Features.LineDensity
	makeAllLines(n)
	switch g.Features.Focus {
	case "Eight":
		g.makeRandom2x2(n, &all_lines, &keep_lines, &groups, rng)
	case "Sixteen":
		g.makeRandomStar(n, &all_lines, &keep_lines, &groups, rng)
	}

	// Discount the lines you've already used.
	num -= len(keep_lines)

	for len(all_lines) > 0 && num > 0 {
		ri := int(rng.Float64() * float64(len(all_lines)))
		keep_lines = append(keep_lines, all_lines[ri])
		all_lines = append(all_lines[:ri], all_lines[ri+1:]...)
		num--
	}

	return keep_lines, groups
}

// makeRandom2x2 mirrors JS makeRandom2x2 - exact same structure
func (g *Generator) makeRandom2x2(n int, allLines, keepLines *[]line, groups *[][]geom.Point, rng *rand.Rand) {
	// Remove some lines from the array so that a random 2x2 block is
	// forced to be part of the result.  Return the vertices associated
	// with that block.
	if rng.Float64() < 0.5 {
		ax := int(rng.Float64() * float64(n))
		ay := int(rng.Float64() * float64(n))

		// remove diagonals around 2x2 vertices at (ax,ay)
		var rem []int

		for i := 0; i < 3; i++ {
			k := (2*n + 2) + (n - 1) + ax - ay
			if (k >= 2*n+2) && (k < 4*n+3) {
				rem = append(rem, k)
			}
		}

		for i := 0; i < 3; i++ {
			k := 4*n + 3 + i + ax + ay
			rem = append(rem, k)
		}

		// rem.reverse();
		for i := len(rem) - 1; i >= 0; i-- {
			*allLines = append((*allLines)[:rem[i]], (*allLines)[rem[i]+1:]...)
		}

		// Mark these vertices for later
		*groups = append(*groups, []geom.Point{geom.MakePoint(float64(2*ay), float64(2*ax)), geom.MakePoint(float64(2*ay+2), float64(2*ax)),
			geom.MakePoint(float64(2*ay), float64(2*ax+2)), geom.MakePoint(float64(2*ay+2), float64(2*ax+2))})

		// Keep the necessary H and V lines
		for _, i := range []int{n + 1 + ay + 1, n + 1 + ay, ax + 1, ax} {
			*keepLines = append(*keepLines, (*allLines)[i])
			*allLines = append((*allLines)[:i], (*allLines)[i+1:]...)
		}
	} else {
		// on the bias.  Remove 2h,1v or 1h,2v
		a := int(rng.Float64() * float64(n))
		b := int(rng.Float64()*float64(n-1)) + 1

		if rng.Float64() < 0.5 {
			*allLines = append((*allLines)[:n+1+a+1], (*allLines)[n+1+a+2:]...)
			*allLines = append((*allLines)[:n+1+a], (*allLines)[n+1+a+1:]...)
			*allLines = append((*allLines)[:b], (*allLines)[b+1:]...)
			*groups = append(*groups, []geom.Point{geom.MakePoint(float64(2*a+1), float64(2*b-1)), geom.MakePoint(float64(2*a+1), float64(2*b+1)),
				geom.MakePoint(float64(2*a), float64(2*b)), geom.MakePoint(float64(2*a+2), float64(2*b))})

			for _, i := range []int{4*n + a + b + 1, 4*n + a + b, 3*n - 1 + b - a, 3*n - 2 + b - a} {
				*keepLines = append(*keepLines, (*allLines)[i])
				*allLines = append((*allLines)[:i], (*allLines)[i+1:]...)
			}
		} else {
			*allLines = append((*allLines)[:n+1+b], (*allLines)[n+1+b+1:]...)
			*allLines = append((*allLines)[:a+1], (*allLines)[a+2:]...)
			*allLines = append((*allLines)[:a], (*allLines)[a+1:]...)
			*groups = append(*groups, []geom.Point{geom.MakePoint(float64(2*b), float64(2*a)), geom.MakePoint(float64(2*b), float64(2*a+2)),
				geom.MakePoint(float64(2*b-1), float64(2*a+1)), geom.MakePoint(float64(2*b+1), float64(2*a+1))})

			for _, i := range []int{4*n + a + b + 1, 4*n + a + b, 3*n + a - b, 3*n - 1 + a - b} {
				*keepLines = append(*keepLines, (*allLines)[i])
				*allLines = append((*allLines)[:i], (*allLines)[i+1:]...)
			}
		}
	}
}

// makeRandomStar mirrors JS makeRandomStar - exact same structure
func (g *Generator) makeRandomStar(n int, allLines, keepLines *[]line, groups *[][]geom.Point, rng *rand.Rand) {
	ax := int(rng.Float64()*float64(n-4)) + 2
	ay := int(rng.Float64()*float64(n-4)) + 2

	var plan = []struct {
		idx  int
		keep bool
	}{
		{idx: 4*n + 7 + ax + ay, keep: false},
		{idx: 4*n + 6 + ax + ay, keep: true},
		{idx: 4*n + 5 + ax + ay, keep: false},
		{idx: 4*n + 4 + ax + ay, keep: false},
		{idx: 4*n + 3 + ax + ay, keep: false},
		{idx: 4*n + 2 + ax + ay, keep: true},
		{idx: 4*n + 1 + ax + ay, keep: false},

		{idx: 3*n + 5 + ax - ay, keep: false},
		{idx: 3*n + 4 + ax - ay, keep: true},
		{idx: 3*n + 3 + ax - ay, keep: false},
		{idx: 3*n + 2 + ax - ay, keep: false},
		{idx: 3*n + 1 + ax - ay, keep: false},
		{idx: 3*n + 0 + ax - ay, keep: true},
		{idx: 3*n - 1 + ax - ay, keep: false},

		{idx: n + 1 + ay + 2, keep: true},
		{idx: n + 1 + ay + 1, keep: false},
		{idx: n + 1 + ay, keep: false},
		{idx: n + 1 + ay - 1, keep: true},

		{idx: ax + 2, keep: true},
		{idx: ax + 1, keep: false},
		{idx: ax, keep: false},
		{idx: ax - 1, keep: true},
	}

	for _, step := range plan {
		if step.keep {
			*keepLines = append(*keepLines, (*allLines)[step.idx])
		}
		*allLines = append((*allLines)[:step.idx], (*allLines)[step.idx+1:]...)
	}

	*groups = append(*groups, []geom.Point{
		geom.MakePoint(float64(2*ay+1), float64(2*ax-3)),

		geom.MakePoint(float64(2*ay-2), float64(2*ax-2)),
		geom.MakePoint(float64(2*ay), float64(2*ax-2)),
		geom.MakePoint(float64(2*ay+2), float64(2*ax-2)),
		geom.MakePoint(float64(2*ay+4), float64(2*ax-2)),

		geom.MakePoint(float64(2*ay-2), float64(2*ax)),
		geom.MakePoint(float64(2*ay+4), float64(2*ax)),

		geom.MakePoint(float64(2*ay-3), float64(2*ax+1)),
		geom.MakePoint(float64(2*ay+5), float64(2*ax+1)),

		geom.MakePoint(float64(2*ay-2), float64(2*ax+2)),
		geom.MakePoint(float64(2*ay+4), float64(2*ax+2)),

		geom.MakePoint(float64(2*ay-2), float64(2*ax+4)),
		geom.MakePoint(float64(2*ay), float64(2*ax+4)),
		geom.MakePoint(float64(2*ay+2), float64(2*ax+4)),
		geom.MakePoint(float64(2*ay+4), float64(2*ax+4)),

		geom.MakePoint(float64(2*ay+1), float64(2*ax+5)),
	})
}

// groupTiles is an EXACT port of the JS groupTiles function
func groupTiles(tiles [][]geom.Point) []geom.Point {
	// Build a list of segments, eliminating matching pairs.
	type seg struct{ p, q geom.Point }
	var segs []seg

	for _, t := range tiles {
		tlen := len(t)
		for idx := 0; idx < tlen; idx++ {
			P := t[idx]
			Q := t[(idx+1)%tlen]
			found := -1

			// If this segment already exists in the opposite orientation,
			// don't add it again.
			for sidx := 0; sidx < len(segs); sidx++ {
				s := segs[sidx]
				if (geom.Dist(s.p, Q) < 0.0001) && (geom.Dist(s.q, P) < 0.0001) {
					found = sidx
					break
				}
			}

			if found >= 0 {
				// Eliminate the match too.
				segs = append(segs[:found], segs[found+1:]...)
			} else {
				segs = append(segs, seg{p: P, q: Q})
			}
		}
	}

	// Now reconstruct the boundary from the remaining segments.
	var ret []geom.Point
	ret = append(ret, segs[0].p)
	last := segs[0].q
	segs = segs[1:] // segs.splice(1) in JS removes first element

	for len(segs) > 0 {
		for idx := 0; idx < len(segs); idx++ {
			if geom.Dist(segs[idx].p, last) < 0.0001 {
				ret = append(ret, segs[idx].p)
				last = segs[idx].q
				segs = append(segs[:idx], segs[idx+1:]...)
				break
			}
		}
	}

	return ret
}
