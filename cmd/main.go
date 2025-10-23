package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/go-gl/gl/v4.1-core/gl"
	"github.com/go-gl/glfw/v3.3/glfw"

	"github.com/irfansharif/zellij/internal/app"
	"github.com/irfansharif/zellij/internal/gen"
	"github.com/irfansharif/zellij/internal/memory"
	"github.com/irfansharif/zellij/internal/render"
)

const logFlags = log.Ltime | log.Lshortfile

var runtimeLogger *log.Logger = log.New(io.Discard, "", 0)

func init() {
	// OpenGL contexts are tied to specific OS threads - let's pin to just one.
	runtime.LockOSThread()
	log.SetFlags(logFlags)

	if os.Getenv("ZELLIJ_DEBUG_RUNTIME") == "1" {
		runtimeLogger = log.New(os.Stdout, "[runtime] ", log.Ltime|log.Lmsgprefix)
	}
}

func makeTitle(fps float64, avgFrameTime float64, renderStats render.Stats, memStats memory.Stats) string {
	return fmt.Sprintf("Zellij (%.1f FPS, %.2fms/frame, %d clusters, %d triangles, %.2fM triangles/sec, %d draw calls/frame, %.2fµs/draw, %.2fms/prepare, %.1fMiB GPU)",
		fps,
		avgFrameTime,
		memStats.TotalClusters,
		memStats.TotalVertices/3,
		fps*float64(memStats.TotalVertices/3)/1000000.0,
		memStats.DrawCallsPerFrame,
		renderStats.LastDrawTimeUs,
		renderStats.LastPrepareTimeMs,
		float64(memStats.TotalGPUBytes)/(1024.0*1024.0),
	)
}

func main() {
	flag.Parse()

	if err := glfw.Init(); err != nil {
		log.Fatalf("Failed to initialize GLFW: %v", err)
	}
	defer glfw.Terminate()

	// Configure GLFW window hints - use OpenGL 4.1.
	glfw.DefaultWindowHints()
	glfw.WindowHint(glfw.Resizable, glfw.True)
	glfw.WindowHint(glfw.OpenGLProfile, glfw.OpenGLCoreProfile)
	glfw.WindowHint(glfw.OpenGLForwardCompatible, glfw.True)
	glfw.WindowHint(glfw.ContextVersionMajor, 4)
	glfw.WindowHint(glfw.ContextVersionMinor, 1)

	window, err := glfw.CreateWindow(
		1280, // width
		960,  // height
		"Zellij",
		nil, nil,
	)
	if err != nil {
		log.Fatalf("Failed to create window: %v", err)
	}
	window.MakeContextCurrent()

	if err := gl.Init(); err != nil {
		log.Fatalf("Failed to initialize OpenGL: %v", err)
	}

	s := seed()
	generator := gen.NewGenerator()
	generator.SetFeaturesForComplexity(rand.New(rand.NewSource(s)), nil /* complexity */)

	cw, ch := window.GetFramebufferSize()
	application := app.NewApp(
		window,
		generator,
		app.NewView(cw, ch),
		s,
	)

	// Create initial cluster manually.
	centerX, centerY := float64(cw)/2.0, float64(ch)/2.0 // center of the canvas
	application.CreateCluster(centerX, centerY, nil /* complexity */)
	application.PrepareRenderer(cw, ch)

	// Initialize event handlers.
	eventHandlers := NewEventHandlers(application)

	frameCount, frameTimeSum := 0, 0.0
	lastFPSUpdate := time.Now()

	// Main loop.
	for !application.Window.ShouldClose() {
		frameStart := time.Now()

		eventHandlers.handleContinuousRegeneration()
		eventHandlers.handleContinuousPanning()

		w, h := application.Window.GetFramebufferSize()
		gl.Viewport(0, 0, int32(w), int32(h))
		gl.ClearColor(1, 1, 1, 1)
		gl.Clear(gl.COLOR_BUFFER_BIT)

		application.Renderer.Draw()
		application.Window.SwapBuffers()
		glfw.PollEvents()

		frameTime := time.Since(frameStart).Seconds() * 1000.0 // ms
		frameTimeSum += frameTime

		frameCount++
		now := time.Now()
		if now.Sub(lastFPSUpdate) >= time.Second {
			fps := float64(frameCount) / now.Sub(lastFPSUpdate).Seconds()
			avgFrameTime := frameTimeSum / float64(frameCount)
			frameCount, frameTimeSum = 0, 0.0
			lastFPSUpdate = now

			memStats := application.MemoryController.Stats()
			renderStats := application.Renderer.Stats()

			application.Window.SetTitle(
				makeTitle(fps, avgFrameTime, renderStats, memStats),
			)

			runtimeLogger.Println("=== Performance statistics ===")
			runtimeLogger.Printf("Frame rate:     %.1f FPS (%.2f ms/frame, %d draw calls/frame)", fps, avgFrameTime, memStats.DrawCallsPerFrame)
			runtimeLogger.Printf("Shapes:         %d clusters, %d triangles, %d vertices", memStats.TotalClusters, memStats.TotalVertices/3, memStats.TotalVertices)
			runtimeLogger.Printf("GPU memory:     %.2f MiB", float64(memStats.TotalGPUBytes)/(1024.0*1024.0))
			runtimeLogger.Printf("Render time:    %.2f µs (last draw), %.2f ms (last prepare)", renderStats.LastDrawTimeUs, renderStats.LastPrepareTimeMs)
			runtimeLogger.Printf("Compaction:     %d events (%d slots relocated, %d batches deleted), %.2f μs (last)", memStats.CompactionEvents, memStats.SlotsRelocated, memStats.BatchDeletions, memStats.LastCompactionTimeUs)
			runtimeLogger.Printf("Throughput:     %.2f M vertices/sec", fps*float64(memStats.TotalVertices)/1000000.0)
			runtimeLogger.Println("==============================")

			application.MemoryController.PrintStats()
		}

		if frameCount%60 == 0 { // Periodic compaction.
			if err := application.MemoryController.TryCompaction(); err != nil {
				log.Fatalf("Compaction error: %v", err)
			}
		}

		if frameCount%100 == 0 { // Periodically validate cluster integrity.
			if err := application.MemoryController.ValidateClusterIntegrity(); err != nil {
				log.Fatalf("Cluster integrity invalid: %v", err)
			}
		}
	}
}

func seed() int64 {
	seedStr := os.Getenv("ZELLIJ_SEED")
	now := time.Now().Unix()
	if seedStr == "" {
		return now
	}
	seed, err := strconv.ParseInt(seedStr, 10, 64)
	if err != nil {
		log.Fatalf("Invalid ZELLIJ_SEED value '%s': %v", seedStr, err)
	}
	return seed
}
