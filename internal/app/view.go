package app

import (
	"github.com/irfansharif/zellij/internal/geom"
)

const (
	minZoom = 0.1
	maxZoom = 8.0
)

// View manages the current view state including zoom, pan, and viewport.
type View struct {
	Zoom          float64
	PanX, PanY    float64
	Width, Height int
}

// NewView creates a new view state with default values.
func NewView(width, height int) *View {
	return &View{
		Zoom:   1.0,
		Width:  width,
		Height: height,
	}
}

// SetZoom sets the zoom level, clamping to valid range.
func (vs *View) SetZoom(zoom float64) {
	if zoom < minZoom {
		vs.Zoom = minZoom
	} else if zoom > maxZoom {
		vs.Zoom = maxZoom
	} else {
		vs.Zoom = zoom
	}
}

// SetPan sets the pan position to the given coordinates.
func (vs *View) SetPan(x, y float64) {
	vs.PanX = x
	vs.PanY = y
}

// SetViewport updates the viewport dimensions.
func (vs *View) SetViewport(width, height int) {
	vs.Width = width
	vs.Height = height
}

// ResetTo resets zoom to 1.0 and pans to center the given point in the
// viewport.
func (vs *View) ResetTo(pos geom.Point) {
	vs.Zoom = 1.0
	viewportCenterX := float64(vs.Width) / 2.0
	viewportCenterY := float64(vs.Height) / 2.0
	vs.PanX = viewportCenterX - pos.X
	vs.PanY = viewportCenterY - pos.Y
}
