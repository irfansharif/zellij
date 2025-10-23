// Package memory provides GPU memory management for efficient multi-cluster rendering.
//
// The memory controller uses size-bucketed slot allocation to batch multiple clusters
// into shared VBOs, minimizing draw calls while supporting fast per-cluster updates.
package memory

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/go-gl/gl/v4.1-core/gl"
)

// TODO(irfanshari): Tune bucket sizes.
// TODO(irfansharif): Tune how 'c' maps to shapes.

var memoryLogger *log.Logger = log.New(io.Discard, "", 0)

func init() {
	if os.Getenv("ZELLIJ_DEBUG_MEMORY") == "1" {
		memoryLogger = log.New(os.Stdout, "[memory] ", log.Ltime|log.Lmsgprefix)
	}
}

// Configuration constants for memory management features.
const (
	// Defragmentation configuration. If the batch's slot utilization is less
	// than the threshold, we attempt to consolidate slots into other batches in
	// the same pool. No more than the configured number of batches will be
	// compacted per frame, to keep compaction load minimal.
	//
	// TODO(irfansharif): Would be better to instead shoot for some target
	// utilization that isn't 100%? Could be more stable.
	DefragEnableCompaction = true
	DefragThreshold        = 0.25 // 25%
	DefragMaxPerFrame      = 1

	// Dynamic growth configuration. If the batch's slot utilization is greater
	// than or equal to GrowthUtilThreshold, we allow dynamic resizing: the VBO
	// and slot array are doubled (up to GrowthMaxCycles times, or until we hit
	// the max batch size.
	GrowthEnableDynamic = true
	GrowthUtilThreshold = 0.75              // 75%
	GrowthMaxCycles     = 2                 // Start size → 2× → 4×
	GrowthMaxBatchBytes = 256 * 1024 * 1024 // 256 MiB

	// Free list configuration. If enabled, the free slot list is kept sorted by
	// (batchID, slotIndex). This makes slot allocation more predictable and
	// reduces VBO fragmentation. The idea is to keep the keeps geometry for
	// clusters in a specific regions of the VBO as clusters churn.
	FreeListEnableSorted = true

	// Bucket configuration.
	vertexCapacityS  = 1024
	vertexCapacityM  = 4096
	vertexCapacityL  = 16384
	vertexCapacityXL = 65536
	slotsPerBatchS   = 256
	slotsPerBatchM   = 128
	slotsPerBatchL   = 64
	slotsPerBatchXL  = 16
	slotsPerBatchXXL = 1
)

// BucketSize represents different size categories for cluster geometry.
type BucketSize int

const (
	BucketS   BucketSize = iota // 1K vertices (~341 triangles)
	BucketM                     // 4K vertices (~1365 triangles)
	BucketL                     // 16K vertices (~5461 triangles)
	BucketXL                    // 64K vertices (~21845 triangles)
	BucketXXL                   // Dedicated (per-cluster VBO for outliers)
)

var bucketSizes = []BucketSize{BucketS, BucketM, BucketL, BucketXL, BucketXXL}

func (bs BucketSize) String() string {
	switch bs {
	case BucketS:
		return "small"
	case BucketM:
		return "medium"
	case BucketL:
		return "large"
	case BucketXL:
		return "xlarge"
	case BucketXXL:
		return "xxlarge"
	default:
		return "unknown"
	}
}

// ClusterID uniquely identifies a cluster for memory management.
type ClusterID int

// MemoryController manages GPU memory for all clusters.
type MemoryController struct {
	buckets                 map[BucketSize]*BucketPool
	clusterSlots            map[ClusterID]*SlotAllocation
	stats                   Stats
	compactor               *Compactor
	clustersNeedingReupload map[ClusterID]bool
	nextBatchID             int
}

// Stats tracks performance metrics for the memory controller.
type Stats struct {
	TotalClusters        int
	TotalVertices        int64
	TotalGPUBytes        int64
	TotalBatches         int
	TotalSlots           int
	TotalActiveSlots     int
	TotalActiveBatches   int
	DrawCallsPerFrame    int
	BucketSizeStats      map[BucketSize]BucketSizeStats
	CompactionEvents     int
	LastCompactionTimeUs float64
	BatchDeletions       int
	SlotsRelocated       int
	GrowthEvents         int
	LastGrowthTimeUs     float64
	FreeSlots            int
}

// BucketSizeStats tracks metrics across buckets of a specific size.
type BucketSizeStats struct {
	ClusterCount  int
	BatchCount    int
	TotalSlots    int
	ActiveSlots   int
	ActiveBatches int
	FreeSlots     int
	GPUBytes      int64
	Vertices      int64
}

// Slot represents a fixed-capacity allocation within a batch.
type Slot struct {
	active       bool
	clusterID    ClusterID
	vertexCount  int
	vertexOffset int
}

// Batch represents a VBO+VAO containing multiple fixed-capacity slots.
type Batch struct {
	id                  int
	vbo                 uint32
	vao                 uint32
	totalVertexCapacity int
	slots               []Slot
	activeSlots         []int // indices of active slots in the slots array
	bucketSize          BucketSize
	growthCycles        int
	initialCapacity     int
}

// BucketPool manages batches and free slots for a specific bucket size.
type BucketPool struct {
	size                  BucketSize
	vertexCapacityPerSlot int
	slotsPerBatch         int
	batches               []*Batch
	freeSlots             []SlotRef
}

// SlotRef references a specific slot within a batch.
type SlotRef struct {
	batch     *Batch
	slotIndex int
}

// SlotAllocation records where a cluster's data is stored.
type SlotAllocation struct {
	batch       *Batch
	slotIndex   int
	vertexCount int
}

// selectBucket chooses the smallest bucket that can fit the given vertex count.
func selectBucket(vertexCount int) BucketSize {
	if vertexCount <= vertexCapacityS {
		return BucketS
	}
	if vertexCount <= vertexCapacityM {
		return BucketM
	}
	if vertexCount <= vertexCapacityL {
		return BucketL
	}
	if vertexCount <= vertexCapacityXL {
		return BucketXL
	}
	return BucketXXL
}

// vertexCapacityForBucket returns the vertex capacity for a given bucket.
func vertexCapacityForBucket(bucket BucketSize) int {
	switch bucket {
	case BucketS:
		return vertexCapacityS
	case BucketM:
		return vertexCapacityM
	case BucketL:
		return vertexCapacityL
	case BucketXL:
		return vertexCapacityXL
	case BucketXXL:
		return 0 // Handled specially in allocation
	default:
		return vertexCapacityS
	}
}

// slotsPerBatchForBucket returns the number of slots per batch for a given bucket.
func slotsPerBatchForBucket(bucket BucketSize) int {
	switch bucket {
	case BucketS:
		return slotsPerBatchS
	case BucketM:
		return slotsPerBatchM
	case BucketL:
		return slotsPerBatchL
	case BucketXL:
		return slotsPerBatchXL
	case BucketXXL:
		return slotsPerBatchXXL
	default:
		return slotsPerBatchS
	}
}

// newBucketPool creates a new bucket pool for the given bucket size.
func newBucketPool(size BucketSize) *BucketPool {
	return &BucketPool{
		size:                  size,
		vertexCapacityPerSlot: vertexCapacityForBucket(size),
		slotsPerBatch:         slotsPerBatchForBucket(size),
		batches:               make([]*Batch, 0),
		freeSlots:             make([]SlotRef, 0),
	}
}

// findFreeSlot returns a free slot from the pool, or nil if none available.
func (bp *BucketPool) findFreeSlot() *SlotRef {
	if len(bp.freeSlots) == 0 {
		return nil
	}

	if FreeListEnableSorted {
		ref := bp.freeSlots[0]
		bp.freeSlots = bp.freeSlots[1:]
		return &ref
	}

	ref := bp.freeSlots[len(bp.freeSlots)-1]
	bp.freeSlots = bp.freeSlots[:len(bp.freeSlots)-1]
	return &ref
}

// addFreeSlot adds a slot reference to the free list.
func (bp *BucketPool) addFreeSlot(ref SlotRef) {
	if !FreeListEnableSorted {
		bp.freeSlots = append(bp.freeSlots, ref)
		return
	}

	insertIdx := 0
	for i, existing := range bp.freeSlots {
		if existing.batch.id > ref.batch.id {
			insertIdx = i
			break
		}
		if existing.batch.id < ref.batch.id {
			continue
		}
		if existing.slotIndex > ref.slotIndex {
			insertIdx = i
			break
		}
		insertIdx = i + 1
	}

	if insertIdx >= len(bp.freeSlots) {
		bp.freeSlots = append(bp.freeSlots, ref)
	} else {
		bp.freeSlots = append(bp.freeSlots, SlotRef{})
		copy(bp.freeSlots[insertIdx+1:], bp.freeSlots[insertIdx:])
		bp.freeSlots[insertIdx] = ref
	}
}

// removeFromFreeList removes a specific slot reference from the free list.
func (bp *BucketPool) removeFromFreeList(batch *Batch, slotIndex int) {
	newFree := make([]SlotRef, 0, len(bp.freeSlots))
	for _, ref := range bp.freeSlots {
		if ref.batch.id == batch.id && ref.slotIndex == slotIndex {
			continue // skip this one
		}
		newFree = append(newFree, ref)
	}
	bp.freeSlots = newFree
}

// findBatchWithCapacity finds a batch with at least one available slot.
func (bp *BucketPool) findBatchWithCapacity() *Batch {
	for _, batch := range bp.batches {
		if len(batch.activeSlots) < len(batch.slots) {
			return batch
		}
	}
	return nil
}

// createBatch creates a new batch with OpenGL resources for the given bucket.
// Batch setup with VAO/VBO configuration.
func (mc *MemoryController) createBatch(bucket BucketSize, vertexCount int) (*Batch, error) {
	pool := mc.buckets[bucket]

	// For XXL buckets, use dynamic totalVertexCapacity based on actual vertex count.
	var totalVertexCapacity, numSlots int
	if bucket == BucketXXL {
		totalVertexCapacity = vertexCount
		numSlots = 1
	} else {
		totalVertexCapacity = pool.vertexCapacityPerSlot * pool.slotsPerBatch
		numSlots = pool.slotsPerBatch
	}

	// Generate OpenGL objects.
	var vao, vbo uint32
	gl.GenVertexArrays(1, &vao)
	gl.GenBuffers(1, &vbo)

	// Bind VAO. Bind VBO.
	gl.BindVertexArray(vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, vbo)

	// Allocate VBO with full capacity (6 floats per vertex: x, y, r, g, b, a)
	bufferSize := totalVertexCapacity * 6 * 4 // vertices × 6 floats × 4 bytes
	gl.BufferData(gl.ARRAY_BUFFER, bufferSize, nil, gl.DYNAMIC_DRAW)

	// Configure vertex attributes
	// - Attribute 0: position (vec2)
	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointer(0, 2, gl.FLOAT, false, 24, gl.PtrOffset(0))
	// - Attribute 1: color (vec4)
	gl.EnableVertexAttribArray(1)
	gl.VertexAttribPointer(1, 4, gl.FLOAT, false, 24, gl.PtrOffset(8))

	// Unbind.
	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	gl.BindVertexArray(0)

	// Initialize slots array.
	slots := make([]Slot, numSlots)
	if bucket == BucketXXL {
		slots[0].vertexOffset = 0 // single slot with full capacity
	} else {
		// Multiple slots with fixed offsets.
		for i := 0; i < numSlots; i++ {
			slots[i].vertexOffset = i * pool.vertexCapacityPerSlot
		}
	}

	batch := &Batch{
		id:                  mc.nextBatchID,
		vbo:                 vbo,
		vao:                 vao,
		totalVertexCapacity: totalVertexCapacity,
		slots:               slots,
		activeSlots:         make([]int, 0),
		bucketSize:          bucket,
		growthCycles:        0,
		initialCapacity:     totalVertexCapacity,
	}
	mc.nextBatchID++

	// Add batch to pool
	pool.batches = append(pool.batches, batch)
	return batch, nil
}

// allocateSlotInBatch allocates a specific slot in a batch and returns the slot index.
// Removes the allocated slot from the pool's free list if present.
func (b *Batch) allocateSlotInBatch(pool *BucketPool, clusterID ClusterID, vertexCount int) (int, error) {
	// Find first inactive slot
	for i := range b.slots {
		if !b.slots[i].active {
			b.slots[i].active = true
			b.slots[i].clusterID = clusterID
			b.slots[i].vertexCount = vertexCount
			b.activeSlots = append(b.activeSlots, i)

			// Remove from free list if present
			pool.removeFromFreeList(b, i)

			return i, nil
		}
	}
	return -1, fmt.Errorf("no available slots in batch")
}

// freeSlot marks a slot as inactive and removes it from activeSlots.
func (b *Batch) freeSlot(slotIndex int) {
	if slotIndex < 0 || slotIndex >= len(b.slots) {
		return
	}

	slot := &b.slots[slotIndex]
	slot.active = false
	slot.clusterID = 0
	slot.vertexCount = 0

	// Remove from activeSlots (swap with last, pop)
	for i, idx := range b.activeSlots {
		if idx == slotIndex {
			// Swap with last element
			lastIdx := len(b.activeSlots) - 1
			b.activeSlots[i] = b.activeSlots[lastIdx]
			// Pop last element
			b.activeSlots = b.activeSlots[:lastIdx]
			break
		}
	}
}

// cleanup releases OpenGL resources for this batch.
func (b *Batch) cleanup() {
	if b.vao != 0 {
		gl.DeleteVertexArrays(1, &b.vao)
		b.vao = 0
	}
	if b.vbo != 0 {
		gl.DeleteBuffers(1, &b.vbo)
		b.vbo = 0
	}
}

// canGrow checks if a batch is eligible for growth.
func (b *Batch) canGrow() bool {
	if !GrowthEnableDynamic {
		return false
	}
	if b.growthCycles >= GrowthMaxCycles {
		return false
	}
	if len(b.slots) == 0 {
		return false
	}
	util := float64(len(b.activeSlots)) / float64(len(b.slots))
	if util < GrowthUtilThreshold {
		return false
	}
	newCapacity := b.totalVertexCapacity * 2
	newSizeBytes := newCapacity * 6 * 4
	return newSizeBytes <= GrowthMaxBatchBytes
}

// growBatch doubles a batch's capacity by allocating a new VBO and copying data.
func (mc *MemoryController) growBatch(batch *Batch) ([]ClusterID, error) {
	if !batch.canGrow() {
		return nil, fmt.Errorf("batch cannot grow")
	}

	startTime := time.Now()

	affectedClusters := make([]ClusterID, 0, len(batch.activeSlots))
	for _, slotIdx := range batch.activeSlots {
		affectedClusters = append(affectedClusters, batch.slots[slotIdx].clusterID)
	}

	var savedVAO, savedVBO int32
	gl.GetIntegerv(gl.VERTEX_ARRAY_BINDING, &savedVAO)
	gl.GetIntegerv(gl.ARRAY_BUFFER_BINDING, &savedVBO)

	newCapacity := batch.totalVertexCapacity * 2
	newSlotCount := len(batch.slots) * 2

	var newVBO uint32
	gl.GenBuffers(1, &newVBO)
	gl.BindBuffer(gl.ARRAY_BUFFER, newVBO)

	size := newCapacity * 6 * 4
	gl.BufferData(gl.ARRAY_BUFFER, size, nil, gl.DYNAMIC_DRAW)

	gl.BindVertexArray(batch.vao)
	gl.BindBuffer(gl.ARRAY_BUFFER, newVBO)

	gl.EnableVertexAttribArray(0)
	gl.VertexAttribPointer(0, 2, gl.FLOAT, false, 24, gl.PtrOffset(0))
	gl.EnableVertexAttribArray(1)
	gl.VertexAttribPointer(1, 4, gl.FLOAT, false, 24, gl.PtrOffset(8))

	if savedVAO > 0 {
		gl.BindVertexArray(uint32(savedVAO))
	} else {
		gl.BindVertexArray(0)
	}
	if savedVBO > 0 {
		gl.BindBuffer(gl.ARRAY_BUFFER, uint32(savedVBO))
	} else {
		gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	}

	gl.Finish()

	oldVBO := batch.vbo
	gl.DeleteBuffers(1, &oldVBO)

	batch.vbo = newVBO
	batch.totalVertexCapacity = newCapacity
	batch.growthCycles++

	oldSlots := batch.slots
	batch.slots = make([]Slot, newSlotCount)
	copy(batch.slots, oldSlots)

	pool := mc.buckets[batch.bucketSize]
	vertexOffset := pool.vertexCapacityPerSlot * len(oldSlots)
	for i := len(oldSlots); i < newSlotCount; i++ {
		batch.slots[i] = Slot{
			active:       false,
			vertexOffset: vertexOffset,
		}
		vertexOffset += pool.vertexCapacityPerSlot
		pool.addFreeSlot(SlotRef{
			batch:     batch,
			slotIndex: i,
		})
	}

	mc.stats.GrowthEvents++
	mc.stats.LastGrowthTimeUs = float64(time.Since(startTime).Microseconds())
	return affectedClusters, nil
}

// NewMemoryController creates a new memory controller with initialized buckets.
func NewMemoryController() *MemoryController {
	mc := &MemoryController{
		buckets:                 make(map[BucketSize]*BucketPool),
		clusterSlots:            make(map[ClusterID]*SlotAllocation),
		clustersNeedingReupload: make(map[ClusterID]bool),
		stats: Stats{
			BucketSizeStats: make(map[BucketSize]BucketSizeStats),
		},
		compactor: newCompactor(),
	}

	mc.buckets[BucketS] = newBucketPool(BucketS)
	mc.buckets[BucketM] = newBucketPool(BucketM)
	mc.buckets[BucketL] = newBucketPool(BucketL)
	mc.buckets[BucketXL] = newBucketPool(BucketXL)
	mc.buckets[BucketXXL] = newBucketPool(BucketXXL)

	return mc
}

// EnsureSlot ensures a cluster has an allocated slot with the given vertex data.
func (mc *MemoryController) EnsureSlot(clusterID ClusterID, vertices []float32) error {
	if len(vertices) == 0 {
		return fmt.Errorf("cannot allocate empty vertex data for cluster %d", clusterID)
	}

	if len(vertices)%6 != 0 {
		return fmt.Errorf("vertex data must be multiple of 6 floats (x,y,r,g,b,a), got %d", len(vertices))
	}

	vertexCount := len(vertices) / 6
	bucketSize := selectBucket(vertexCount)

	if existing, exists := mc.clusterSlots[clusterID]; exists {
		existingBucketSize := existing.batch.bucketSize

		if existingBucketSize == BucketXXL {
			slot := &existing.batch.slots[existing.slotIndex]
			slotCapacity := existing.batch.totalVertexCapacity - slot.vertexOffset
			if vertexCount <= slotCapacity {
				return mc.updateSlotInPlace(existing, vertices, vertexCount)
			}
			if err := mc.RemoveCluster(clusterID); err != nil {
				return fmt.Errorf("failed to remove cluster %d for reallocation: %w", clusterID, err)
			}
		} else {
			pool := mc.buckets[existingBucketSize]
			if vertexCount <= pool.vertexCapacityPerSlot {
				return mc.updateSlotInPlace(existing, vertices, vertexCount)
			}
			if err := mc.RemoveCluster(clusterID); err != nil {
				return fmt.Errorf("failed to remove cluster %d for reallocation: %w", clusterID, err)
			}
		}
	}

	pool := mc.buckets[bucketSize]
	var batch *Batch
	var slotIndex int
	var err error

	for {
		freeSlot := pool.findFreeSlot()
		if freeSlot == nil {
			break
		}

		batch = freeSlot.batch
		slotIndex = freeSlot.slotIndex

		if bucketSize == BucketXXL {
			slot := &batch.slots[slotIndex]
			slotCapacity := batch.totalVertexCapacity - slot.vertexOffset
			if vertexCount > slotCapacity {
				continue
			}
		}

		batch.slots[slotIndex].active = true
		batch.slots[slotIndex].clusterID = clusterID
		batch.slots[slotIndex].vertexCount = vertexCount
		batch.activeSlots = append(batch.activeSlots, slotIndex)
		goto slot_selected
	}

	{
		batch = pool.findBatchWithCapacity()
		if batch == nil {
			if GrowthEnableDynamic && bucketSize != BucketXXL {
				for _, b := range pool.batches {
					if b.canGrow() {
						affectedClusters, err := mc.growBatch(b)
						if err == nil {
							batch = b
							mc.markClustersForReupload(affectedClusters)
							break
						}
					}
				}
			}

			if batch == nil {
				batch, err = mc.createBatch(bucketSize, vertexCount)
				if err != nil {
					return fmt.Errorf("failed to create batch for bucket %s: %w", bucketSize, err)
				}
			}
		}

		if bucketSize == BucketXXL {
			if vertexCount > batch.totalVertexCapacity {
				batch, err = mc.createBatch(bucketSize, vertexCount)
				if err != nil {
					return fmt.Errorf("failed to create XXL batch for %d vertices: %w", vertexCount, err)
				}
			}
		}

		slotIndex, err = batch.allocateSlotInBatch(pool, clusterID, vertexCount)
		if err != nil {
			return fmt.Errorf("failed to allocate slot in batch: %w", err)
		}
	}

slot_selected:

	slot := &batch.slots[slotIndex]
	if err := mc.uploadVertexData(batch, slot, vertices); err != nil {
		return fmt.Errorf("failed to upload vertex data: %w", err)
	}

	mc.clusterSlots[clusterID] = &SlotAllocation{
		batch:       batch,
		slotIndex:   slotIndex,
		vertexCount: vertexCount,
	}

	return nil
}

// updateSlotInPlace updates an existing slot's vertex data without reallocation.
func (mc *MemoryController) updateSlotInPlace(alloc *SlotAllocation, vertices []float32, vertexCount int) error {
	slot := &alloc.batch.slots[alloc.slotIndex]
	slot.vertexCount = vertexCount
	return mc.uploadVertexData(alloc.batch, slot, vertices)
}

// uploadVertexData uploads vertex data to the GPU at the slot's offset.
func (mc *MemoryController) uploadVertexData(batch *Batch, slot *Slot, vertices []float32) error {
	gl.BindBuffer(gl.ARRAY_BUFFER, batch.vbo)
	byteOffset := slot.vertexOffset * 6 * 4 // vertices × 6 floats × 4 bytes
	byteSize := len(vertices) * 4           // vertices × 4 bytes
	gl.BufferSubData(gl.ARRAY_BUFFER, byteOffset, byteSize, gl.Ptr(vertices))
	gl.BindBuffer(gl.ARRAY_BUFFER, 0)
	return nil
}

// RemoveCluster removes a cluster's allocation and frees its slot.
func (mc *MemoryController) RemoveCluster(clusterID ClusterID) error {
	alloc, exists := mc.clusterSlots[clusterID]
	if !exists {
		return fmt.Errorf("cluster %d not found", clusterID)
	}

	batch := alloc.batch
	batch.freeSlot(alloc.slotIndex)

	bucket := batch.bucketSize
	pool := mc.buckets[bucket]
	pool.addFreeSlot(SlotRef{
		batch:     batch,
		slotIndex: alloc.slotIndex,
	})

	delete(mc.clusterSlots, clusterID)
	return nil
}

// ValidateClusterIntegrity checks that all tracked clusters have valid batch
// references.
func (mc *MemoryController) ValidateClusterIntegrity() error {
	var errors []string

	for clusterID, alloc := range mc.clusterSlots {
		pool := mc.buckets[alloc.batch.bucketSize]
		batchExists := false
		for _, b := range pool.batches {
			if b.id == alloc.batch.id {
				batchExists = true
				break
			}
		}

		if !batchExists {
			errors = append(errors, fmt.Sprintf("Cluster %d references deleted batch %d", clusterID, alloc.batch.id))
			continue
		}

		if alloc.slotIndex >= len(alloc.batch.slots) {
			errors = append(errors, fmt.Sprintf("Cluster %d has invalid slot index %d (batch has %d slots)",
				clusterID, alloc.slotIndex, len(alloc.batch.slots)))
			continue
		}

		slot := &alloc.batch.slots[alloc.slotIndex]
		if !slot.active {
			errors = append(errors, fmt.Sprintf("Cluster %d references inactive slot %d in batch %d",
				clusterID, alloc.slotIndex, alloc.batch.id))
		}
		if slot.clusterID != clusterID {
			errors = append(errors, fmt.Sprintf("Cluster %d slot mismatch: slot points to cluster %d",
				clusterID, slot.clusterID))
		}
	}

	if len(errors) > 0 {
		log.Printf("cluster integrity check failed with %d errors:", len(errors))
		for _, err := range errors {
			log.Printf("  - %s", err)
		}
		return fmt.Errorf("cluster integrity check failed with %d errors", len(errors))
	}

	return nil
}

// Draw renders all active clusters using MultiDrawArrays.
func (mc *MemoryController) Draw() error {
	drawCalls := 0

	buckets := []BucketSize{BucketS, BucketM, BucketL, BucketXL, BucketXXL}

	for _, bucketSize := range buckets {
		pool := mc.buckets[bucketSize]

		for _, batch := range pool.batches {
			if len(batch.activeSlots) == 0 {
				continue
			}

			gl.BindVertexArray(batch.vao)

			firsts := make([]int32, len(batch.activeSlots))
			counts := make([]int32, len(batch.activeSlots))

			for i, slotIdx := range batch.activeSlots {
				slot := batch.slots[slotIdx]
				firsts[i] = int32(slot.vertexOffset)
				counts[i] = int32(slot.vertexCount)
			}

			gl.MultiDrawArrays(gl.TRIANGLES, &firsts[0], &counts[0], int32(len(firsts)))
			drawCalls++
		}
	}

	gl.BindVertexArray(0)
	mc.stats.DrawCallsPerFrame = drawCalls
	return nil
}

// Cleanup releases all OpenGL resources.
func (mc *MemoryController) Cleanup() {
	for _, pool := range mc.buckets {
		for _, batch := range pool.batches {
			batch.cleanup()
		}
	}
}

// Stats returns current memory statistics.
func (mc *MemoryController) Stats() Stats {
	mc.updateStats()
	return mc.stats
}

// updateStats recalculates statistics from current state.
func (mc *MemoryController) updateStats() {
	mc.stats.TotalClusters = len(mc.clusterSlots)
	mc.stats.TotalVertices = 0
	mc.stats.TotalGPUBytes = 0
	mc.stats.TotalBatches = 0
	mc.stats.TotalSlots = 0
	mc.stats.TotalActiveSlots = 0
	mc.stats.TotalActiveBatches = 0

	for bucketSize, pool := range mc.buckets {
		bucketStats := pool.calculateStats()

		mc.stats.TotalBatches += bucketStats.BatchCount
		mc.stats.TotalGPUBytes += bucketStats.GPUBytes
		mc.stats.TotalVertices += bucketStats.Vertices
		mc.stats.TotalSlots += bucketStats.TotalSlots
		mc.stats.TotalActiveSlots += bucketStats.ActiveSlots
		mc.stats.TotalActiveBatches += bucketStats.ActiveBatches

		mc.stats.BucketSizeStats[bucketSize] = bucketStats
	}

	freeSlots := 0
	for _, pool := range mc.buckets {
		freeSlots += len(pool.freeSlots)
	}
	mc.stats.FreeSlots = freeSlots
}

// PrintStats outputs memory statistics with visual bars.
func (mc *MemoryController) PrintStats() {
	stats := mc.Stats()

	// Compute aggregate utilization metrics
	slotsUtil := 0.0
	if stats.TotalSlots > 0 {
		slotsUtil = float64(stats.TotalActiveSlots) / float64(stats.TotalSlots)
	}
	batchUtil := 0.0
	if stats.TotalBatches > 0 {
		batchUtil = float64(stats.TotalActiveBatches) / float64(stats.TotalBatches)
	}

	memoryLogger.Println("===== Memory Controller Stats =====")
	memoryLogger.Printf("%d compactions (%d slots relocated, %d batches deleted, %.2fμs last), %d growth events (%.2fμs last)",
		stats.CompactionEvents, stats.SlotsRelocated, stats.BatchDeletions, stats.LastCompactionTimeUs,
		stats.GrowthEvents, stats.LastGrowthTimeUs,
	)
	memoryLogger.Printf("%.1f%% slots active (%d/%d), %.1f%% batches active (%d/%d), %d free-list slots, %s GPU, %d clusters (%s triangles, %s vertices)",
		slotsUtil*100,
		stats.TotalActiveSlots,
		stats.TotalSlots,
		batchUtil*100,
		stats.TotalActiveBatches,
		stats.TotalBatches,
		stats.FreeSlots,
		formatNumber(stats.TotalGPUBytes),
		stats.TotalClusters,
		formatNumber(stats.TotalVertices/3),
		formatNumber(stats.TotalVertices),
	)

	for _, bucketSize := range bucketSizes {
		if bucketSizeStats, ok := stats.BucketSizeStats[bucketSize]; ok && bucketSizeStats.BatchCount > 0 {
			slotsUtil := 0.0
			if bucketSizeStats.TotalSlots > 0 {
				slotsUtil = float64(bucketSizeStats.ActiveSlots) / float64(bucketSizeStats.TotalSlots)
			}

			batchUtil := 0.0
			if bucketSizeStats.BatchCount > 0 {
				batchUtil = float64(bucketSizeStats.ActiveBatches) / float64(bucketSizeStats.BatchCount)
			}

			slotsUtilBar := makeUtilizationBar(slotsUtil, 12)
			memoryLogger.Printf("  [%8s] %s %.0f%% slots active (%d/%d), %.0f%% batches active (%d/%d), %d free-list slots, %s GPU (%s triangles, %s vertices)\n",
				bucketSize.String(),
				slotsUtilBar,
				slotsUtil*100,
				bucketSizeStats.ActiveSlots,
				bucketSizeStats.TotalSlots,
				batchUtil*100,
				bucketSizeStats.ActiveBatches,
				bucketSizeStats.BatchCount,
				bucketSizeStats.FreeSlots,
				formatNumber(bucketSizeStats.GPUBytes),
				formatNumber(bucketSizeStats.Vertices/3),
				formatNumber(bucketSizeStats.Vertices))

			// Show individual batch details.
			pool := mc.buckets[bucketSize]
			if pool != nil && len(pool.batches) > 0 {
				for _, batch := range pool.batches {
					activeSlots := len(batch.activeSlots)
					totalSlots := len(batch.slots)
					batchSlotsUtil := 0.0
					if totalSlots > 0 {
						batchSlotsUtil = float64(activeSlots) / float64(totalSlots)
					}

					batchGPUBytes := int64(batch.totalVertexCapacity * 6 * 4) // vertices × 6 floats × 4 bytes
					batchVertices := int64(0)
					for _, slotIdx := range batch.activeSlots {
						if slotIdx < len(batch.slots) {
							batchVertices += int64(batch.slots[slotIdx].vertexCount)
						}
					}

					batchSlotsUtilBar := makeUtilizationBar(batchSlotsUtil, 8)
					memoryLogger.Printf("      batch#%03d  %s %.0f%% slots active (%d/%d), %s GPU (%s triangles, %s vertices), %d× growth (%s -> %s)",
						batch.id,
						batchSlotsUtilBar,
						batchSlotsUtil*100,
						activeSlots,
						totalSlots,
						formatNumber(batchGPUBytes),
						formatNumber(batchVertices/3),
						formatNumber(batchVertices),
						batch.growthCycles+1,
						formatNumber(int64(batch.initialCapacity)),
						formatNumber(int64(batch.totalVertexCapacity)),
					)
				}
			}
		}
	}
	memoryLogger.Println("===================================")
}

// makeUtilizationBar creates a visual bar for utilization percentage.
func makeUtilizationBar(utilization float64, width int) string {
	if utilization < 0 {
		utilization = 0
	}
	if utilization > 1 {
		utilization = 1
	}

	filled := int(utilization * float64(width))
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return bar
}

// formatNumber formats large numbers with K/M suffixes for readability.
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fK", float64(n)/1000.0)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000.0)
}

// markClustersForReupload marks clusters as needing re-upload after batch growth.
func (mc *MemoryController) markClustersForReupload(clusterIDs []ClusterID) {
	for _, id := range clusterIDs {
		mc.clustersNeedingReupload[id] = true
	}
}

// GetAndClearClustersNeedingReupload returns clusters affected by batch growth and clears the list.
func (mc *MemoryController) GetAndClearClustersNeedingReupload() []ClusterID {
	if len(mc.clustersNeedingReupload) == 0 {
		return nil
	}

	result := make([]ClusterID, 0, len(mc.clustersNeedingReupload))
	for clusterID := range mc.clustersNeedingReupload {
		result = append(result, clusterID)
	}

	mc.clustersNeedingReupload = make(map[ClusterID]bool)
	return result
}

// TryCompaction attempts to compact sparse batches if enabled. Should be called
// periodically (e.g., every 60 frames). Limits compaction to DefragMaxPerFrame
// batches per call.
func (mc *MemoryController) TryCompaction() error {
	if mc.compactor == nil || !DefragEnableCompaction {
		return nil
	}

	// Scan for compaction candidates.
	candidates := mc.compactor.ScanForCompaction(mc.buckets)
	if len(candidates) == 0 {
		return nil
	}

	startTime := time.Now()
	compactedBatches, deletedBatches := 0, 0
	for _, batch := range candidates {
		if compactedBatches >= DefragMaxPerFrame {
			compactionLogger.Printf("reached max compactions (%d) per frame, skipping %d remaining candidates", DefragMaxPerFrame, len(candidates)-compactedBatches)
			break
		}

		compactionLogger.Printf("processing Batch#%d (%s) - %d active slots",
			batch.id, batch.bucketSize.String(), len(batch.activeSlots))

		// Handle empty batches - delete immediately without compaction.
		if len(batch.activeSlots) == 0 {
			compactionLogger.Printf("batch#%d is empty, deleting immediately", batch.id)
			if err := mc.deleteBatch(batch); err != nil {
				compactionLogger.Printf("failed to delete empty batch %d: %v", batch.id, err)
			} else {
				compactionLogger.Printf("successfully deleted empty batch %d", batch.id)
				compactedBatches += 1
				deletedBatches += 1
				mc.stats.CompactionEvents++
				mc.stats.BatchDeletions++
			}
		} else {
			// Handle sparse batches - compact first, then delete if empty.
			deletable, slotsRelocated, err := mc.compactor.CompactBatch(mc, batch)
			if err != nil {
				compactionLogger.Printf("Failed to compact batch %d: %v", batch.id, err)
				continue
			}

			if slotsRelocated > 0 {
				compactedBatches += 1
				mc.stats.CompactionEvents++
				mc.stats.SlotsRelocated += slotsRelocated
			}

			if deletable {
				// TODO(irfansharif): Improve the loop structure here - pretty
				// sure we don't need this. Empty batches should appear in the
				// subsequent compaction pass and get cleared out above.
				compactionLogger.Printf("batch#%d is now empty after compaction, attempting deletion", batch.id)
				if err := mc.deleteBatch(batch); err != nil {
					compactionLogger.Printf("Failed to delete compacted batch %d: %v", batch.id, err)
				} else {
					compactionLogger.Printf("Deleted empty batch %d after compaction", batch.id)
					deletedBatches += 1

					mc.stats.BatchDeletions++
				}
			} else {
				compactionLogger.Printf("batch#%d still has active slots after compaction", batch.id)
			}
		}
	}

	compactionLogger.Printf("completed: processed %d candidates, compacted %d batches, deleted %d empty batches",
		len(candidates), compactedBatches, deletedBatches)

	if compactedBatches > 0 || deletedBatches > 0 {
		mc.stats.LastCompactionTimeUs = float64(time.Since(startTime).Microseconds())
	}
	return nil
}

// deleteBatch removes a batch and frees its OpenGL resources.
func (mc *MemoryController) deleteBatch(batch *Batch) error {
	// SAFETY CHECK: Verify batch is actually empty.
	if len(batch.activeSlots) > 0 {
		compactionLogger.Printf("attempting to delete batch %d with %d active slots!", batch.id, len(batch.activeSlots))

		// Log which clusters are still in this batch.
		for _, slotIdx := range batch.activeSlots {
			slot := &batch.slots[slotIdx]
			compactionLogger.Printf("  - Slot %d: cluster %d, %d vertices", slotIdx, slot.clusterID, slot.vertexCount)
		}
		return fmt.Errorf("cannot delete batch %d: still has %d active clusters", batch.id, len(batch.activeSlots))
	}

	// Remove from bucket pool.
	compactionLogger.Printf("deleting empty batch %d from bucket %s", batch.id, batch.bucketSize)
	pool := mc.buckets[batch.bucketSize]
	for i, b := range pool.batches {
		if b.id == batch.id {
			pool.batches = append(pool.batches[:i], pool.batches[i+1:]...)
			break
		}
	}

	// Remove any free slots pointing to this batch.
	removedFreeSlots := 0
	newFreeSlots := make([]SlotRef, 0, len(pool.freeSlots))
	for _, ref := range pool.freeSlots {
		if ref.batch.id != batch.id {
			newFreeSlots = append(newFreeSlots, ref)
		} else {
			removedFreeSlots++
		}
	}
	pool.freeSlots = newFreeSlots
	if removedFreeSlots > 0 {
		compactionLogger.Printf("removed %d free slot references for batch %d", removedFreeSlots, batch.id)
	}

	// Cleanup OpenGL resources.
	batch.cleanup()
	return nil
}

// calculateStats computes statistics for this bucket pool.
func (bp *BucketPool) calculateStats() BucketSizeStats {
	stats := BucketSizeStats{
		BatchCount: len(bp.batches),
		FreeSlots:  len(bp.freeSlots),
	}

	for _, batch := range bp.batches {
		batchBytes := batch.totalVertexCapacity * 6 * 4
		stats.GPUBytes += int64(batchBytes)

		stats.TotalSlots += len(batch.slots)
		stats.ActiveSlots += len(batch.activeSlots)

		if len(batch.activeSlots) > 0 {
			stats.ActiveBatches++
		}

		for _, slotIdx := range batch.activeSlots {
			slot := batch.slots[slotIdx]
			stats.Vertices += int64(slot.vertexCount)
			stats.ClusterCount++
		}
	}

	return stats
}
