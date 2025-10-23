package app

import (
	"math"
	"sort"

	"github.com/irfansharif/zellij/internal/gen"
	"github.com/irfansharif/zellij/internal/geom"
	"github.com/irfansharif/zellij/internal/memory"
)

// Cluster represents a single cluster rendering with its position and metadata.
type Cluster struct {
	ID          memory.ClusterID // unique identifier
	GridBounds  geom.Box         // bounding box in integer grid space
	CanvasPos   geom.Point       // position in canvas coordinates
	Composition gen.Composition  // generated pattern
	Seed        int64            // seed used for generation (for reproducibility)
	Complexity  *int             // complexity level, nil for default randomization
	Dirty       bool             // marks cluster for GPU re-upload
}

// SetComposition updates the cluster's composition and marks it dirty.
func (c *Cluster) SetComposition(comp gen.Composition) {
	c.Composition = comp
	c.Dirty = true
}

// SetSeed updates the cluster's seed.
func (c *Cluster) SetSeed(seed int64) {
	c.Seed = seed
}

func (c *Cluster) SetComplexity(complexity *int) {
	c.Complexity = complexity
}

// ClusterManager manages multiple clusters across the canvas.
type ClusterManager struct {
	clusters         map[memory.ClusterID]*Cluster // map of cluster IDs to clusters
	currentClusterID memory.ClusterID              // ID of the current cluster
	currentSeed      int64                         // current seed
	nextID           memory.ClusterID              // next cluster ID to assign
}

// NewClusterManager creates a new cluster manager.
func NewClusterManager(seed int64) *ClusterManager {
	return &ClusterManager{
		clusters:         make(map[memory.ClusterID]*Cluster),
		currentClusterID: -1,
		currentSeed:      seed,
	}
}

// AddCluster adds a new cluster to the manager.
func (cm *ClusterManager) AddCluster(gridBounds geom.Box, canvasPos geom.Point, comp gen.Composition, seed int64, complexity *int) *Cluster {
	cluster := &Cluster{
		ID:          cm.nextID,
		GridBounds:  gridBounds,
		CanvasPos:   canvasPos,
		Composition: comp,
		Seed:        seed,
		Complexity:  complexity,
		Dirty:       true, // New clusters always need upload
	}
	cm.clusters[cluster.ID] = cluster
	cm.nextID++
	return cluster
}

// RemoveCluster removes a cluster by ID.
func (cm *ClusterManager) RemoveCluster(id memory.ClusterID) bool {
	if _, ok := cm.clusters[id]; ok {
		delete(cm.clusters, id)
		return true
	}
	return false
}

// GetClusters returns all clusters sorted by ID (ascending).
func (cm *ClusterManager) GetClusters() []*Cluster {
	clusters := make([]*Cluster, 0, len(cm.clusters))
	for _, cluster := range cm.clusters {
		clusters = append(clusters, cluster)
	}
	sort.SliceStable(clusters, func(i, j int) bool { return clusters[i].ID < clusters[j].ID })
	return clusters
}

// FindClosestClusters returns all clusters sorted by distance to the given point (closest first).
// For clusters at equal distance, sorts by ID (highest first).
func (cm *ClusterManager) FindClosestClusters(canvasX, canvasY float64) []*Cluster {
	// Find all clusters and sort by distance to given coordinates
	type sortKey struct {
		distance float64
		ID       memory.ClusterID
	}

	var sortKeys []sortKey
	for _, cluster := range cm.clusters {
		dx := cluster.CanvasPos.X - canvasX
		dy := cluster.CanvasPos.Y - canvasY
		distance := math.Sqrt(dx*dx + dy*dy)
		sortKeys = append(sortKeys, sortKey{distance, cluster.ID})
	}

	// Sort by distance (closest first), then by ID (highest first) for ties.
	sort.Slice(sortKeys, func(i, j int) bool {
		if math.Abs(sortKeys[i].distance-sortKeys[j].distance) < 1e-4 {
			return sortKeys[i].ID > sortKeys[j].ID
		}
		return sortKeys[i].distance < sortKeys[j].distance
	})

	result := make([]*Cluster, len(sortKeys))
	for i, sortKey := range sortKeys {
		result[i] = cm.clusters[sortKey.ID]
	}
	return result
}

// SetCurrentCluster sets the current cluster directly.
func (cm *ClusterManager) SetCurrentCluster(cluster *Cluster) {
	if cluster == nil {
		cm.currentClusterID = -1
	} else {
		cm.currentClusterID = cluster.ID
	}
}

// IncrementSeed increments the seed by 1 and returns it.
func (cm *ClusterManager) IncrementSeed() int64 {
	cm.currentSeed++
	return cm.currentSeed
}

// IterCluster iterates to the next or previous cluster based on sorted cluster
// IDs (typically creation order).
func (cm *ClusterManager) IterCluster(next bool) *Cluster {
	if len(cm.clusters) == 0 {
		cm.currentClusterID = -1
		return nil
	}

	direction := 1
	if !next {
		direction = -1
	}

	ids := make([]memory.ClusterID, 0, len(cm.clusters))
	for id := range cm.clusters {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var currentID memory.ClusterID
	if cm.currentClusterID >= 0 {
		currentID = cm.currentClusterID
	} else {
		// No current cluster, start from first or last.
		if next {
			currentID = ids[0]
		} else {
			currentID = ids[len(ids)-1]
		}
	}

	pos := -1
	for i, id := range ids {
		if id == currentID {
			pos = i
			break
		}
	}
	if pos == -1 {
		pos = 0 // current cluster was deleted, start from first
	}

	// Calculate new position.
	newPos := (pos + direction + len(ids)) % len(ids)
	newID := ids[newPos]

	cm.currentClusterID = newID
	if cluster, ok := cm.clusters[cm.currentClusterID]; ok {
		return cluster
	}
	return nil
}
