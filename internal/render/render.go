// Package render handles the visual presentation of generated Zellij patterns.
//
// It takes abstract tile compositions from the gen package and:
// 1. Maps tiles from unit grid coordinates to screen coordinates.
// 4. Triangulates and renders the filled patterns using OpenGL
package render

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/irfansharif/zellij/internal/fillers"
	"github.com/irfansharif/zellij/internal/gen"
	"github.com/irfansharif/zellij/internal/geom"
	"github.com/irfansharif/zellij/internal/memory"
	"github.com/irfansharif/zellij/internal/palette"
)

const viewportScaleFactor = 0.7

type Renderer struct {
	w, h             int
	zoom, panX, panY float64

	memController *memory.MemoryController
	shaderManager *ShaderManager
	stats         Stats
}

// ClusterRenderData holds rendering information for a single cluster.
type ClusterRenderData struct {
	ID            memory.ClusterID // Cluster ID for memory controller
	Composition   gen.Composition
	GridBounds    geom.Box
	CanvasPos     geom.Point
	WorldToScreen geom.Affine
	Palette       palette.Palette
	Seed          int64 // seed for deterministic per-cluster effects (e.g., shimmer)
	Dirty         bool  // whether cluster needs GPU re-upload
}

// Stats tracks rendering performance metrics.
type Stats struct {
	LastPrepareTimeMs float64 // time spent in last Prepare() call in milliseconds
	LastDrawTimeUs    float64 // time spent in last Draw() call in microseconds
}

func NewRenderer(memController *memory.MemoryController) *Renderer {
	return &Renderer{
		zoom:          1.0,
		shaderManager: NewShaderManager(),
		memController: memController,
	}
}

func (r *Renderer) SetView(w, h int, zoom, panX, panY float64) {
	r.w, r.h = w, h
	r.zoom = zoom
	r.panX, r.panY = panX, panY
}

// PrepareMulti prepares the renderer for multiple clusters with dirty tracking.
// Only dirty clusters have their geometry regenerated and uploaded to GPU.
func (r *Renderer) PrepareMulti(clusters []ClusterRenderData, w, h int) error {
	startTime := time.Now()

	if w <= 0 || h <= 0 {
		log.Fatalf("cannot prepare renderer: invalid viewport dimensions %dx%d", w, h)
	}

	r.w, r.h = w, h

	if len(clusters) == 0 {
		r.stats = Stats{
			LastPrepareTimeMs: float64(time.Since(startTime).Microseconds()) / 1000.0,
		}
		return nil
	}

	// Process dirty clusters and cache geometry for potential re-upload after
	// potential batch growth+move in the memory controller.
	// TODO(irfansharif): Simplify this structure.
	dirtyCount := 0
	clusterGeometry := make(map[memory.ClusterID][]float32) // Cache generated geometry for re-uploads

	for i := range clusters {
		cluster := &clusters[i]
		if !cluster.Dirty {
			continue // skip clean clusters
		}

		// Generate geometry in world/canvas space.
		vertices := r.generateClusterGeometry(*cluster)
		if len(vertices) == 0 {
			log.Printf("WARNING: cluster %d generated no geometry, skipping", cluster.ID)
			continue
		}

		// Cache geometry for potential re-upload after growth.
		clusterGeometry[cluster.ID] = vertices

		// Upload to memory controller.
		if err := r.memController.EnsureSlot(cluster.ID, vertices); err != nil {
			log.Printf("Error uploading cluster %d: %v", cluster.ID, err)
			continue
		}

		dirtyCount++
	}

	// Check if any clusters were affected by batch growth during upload
	// These clusters need to be re-uploaded because their data was in the old VBO
	affectedIDs := r.memController.GetAndClearClustersNeedingReupload()
	for _, clusterID := range affectedIDs {
		// Check if we have cached geometry (cluster was dirty this frame)
		vertices, exists := clusterGeometry[clusterID]
		if !exists {
			// Cluster was clean, need to regenerate its geometry
			for i := range clusters {
				if clusters[i].ID == clusterID {
					vertices = r.generateClusterGeometry(clusters[i])
					break
				}
			}
		}

		if len(vertices) > 0 {
			memClusterID := memory.ClusterID(clusterID)
			if err := r.memController.EnsureSlot(memClusterID, vertices); err != nil {
				log.Printf("Error re-uploading cluster %d: %v", clusterID, err)
			}
		}
	}

	r.stats.LastPrepareTimeMs = float64(time.Since(startTime).Microseconds()) / 1000.0
	return nil
}

// generateClusterGeometry generates array-based vertex data for a cluster in world/canvas space.
// This is the core of world-space rendering: geometry is generated once and transformed by
// view matrix in the shader, so pan/zoom doesn't require regeneration.
func (r *Renderer) generateClusterGeometry(clusterData ClusterRenderData) []float32 {
	// Compute bounds for this cluster's composition
	bounds, err := r.computeModelBounds(clusterData.Composition)
	if err != nil {
		return nil
	}

	// Apply shimmer to the cluster's palette deterministically using the
	// cluster seed.
	localRand := rand.New(rand.NewSource(clusterData.Seed))
	shimmerPal := palette.Shimmered(clusterData.Palette, clusterData.Composition.Shimmer, localRand)

	// Calculate scale from model space to world space
	// The cluster should maintain a consistent size across the canvas
	minSide := math.Min(float64(r.w), float64(r.h))
	referenceGridSide := clusterData.GridBounds.W
	if referenceGridSide == 0 {
		referenceGridSide = 1
	}
	pixelsPerWorldUnit := (viewportScaleFactor * minSide) / referenceGridSide

	// Compute world-space dimensions
	worldW := bounds.W * pixelsPerWorldUnit
	worldH := bounds.H * pixelsPerWorldUnit

	// Create transform from model space to world space
	// Model bounds → scaled bounds centered at cluster's canvas position
	worldBounds := geom.MakeBox(
		clusterData.CanvasPos.X-0.5*worldW,
		clusterData.CanvasPos.Y-0.5*worldH,
		worldW,
		worldH,
	)
	modelToWorld := geom.FillBox(bounds, worldBounds, false)

	// Generate triangles for all tiles in world space
	vertices := make([]float32, 0, len(clusterData.Composition.Tiles)*100) // estimate

	for _, tile := range clusterData.Composition.Tiles {
		// Transform tile to world coordinates.
		worldPath := make([]geom.Point, len(tile.Path))
		for i, p := range tile.Path {
			worldPath[i] = modelToWorld.MulPoint(p)
		}

		// Try to match filler pattern.
		if !r.prepareTileToVertices(worldPath, shimmerPal, &vertices) {
			log.Printf("WARNING: no filler pattern found for tile %d, skipping", clusterData.ID)
		}
	}

	return vertices
}

// prepareTileToVertices generates vertices for a tile with filler pattern, appending to vertices slice.
// Returns true if filler was applied, false if fallback should be used.
func (r *Renderer) prepareTileToVertices(tilePath []geom.Point, pal palette.Palette, vertices *[]float32) bool {
	if len(fillers.Library) == 0 || len(tilePath) == 0 {
		return false
	}

	// Generate geometric signature with rotation logic.
	currentSig, alignedPath, found := fillers.Signature(tilePath)
	if !found {
		return false
	}

	// Select a filler cluster.
	matchingClusters := fillers.Library[currentSig]
	selectedCluster := matchingClusters[len(currentSig)%len(matchingClusters)]

	// Validate cluster.
	if len(selectedCluster.Bounds) < 2 {
		return false
	}

	// Align cluster to tile using reference segments.
	clusterRefStart := selectedCluster.Bounds[0]
	clusterRefEnd := selectedCluster.Bounds[1]
	tileRefStart := alignedPath[0]
	tileRefEnd := alignedPath[1]
	alignmentTransform := geom.MatchTwoSegs(clusterRefStart, clusterRefEnd, tileRefStart, tileRefEnd)

	// Process each decorative shape.
	for _, shape := range selectedCluster.Shapes {
		if len(shape.Path) < 3 {
			continue
		}

		// Get shape color.
		clampedIndex := minInt(4, maxInt(0, shape.Colour))
		shapeColor := pal[clampedIndex]

		// Transform shape vertices to tile space.
		transformedVertices := make([]geom.Point, len(shape.Path))
		for j, vertex := range shape.Path {
			transformedVertices[j] = alignmentTransform.MulPoint(vertex)
		}

		// Triangulate and append to vertices (array-based: no deduplication).
		triangles := earClip(transformedVertices)
		if triangles == nil {
			continue
		}

		for _, tri := range triangles {
			for v := 0; v < 3; v++ {
				*vertices = append(*vertices,
					float32(tri[v].X), float32(tri[v].Y), // position
					float32(shapeColor.R)/255.0, float32(shapeColor.G)/255.0,
					float32(shapeColor.B)/255.0, float32(shapeColor.A)/255.0, // color
				)
			}
		}
	}

	return true
}

// computeModelBounds calculates the bounding box for the composition's geometry.
//
// Returns the axis-aligned bounding box containing all tiles or boundary points.
// If boundary is available, uses it for more accurate bounds. Falls back to
// tile-based bounds if boundary is unavailable.
//
// Returns error if no valid geometry is found.
func (r *Renderer) computeModelBounds(comp gen.Composition) (geom.Box, error) {
	xmin, xmax := math.MaxFloat64, -math.MaxFloat64
	ymin, ymax := math.MaxFloat64, -math.MaxFloat64
	pointCount := 0

	// Try boundary first for more accurate bounds.
	if len(comp.Boundary) > 0 {
		for _, p := range comp.Boundary {
			xmin = math.Min(xmin, p.X)
			xmax = math.Max(xmax, p.X)
			ymin = math.Min(ymin, p.Y)
			ymax = math.Max(ymax, p.Y)
			pointCount++
		}

	} else {
		// Fallback to tile geometry
		for _, tile := range comp.Tiles {
			for _, p := range tile.Path {
				xmin = math.Min(xmin, p.X)
				xmax = math.Max(xmax, p.X)
				ymin = math.Min(ymin, p.Y)
				ymax = math.Max(ymax, p.Y)
				pointCount++
			}
		}

	}

	// Validate computed bounds.
	if pointCount == 0 {
		return geom.Box{}, fmt.Errorf("no valid geometry points found in composition")
	}
	if math.IsInf(xmin, 0) || math.IsInf(xmax, 0) || math.IsInf(ymin, 0) || math.IsInf(ymax, 0) {
		return geom.Box{}, fmt.Errorf("computed bounds contain infinite values: x[%f,%f] y[%f,%f]", xmin, xmax, ymin, ymax)
	}
	if xmin >= xmax || ymin >= ymax {
		return geom.Box{}, fmt.Errorf("computed bounds are degenerate: x[%f,%f] y[%f,%f]", xmin, xmax, ymin, ymax)
	}

	width := xmax - xmin
	height := ymax - ymin
	return geom.MakeBox(xmin, ymin, width, height), nil
}

func (r *Renderer) Draw() {
	startTime := time.Now()

	// Set shader uniforms.
	matrix := r.computeTransformMatrix()
	r.shaderManager.SetTransform(matrix)

	// Memory controller handles all draws.
	if err := r.memController.Draw(); err != nil {
		log.Fatalf("Memory controller draw failed: %v", err)
	}

	// Record draw time.
	r.stats.LastDrawTimeUs = float64(time.Since(startTime).Microseconds())
}

// Stats returns the current performance statistics
func (r *Renderer) Stats() Stats {
	return r.stats
}

// computeTransformMatrix computes the complete transformation matrix from world
// coordinates to OpenGL NDC.
func (r *Renderer) computeTransformMatrix() [16]float32 {
	transform := geom.MakeAffine(1, 0, 0, 0, 1, 0)
	transform = r.applyZoomTransform(transform)
	transform = r.applyPanTransform(transform)
	transform = r.applyScreenToNDCTransform(transform)
	return r.affineToMatrix4(transform)
}

// applyZoomTransform applies zoom scaling around the viewport center.
func (r *Renderer) applyZoomTransform(baseTransform geom.Affine) geom.Affine {
	viewportCenterX := float64(r.w) / 2.0
	viewportCenterY := float64(r.h) / 2.0

	translateToOrigin := geom.MakeAffine(1, 0, -viewportCenterX, 0, 1, -viewportCenterY)
	uniformScale := geom.MakeAffine(r.zoom, 0, 0, 0, r.zoom, 0)
	translateBack := geom.MakeAffine(1, 0, viewportCenterX, 0, 1, viewportCenterY)

	return translateBack.Mul(uniformScale.Mul(translateToOrigin.Mul(baseTransform)))
}

// applyPanTransform applies pan translation in screen space.
func (r *Renderer) applyPanTransform(baseTransform geom.Affine) geom.Affine {
	panTranslation := geom.MakeAffine(1, 0, r.panX, 0, 1, r.panY)
	return panTranslation.Mul(baseTransform)
}

// applyScreenToNDCTransform converts screen coordinates to OpenGL NDC.
func (r *Renderer) applyScreenToNDCTransform(baseTransform geom.Affine) geom.Affine {
	screenToNDC := geom.MakeAffine(
		2.0/float64(r.w), 0, -1,
		0, -2.0/float64(r.h), 1,
	)
	return screenToNDC.Mul(baseTransform)
}

// affineToMatrix4 converts an affine transform to OpenGL 4x4 matrix format.
func (r *Renderer) affineToMatrix4(transform geom.Affine) [16]float32 {
	return [16]float32{
		float32(transform.A), float32(transform.B), 0, 0,
		float32(transform.D), float32(transform.E), 0, 0,
		0, 0, 1, 0,
		float32(transform.C), float32(transform.F), 0, 1,
	}
}

// minInt returns the minimum of two integers
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt returns the maximum of two integers
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
