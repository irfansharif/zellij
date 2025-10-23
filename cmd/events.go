package main

import (
	"log"
	"math"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/irfansharif/zellij/internal/app"
	"github.com/irfansharif/zellij/internal/memory"
)

const repeatInterval = 125 * time.Millisecond // time between successive regenerations/pans when pressed down
const basePanDistance = 100.0

// EventHandlers manages all event handling for the application.
type EventHandlers struct {
	application *app.App

	// Space, or shift+space triggers regenerating clusters (with the shift
	// allowing to go back). If held down, we do so continuously.
	spaceHeld, shiftHeld bool
	lastRegenTime        time.Time

	// J/K/H/L allow panning across through keypresses. They also do so
	// continuously if held.
	panKeyHeld                   bool
	panDirectionX, panDirectionY float64
	lastPanTime                  time.Time

	// Drag/pan state (per-gesture), captured on mouse press.
	isDragging                       bool
	dragStartMouseX, dragStartMouseY float64
	dragStartPanX, dragStartPanY     float64

	// Current mouse position in canvas coordinates.
	mouseCanvasX, mouseCanvasY float64

	// Input buffer for numeric input (complexity or batch operations).
	// Accumulates digits and comma until action key (Space, C, D) is pressed.
	inputBuffer string
}

// NewEventHandlers creates a new event handlers manager.
func NewEventHandlers(application *app.App) *EventHandlers {
	eh := &EventHandlers{
		application:   application,
		lastRegenTime: time.Now(),
		lastPanTime:   time.Now(),
	}
	eh.SetupCallbacks(application.Window)
	return eh
}

// SetupCallbacks configures all GLFW event callbacks.
func (eh *EventHandlers) SetupCallbacks(window *glfw.Window) {
	window.SetKeyCallback(func(wnd *glfw.Window, key glfw.Key, _ int, action glfw.Action, mods glfw.ModifierKey) {
		eh.handleKey(key, action, mods) // for various actions
	})
	window.SetMouseButtonCallback(func(wnd *glfw.Window, button glfw.MouseButton, action glfw.Action, mods glfw.ModifierKey) {
		eh.handleMouseButton(button, action) // for panning
	})
	window.SetCursorPosCallback(func(wnd *glfw.Window, xpos, ypos float64) {
		eh.handleCursorPos(xpos, ypos) // for tracking where the mouse currently is (used in regen, etc.)
	})
	window.SetScrollCallback(func(wnd *glfw.Window, _, zoomDelta float64) {
		eh.performZoom(zoomDelta) // for zooming
	})
	window.SetFramebufferSizeCallback(func(wnd *glfw.Window, newW, newH int) {
		eh.handleFramebufferSize(newW, newH) // for window resize
	})
}

// updateRendererView updates the renderer with the current view state and
// framebuffer size.
func (eh *EventHandlers) updateRendererView() {
	view := eh.application.View
	cw, ch := eh.application.Window.GetFramebufferSize()
	eh.application.Renderer.SetView(cw, ch, view.Zoom, view.PanX, view.PanY)
}

// handleFramebufferSize handles window resize events.
func (eh *EventHandlers) handleFramebufferSize(newW, newH int) {
	eh.application.View.SetViewport(newW, newH)
	eh.updateRendererView()
}

// handleKey handles keyboard input events.
func (eh *EventHandlers) handleKey(key glfw.Key, action glfw.Action, mods glfw.ModifierKey) {
	if action == glfw.Press {
		// Handle number keys for input.
		if key >= glfw.Key0 && key <= glfw.Key9 {
			eh.inputBuffer += string(rune('0' + int(key-glfw.Key0)))
			return
		}

		// Handle ',' key for batch operations.
		if key == glfw.KeyComma {
			eh.inputBuffer += ","
			return
		}

		// Handle Escape key to clear input buffer.
		if key == glfw.KeyEscape {
			eh.inputBuffer = ""
			return
		}

		// Clear input buffer on non-input keys (except Space, C, D which are
		// action keys).
		if !(key == glfw.KeySpace || key == glfw.KeyC || key == glfw.KeyD) {
			eh.inputBuffer = ""
		}
	}

	switch key {
	case glfw.KeySpace:
		eh.handleRegenerationKeys(action, mods)
	case glfw.KeyR:
		if action == glfw.Press {
			eh.handleResetKey()
		}
	case glfw.KeyC:
		if action == glfw.Press {
			eh.handleCreateClusterKey()
		}
	case glfw.KeyD:
		if action == glfw.Press {
			eh.handleDeleteClusterKey()
		}
	case glfw.KeyTab:
		if action == glfw.Press {
			next := true
			if (mods & glfw.ModShift) != 0 {
				next = false
			}
			eh.handleClusterNavigation(next)
		}
	case glfw.KeyJ:
		eh.handlePanKeys(action, 0 /*dx*/, -1 /*dy*/) // pan down
	case glfw.KeyK:
		eh.handlePanKeys(action, 0 /*dx*/, 1 /*dy*/) // pan up
	case glfw.KeyH:
		eh.handlePanKeys(action, 1 /*dx*/, 0 /*dy*/) // pan right
	case glfw.KeyL:
		eh.handlePanKeys(action, -1 /*dx*/, 0 /*dy*/) // pan left
	case glfw.KeyEqual:
		if action == glfw.Press && (mods&glfw.ModSuper) != 0 {
			eh.performZoom(1) // zoom in
		}
	case glfw.KeyMinus:
		if action == glfw.Press && (mods&glfw.ModSuper) != 0 {
			eh.performZoom(-1) // zoom out
		}
	}
}

// handleRegenerationKeys handles space and shift+space presses/releases (regenerate cluster).
func (eh *EventHandlers) handleRegenerationKeys(action glfw.Action, mods glfw.ModifierKey) {
	shiftHeld := (mods & glfw.ModShift) != 0

	switch action {
	case glfw.Press:
		_, complexity := eh.parseInput("space") // count is n/a

		if shiftHeld {
			eh.shiftHeld = true
			eh.spaceHeld = false
		} else {
			eh.spaceHeld = true
			eh.shiftHeld = false
		}
		eh.handleSeedChange(shiftHeld)
		eh.application.RegenerateClosest(eh.mouseCanvasX, eh.mouseCanvasY, complexity)
		w, h := eh.application.Window.GetFramebufferSize()
		eh.application.PrepareRenderer(w, h)
		eh.lastRegenTime = time.Now()

	case glfw.Release:
		eh.spaceHeld = false
		eh.shiftHeld = false

	case glfw.Repeat:
		// Ignore repeat events - we handle continuous regeneration ourselves to
		// ensure consistent timing.
	}
}

// handlePanKeys handles j/k/h/l key presses, and also releases for
// continuous panning.
func (eh *EventHandlers) handlePanKeys(action glfw.Action, dx, dy float64) {
	switch action {
	case glfw.Press:
		eh.panKeyHeld = true
		eh.panDirectionX = dx
		eh.panDirectionY = dy
		eh.performPan(dx, dy)
		eh.lastPanTime = time.Now()

	case glfw.Release:
		eh.panKeyHeld = false

	case glfw.Repeat:
		// Ignore repeat events - we handle continuous panning ourselves to
		// ensure consistent timing.
	}
}

// performPan executes a single pan operation.
func (eh *EventHandlers) performPan(dx, dy float64) {
	// Scale by inverse of zoom: when zoomed out (zoom < 1), we move further in
	// canvas space and vice-cersa.
	view := eh.application.View
	zoom := view.Zoom
	scaledDistance := basePanDistance / zoom

	// Apply the pan
	view.SetPan(view.PanX+dx*scaledDistance, view.PanY+dy*scaledDistance)
	eh.updateRendererView()

	mouseX, mouseY := eh.application.Window.GetCursorPos()
	eh.updateMouseCanvasPos(mouseX, mouseY)
}

// handleResetKey handles R key press (reset zoom and pan to closest cluster,
// and also set cursor for subsequent tabs/shift+tabs).
func (eh *EventHandlers) handleResetKey() {
	view := eh.application.View
	clusters := eh.application.ClusterManager.FindClosestClusters(eh.mouseCanvasX, eh.mouseCanvasY)
	if len(clusters) > 0 {
		cluster := clusters[0]
		view.ResetTo(cluster.CanvasPos)
		eh.application.ClusterManager.SetCurrentCluster(cluster)
	}

	eh.updateRendererView()
	mouseX, mouseY := eh.application.Window.GetCursorPos()
	eh.updateMouseCanvasPos(mouseX, mouseY)
}

// handleSeedChange updates the seed of the closest cluster.
func (eh *EventHandlers) handleSeedChange(increment bool) {
	clusters := eh.application.ClusterManager.FindClosestClusters(
		eh.mouseCanvasX,
		eh.mouseCanvasY,
	)
	if len(clusters) == 0 {
		return // nothing to do
	}
	cluster := clusters[0]
	if increment {
		cluster.SetSeed(cluster.Seed + 1)
	} else {
		cluster.SetSeed(cluster.Seed - 1)
	}
}

// handleContinuousRegeneration handles continuous regeneration while space is held.
func (eh *EventHandlers) handleContinuousRegeneration() {
	if !(eh.spaceHeld || eh.shiftHeld) {
		return // nothing to do
	}

	now := time.Now()
	if now.Sub(eh.lastRegenTime) < repeatInterval {
		return // not enough time has passed since the last regeneration
	}

	eh.handleSeedChange(eh.spaceHeld /* increment*/)
	eh.application.RegenerateClosest(eh.mouseCanvasX, eh.mouseCanvasY, nil /*complexity*/) // use existing complexity for continuous regeneration
	w, h := eh.application.Window.GetFramebufferSize()
	eh.application.PrepareRenderer(w, h)
	eh.lastRegenTime = now
}

// handleContinuousPanning handles continuous panning while pan keys are held.
func (eh *EventHandlers) handleContinuousPanning() {
	if !eh.panKeyHeld {
		return // nothing to do
	}

	now := time.Now()
	if now.Sub(eh.lastPanTime) < repeatInterval {
		return // not enough time has passed since the last pan
	}

	eh.performPan(eh.panDirectionX, eh.panDirectionY)
	eh.lastPanTime = now
}

// handleMouseButton handles mouse button events for panning.
func (eh *EventHandlers) handleMouseButton(button glfw.MouseButton, action glfw.Action) {
	if button != glfw.MouseButtonLeft {
		return // nothing to do
	}

	switch action {
	case glfw.Press:
		eh.startPanning()
	case glfw.Release:
		eh.stopPanning()
	}
}

// updateMouseCanvasPos recalculates mouse position in canvas coordinates after
// view changes.
func (eh *EventHandlers) updateMouseCanvasPos(mouseX, mouseY float64) {
	window := eh.application.Window
	cw, ch := window.GetFramebufferSize()
	scaleX, scaleY := window.GetContentScale()
	fbMouseX, fbMouseY := mouseX*float64(scaleX), mouseY*float64(scaleY)

	view := eh.application.View
	zoom := view.Zoom
	panX, panY := view.PanX, view.PanY
	centerX, centerY := float64(cw)/2, float64(ch)/2

	// Use the exact same transformation as zoom calculations
	// canvasPos = (screenPos - center*(1-zoom) - pan) / zoom
	eh.mouseCanvasX = (fbMouseX - centerX*(1-zoom) - panX) / zoom
	eh.mouseCanvasY = (fbMouseY - centerY*(1-zoom) - panY) / zoom
}

// handleCursorPos handles mouse movement for panning.
func (eh *EventHandlers) handleCursorPos(xpos, ypos float64) {
	eh.updateMouseCanvasPos(xpos, ypos)
	eh.updatePanning(xpos, ypos)
}

// startPanning starts the panning operation.
func (eh *EventHandlers) startPanning() {
	eh.isDragging = true
	eh.dragStartMouseX, eh.dragStartMouseY = eh.application.Window.GetCursorPos()
	view := eh.application.View
	eh.dragStartPanX, eh.dragStartPanY = view.PanX, view.PanY
}

// stopPanning ends panning operation.
func (eh *EventHandlers) stopPanning() {
	eh.isDragging = false
}

// updatePanning updates pan position based on mouse movement.
func (eh *EventHandlers) updatePanning(xpos, ypos float64) {
	if !eh.isDragging {
		return
	}

	scaleX, scaleY := eh.application.Window.GetContentScale()
	dx := (xpos - eh.dragStartMouseX) * float64(scaleX)
	dy := (ypos - eh.dragStartMouseY) * float64(scaleY)

	eh.application.View.SetPan(eh.dragStartPanX+dx, eh.dragStartPanY+dy)
	eh.updateRendererView() // direct update for maximum smoothness
}

// performZoom handles zoom operations with cursor-centered zooming.
func (eh *EventHandlers) performZoom(zoomDelta float64) {
	wnd := eh.application.Window
	cw, ch := wnd.GetFramebufferSize()
	centerX, centerY := float64(cw)/2, float64(ch)/2
	mouseX, mouseY := wnd.GetCursorPos()

	scaleX, scaleY := wnd.GetContentScale()
	fbMouseX, fbMouseY := mouseX*float64(scaleX), mouseY*float64(scaleY)

	// Apply zoom with responsive increments for smooth zooming
	zoomFactor := 1.0 + zoomDelta*0.15 // Increased for more responsive zooming
	view := eh.application.View
	oldZoom := view.Zoom

	// Cursor position relative to viewport center.
	cursorOffsetX, cursorOffsetY := fbMouseX-centerX, fbMouseY-centerY

	// What canvas point (relative to center) is under the cursor right now?
	canvasOffsetX, canvasOffsetY := (cursorOffsetX-view.PanX)/oldZoom, (cursorOffsetY-view.PanY)/oldZoom

	// Update zoom.
	view.SetZoom(oldZoom * zoomFactor)

	// Calculate new pan to keep that canvas point at the cursor.
	newZoom := view.Zoom
	view.SetPan(cursorOffsetX-canvasOffsetX*newZoom, cursorOffsetY-canvasOffsetY*newZoom)
	eh.updateRendererView() // direct update for maximum smoothness
}

// handleCreateClusterKey handles C key press (create single new cluster or
// batch create).
func (eh *EventHandlers) handleCreateClusterKey() {
	// Parse batch count and complexity from input buffer.
	batchCount, complexity := eh.parseInput("c")

	// Always start at the current cursor position for batch creation.
	startCanvasX, startCanvasY := eh.mouseCanvasX, eh.mouseCanvasY

	// Create clusters in a grid pattern
	gridUnitPixels := 25.0
	gridSpacingX := gridUnitPixels * 20.0 // Horizontal spacing between clusters
	gridSpacingY := gridUnitPixels * 20.0 // Vertical spacing between clusters
	gridCols := int(math.Sqrt(float64(batchCount))) + 1

	for i := 0; i < batchCount; i++ {
		// Calculate grid offset with some randomization
		col, row := i%gridCols, i/gridCols
		offsetX := float64(col)*gridSpacingX + rand.Float64()*gridUnitPixels
		offsetY := float64(row)*gridSpacingY + rand.Float64()*gridUnitPixels

		newCanvasX, newCanvasY := startCanvasX+offsetX, startCanvasY+offsetY
		eh.application.CreateCluster(newCanvasX, newCanvasY, complexity)
	}

	w, h := eh.application.Window.GetFramebufferSize()
	eh.application.PrepareRenderer(w, h)
}

// handleDeleteClusterKey handles D key press (delete closest cluster or batch delete).
func (eh *EventHandlers) handleDeleteClusterKey() {
	batchCount, _ := eh.parseInput("d")

	clusters := eh.application.ClusterManager.FindClosestClusters(eh.mouseCanvasX, eh.mouseCanvasY)
	if len(clusters) == 0 {
		return // nothing to do
	}

	if batchCount > len(clusters) {
		batchCount = len(clusters) // defensive
	}
	for i := 0; i < batchCount; i++ {
		clusterID := clusters[i].ID

		if err := eh.application.MemoryController.RemoveCluster(memory.ClusterID(clusterID)); err != nil {
			log.Fatalf("Failed to remove cluster %d from GPU: %v", clusterID, err)
		}
		eh.application.ClusterManager.RemoveCluster(clusterID)
	}
}

// handleClusterNavigation handles tab and shift+tab key presses for cluster navigation.
func (eh *EventHandlers) handleClusterNavigation(next bool) {
	cluster := eh.application.ClusterManager.IterCluster(next)
	if cluster == nil {
		return // nothing to do
	}

	// Reset zoom and pan to center of the cluster.
	eh.application.View.ResetTo(cluster.CanvasPos)
	eh.updateRendererView()

	// After tabbing, set mouse position to the center of the selected cluster
	// (helps with subsequent deletions, regenerations, etc.)
	eh.mouseCanvasX = cluster.CanvasPos.X
	eh.mouseCanvasY = cluster.CanvasPos.Y
}

func (eh *EventHandlers) parseInput(action string) (count int, complexity *int) {
	input := eh.inputBuffer
	if input == "" {
		return 1, nil
	}

	if commaIndex := strings.Index(input, ","); commaIndex != -1 {
		// Format: "10,5" (count=10, complexity=5)
		if countStr := strings.TrimSpace(input[:commaIndex]); countStr != "" {
			if val, err := strconv.Atoi(countStr); err == nil {
				count = val
			}
		}
		if complexityStr := strings.TrimSpace(input[commaIndex+1:]); complexityStr != "" {
			if val, err := strconv.Atoi(complexityStr); err == nil {
				complexity = &val
			}
		}
	} else {
		// Format: "5" (count=1, complexity=5 for space/c, count=5 for d)
		if val, err := strconv.Atoi(input); err == nil {
			if action == "d" {
				count = val
			} else {
				count = 1
				complexity = &val
			}
		}
	}

	eh.inputBuffer = ""
	return count, complexity
}
