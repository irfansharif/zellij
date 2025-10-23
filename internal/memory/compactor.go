package memory

import (
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/go-gl/gl/v4.1-core/gl"
)

var compactionLogger *log.Logger = log.New(io.Discard, "", 0)

func init() {
	if os.Getenv("ZELLIJ_DEBUG_COMPACTION") == "1" {
		compactionLogger = log.New(os.Stdout, "[compaction] ", log.Ltime|log.Lmsgprefix)
	}
}

// Compactor manages batch defragmentation.
type Compactor struct{}

func newCompactor() *Compactor {
	return &Compactor{}
}

// ScanForCompaction identifies sparse batches that need compaction. Returns
// batches sorted by sparseness (i.e., lowest utilization first).
func (c *Compactor) ScanForCompaction(buckets map[BucketSize]*BucketPool) []*Batch {
	if !DefragEnableCompaction {
		return nil
	}

	compactionLogger.Printf("scanning across %d buckets for candidates (min-util=%.1f%%)", len(buckets), DefragThreshold*100)

	var candidates []*Batch
	totalBatches, emptyBatches := 0, 0
	for _, bucketSize := range bucketSizes {
		pool := buckets[bucketSize]
		if pool == nil {
			continue // no buckets of that class - nothing to do
		}

		for i, batch := range pool.batches {
			totalBatches++

			if len(batch.slots) == 0 {
				compactionLogger.Printf("[%s] batch[%d/%d]#%d - skipping (no slots)", bucketSize.String(), i+1, len(pool.batches), batch.id)
				continue
			}

			util := float64(len(batch.activeSlots)) / float64(len(batch.slots))
			if util < DefragThreshold {
				isEmpty := len(batch.activeSlots) == 0
				if isEmpty {
					emptyBatches++
				}

				candidates = append(candidates, batch)
				compactionLogger.Printf("[%s] batch[%d/%d]#%d - CANDIDATE (%.1f%% util, %d/%d slots active)",
					bucketSize.String(), i+1, len(pool.batches), batch.id, util*100, len(batch.activeSlots), len(batch.slots))
			} else {
				compactionLogger.Printf("[%s] batch[%d/%d]#%d - TOO DENSE (%.1f%% util, %d/%d slots active)",
					bucketSize.String(), i+1, len(pool.batches), batch.id, util*100, len(batch.activeSlots), len(batch.slots))
			}
		}
	}

	compactionLogger.Printf("scan completed with %d total batches, %d empty, %d candidates for compaction",
		totalBatches, emptyBatches, len(candidates))

	// Sort by sparseness (lowest utilization first).
	sort.Slice(candidates, func(i, j int) bool {
		utilI := float64(len(candidates[i].activeSlots)) / float64(len(candidates[i].slots))
		utilJ := float64(len(candidates[j].activeSlots)) / float64(len(candidates[j].slots))
		return utilI < utilJ
	})
	return candidates
}

// CompactBatch moves active slots from source batch to other batches in the
// same bucket. Returns if it's fully emptied and can be deleted, and the
// number of slots relocated.
func (c *Compactor) CompactBatch(
	mc *MemoryController,
	sourceBatch *Batch,
) (deletable bool, slotsRelocated int, err error) {
	if !DefragEnableCompaction {
		return false, 0, nil // nothing to do.
	}

	pool := mc.buckets[sourceBatch.bucketSize]

	// Find target batches with free space (excluding source).
	var targets []*Batch
	for _, batch := range pool.batches {
		if batch.id == sourceBatch.id {
			continue
		}
		if len(batch.activeSlots) < len(batch.slots) {
			targets = append(targets, batch)
		}
	}

	if len(targets) == 0 {
		return false, 0, nil // no target batches, can't compact
	}

	// Copy active slots to target batches.
	slotsToMove := make([]int, len(sourceBatch.activeSlots))
	copy(slotsToMove, sourceBatch.activeSlots)

	movedCount := 0
	for _, slotIdx := range slotsToMove {
		slot := &sourceBatch.slots[slotIdx]

		// Find target batch with space.
		var targetBatch *Batch
		for _, t := range targets {
			if len(t.activeSlots) < len(t.slots) {
				targetBatch = t
				break
			}
		}

		if targetBatch == nil {
			break // no more space in targets
		}

		// Copy slot data.
		if err := c.copySlotData(mc, sourceBatch, slot, targetBatch); err != nil {
			compactionLogger.Printf("failed to copy slot data during compaction: %v", err)
			return false, 0, err
		}

		movedCount++
	}

	empty := len(sourceBatch.activeSlots) == 0
	return empty, movedCount, nil
}

// copySlotData copies vertex data from source to target batch using CPU-side
// copy. Updates the cluster's allocation record to point to the new location.
func (c *Compactor) copySlotData(
	mc *MemoryController,
	sourceBatch *Batch,
	sourceSlot *Slot,
	targetBatch *Batch,
) error {
	// Allocate slot in target batch
	targetPool := mc.buckets[targetBatch.bucketSize]
	targetSlotIdx, err := targetBatch.allocateSlotInBatch(targetPool, sourceSlot.clusterID, sourceSlot.vertexCount)
	if err != nil {
		return err
	}

	targetSlot := &targetBatch.slots[targetSlotIdx]

	// CPU-side copy.
	// TODO(irfansharif): It'd be better to try and use glCopyBufferSubData, but
	// it's unsupported on OpenGL 4.1.
	srcOffset := sourceSlot.vertexOffset * 6 * 4 // vertices × 6 floats × 4 bytes
	dstOffset := targetSlot.vertexOffset * 6 * 4
	size := sourceSlot.vertexCount * 6 * 4

	// Read from source VBO.
	tempData := make([]float32, sourceSlot.vertexCount*6)
	gl.BindBuffer(gl.ARRAY_BUFFER, sourceBatch.vbo)
	gl.GetBufferSubData(gl.ARRAY_BUFFER, srcOffset, size, gl.Ptr(tempData))

	// Write to target VBO.
	gl.BindBuffer(gl.ARRAY_BUFFER, targetBatch.vbo)
	gl.BufferSubData(gl.ARRAY_BUFFER, dstOffset, size, gl.Ptr(tempData))

	gl.BindBuffer(gl.ARRAY_BUFFER, 0)

	// Update cluster's allocation record.
	alloc := mc.clusterSlots[sourceSlot.clusterID]
	if alloc == nil {
		log.Printf("No allocation found for cluster %d during compaction!", sourceSlot.clusterID)
		return fmt.Errorf("cluster %d has no allocation record", sourceSlot.clusterID)
	}

	oldSlotIndex := -1
	for i, idx := range sourceBatch.activeSlots {
		if sourceBatch.slots[idx].clusterID == sourceSlot.clusterID {
			oldSlotIndex = i
			break
		}
	}

	alloc.batch = targetBatch
	alloc.slotIndex = targetSlotIdx
	alloc.vertexCount = sourceSlot.vertexCount

	// Mark source slot as inactive.
	sourceSlot.active = false
	sourceSlot.clusterID = 0
	sourceSlot.vertexCount = 0

	// Remove from active slots.
	if oldSlotIndex >= 0 {
		sourceBatch.activeSlots = append(sourceBatch.activeSlots[:oldSlotIndex], sourceBatch.activeSlots[oldSlotIndex+1:]...)
	} else {
		compactionLogger.Printf("WARNING: Could not find cluster %d in source batch %d active slots", sourceSlot.clusterID, sourceBatch.id)
	}

	// Add freed slot to free list so it can potentially be reused.
	sourcePool := mc.buckets[sourceBatch.bucketSize]
	slotIdx := -1
	for i := range sourceBatch.slots {
		if &sourceBatch.slots[i] == sourceSlot {
			slotIdx = i
			break
		}
	}
	if slotIdx >= 0 {
		sourcePool.addFreeSlot(SlotRef{
			batch:     sourceBatch,
			slotIndex: slotIdx,
		})
	}

	return nil
}
