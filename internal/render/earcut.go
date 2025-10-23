package render

import (
	"log"

	"github.com/rclancey/earcut"

	"github.com/irfansharif/zellij/internal/geom"
)

// earClip triangulates a polygon using the earcut algorithm. It takes in a list
// of polygon vertices (the order doesn't actually matter) and returns a slice
// of triangles, each represented as a [3]geom.Point.
func earClip(polygonPoints []geom.Point) [][3]geom.Point {
	if len(polygonPoints) < 3 {
		log.Fatalf("Degenerate polygon (%d vertices < 3)", len(polygonPoints))
	}

	// Convert polygon points to flat coordinate array required by earcut.
	// Format: [x0, y0, x1, y1, ..., xn, yn]
	vertexCoords := make([]float64, len(polygonPoints)*2)
	for i, point := range polygonPoints {
		vertexCoords[i*2] = point.X   // x coordinate
		vertexCoords[i*2+1] = point.Y // y coordinate
	}

	triangleIndices, err := earcut.Earcut(vertexCoords, nil /* holeIndices */, 2 /* dim */)
	if err != nil {
		log.Fatalf("Triangulation failed for %d-vertex polygon: %v", len(polygonPoints), err)
	}

	if len(triangleIndices)%3 != 0 {
		log.Fatalf("Invalid triangle count (indices: %d, not divisible by 3)", len(triangleIndices))
	}

	// Convert triangle indices back to geom.Point triangles.
	triangleCount := len(triangleIndices) / 3
	triangles := make([][3]geom.Point, triangleCount)

	for triangleIndex := 0; triangleIndex < triangleCount; triangleIndex++ {
		// Extract vertex indices for this triangle (3 indices per triangle).
		baseIndex := triangleIndex * 3
		vertexIndex0 := triangleIndices[baseIndex]
		vertexIndex1 := triangleIndices[baseIndex+1]
		vertexIndex2 := triangleIndices[baseIndex+2]

		// Create triangle from indexed vertices. Each vertex index maps to a
		// (x,y) pair in vertexCoords array.
		triangles[triangleIndex] = [3]geom.Point{
			{X: vertexCoords[vertexIndex0*2], Y: vertexCoords[vertexIndex0*2+1]},
			{X: vertexCoords[vertexIndex1*2], Y: vertexCoords[vertexIndex1*2+1]},
			{X: vertexCoords[vertexIndex2*2], Y: vertexCoords[vertexIndex2*2+1]},
		}
	}

	return triangles
}
