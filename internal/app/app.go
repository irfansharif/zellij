package app

import (
	"log"
	"math/rand"

	"github.com/go-gl/glfw/v3.3/glfw"
	"github.com/irfansharif/zellij/internal/gen"
	"github.com/irfansharif/zellij/internal/geom"
	"github.com/irfansharif/zellij/internal/memory"
	"github.com/irfansharif/zellij/internal/palette"
	"github.com/irfansharif/zellij/internal/render"
)

const maxGenerationAttempts = 10 // maximum number of attempts to generate a valid composition
const integerGridSize = 25.0 // grid size in integer space (using the same one makes individual tiles size identically)

// App encapsulates the main application state and logic.
type App struct {
	Window           *glfw.Window
	Renderer         *render.Renderer
	Generator        *gen.Generator
	View             *View
	ClusterManager   *ClusterManager
	MemoryController *memory.MemoryController
}

// NewApp creates a new application instance.
func NewApp(window *glfw.Window, generator *gen.Generator, view *View, seed int64) *App {
	memController := memory.NewMemoryController()
	renderer := render.NewRenderer(memController)
	clusterManager := NewClusterManager(seed)
	return &App{
		Window:           window,
		Renderer:         renderer,
		Generator:        generator,
		View:             view,
		ClusterManager:   clusterManager,
		MemoryController: memController,
	}
}

// CreateCluster creates a new cluster at the specified position.
func (app *App) CreateCluster(canvasX, canvasY float64, complexity *int) {
	seed := app.ClusterManager.IncrementSeed()

	// Generate composition for this cluster.
	comp, ok := app.GenerateComposition(seed, complexity)
	if !ok {
		log.Printf("Failed to generate valid composition after %d attempts", maxGenerationAttempts)
		return // don't create the cluster
	}

	// Add cluster at specified position.
	canvasPos := geom.MakePoint(canvasX, canvasY)
	gridBounds := geom.MakeBox(0, 0, integerGridSize, integerGridSize)
	app.ClusterManager.AddCluster(gridBounds, canvasPos, comp, seed, complexity)
}

// RegenerateClosest regenerates the closest cluster to the given center.
func (app *App) RegenerateClosest(centerX, centerY float64, complexity *int) {
	clusters := app.ClusterManager.FindClosestClusters(centerX, centerY)
	if len(clusters) == 0 {
		return // nothing to do
	}
	cluster := clusters[0]

	// Determine complexity to use: parameter if provided, otherwise cluster's
	// existing complexity.
	if complexity == nil {
		complexity = cluster.Complexity
	}

	// Regenerate cluster from scratch with the cluster's seed (retrying
	// internally if needed).
	comp, ok := app.GenerateComposition(cluster.Seed, complexity)
	if !ok {
		return // don't update the cluster with invalid geometry
	}
	cluster.SetComposition(comp)
	cluster.SetComplexity(complexity)
}

// PrepareRenderer prepares the renderer with all current clusters.
func (app *App) PrepareRenderer(cw, ch int) {
	// Sync renderer view state BEFORE generating geometry. PrepareMulti uses
	// r.zoom, r.panX, r.panY to generate geometry, so these must be current.
	app.Renderer.SetView(cw, ch, app.View.Zoom, app.View.PanX, app.View.PanY)

	clusters := app.ClusterManager.GetClusters()
	renderData := make([]render.ClusterRenderData, len(clusters))
	for i, cluster := range clusters {
		palette := palette.RandomPalette(rand.New(rand.NewSource(cluster.Seed)))
		renderData[i] = render.ClusterRenderData{
			ID:          cluster.ID,
			Composition: cluster.Composition,
			GridBounds:  cluster.GridBounds,
			CanvasPos:   cluster.CanvasPos,
			Palette:     palette,
			Seed:        cluster.Seed,
			Dirty:       cluster.Dirty,
		}
	}
	if err := app.Renderer.PrepareMulti(renderData, cw, ch); err != nil {
		log.Fatalf("Failed to prepare renderer: %v", err)
	}
	for _, cluster := range clusters {
		cluster.Dirty = false // mark clusters as clean
	}
}

// GenerateComposition generates a composition with the given base seed,
// retrying up to maxRetries times until a valid geometry is produced.
func (app *App) GenerateComposition(baseSeed int64, complexity *int) (gen.Composition, bool) {
	for attempt := 0; attempt < maxGenerationAttempts; attempt++ {
		retrySeed := baseSeed + int64(attempt)
		comp := app.Generator.Generate(retrySeed, complexity)

		if HasValidGeometry(comp) {
			return comp, true
		}

		if attempt < maxGenerationAttempts-1 {
			log.Printf("WARNING: Composition generation failed, retrying (attempt %d/%d)", attempt+1, maxGenerationAttempts)
		}
	}

	log.Printf("WARNING: Failed to generate valid composition after %d attempts", maxGenerationAttempts)
	return gen.Composition{}, false
}

// HasValidGeometry checks if a composition contains any valid geometry points.
func HasValidGeometry(comp gen.Composition) bool {
	if len(comp.Boundary) > 0 {
		return true
	}

	for _, tile := range comp.Tiles {
		if len(tile.Path) > 0 {
			return true
		}
	}

	return false
}
