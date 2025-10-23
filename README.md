# Zellij

Zellij is this [Moroccan tiling
technique](https://en.wikipedia.org/wiki/Zellij). This is Go implementation of
[Generative
Zellij](https://archive.bridgesmathart.org/2022/bridges2022-285.pdf) by Craig
S. Kaplan. Much of the actual tiling generation code is an AI translation of
the original [JavaScript](https://isohedral.ca/generative-zellij/), so the code
is quite terrible, but some of the stuff around it is (slightly) less so. 

![Zellij](https://github.com/user-attachments/assets/9d5962f0-d2fe-4735-9be8-1aab5ba6bbaf)


```sh
go build -o zellij ./cmd/ && ./zellij
# debug env vars:
#   ZELLIJ_DEBUG_COMPACTION=1
#   ZELLIJ_DEBUG_MEMORY=1
#   ZELLIJ_DEBUG_RUNTIME=1
```

#### Basic Controls
- `Space/Shift+Space`: Generate new pattern (regenerates closest cluster), shift to generate previous
- `H/J/K/L`: Pan left/down/up/right
    - Can get the same through dragging the canvas 
- `Cmd+Plus/Minus`: Zoom in/out
    - Can get the same through scroll
- `Tab/Shift+Tab`: Cycle through clusters in creation order
- `R`: Reset zoom/pan to closest cluster (cursor-centric)
- `C`: New cluster at mouse
    - `<n>C`: New cluster, complexity n (e.g. `5c`)
    - `<n>,C`: Create n clusters in a grid (e.g. `10,c`)
    - `<n>,<m>C`: n clusters, complexity m (e.g. `10,5c`)
- `D`: Delete cluster closest to mouse
    - `<n>D`: Delete n nearest clusters (e.g. `10d`)


#### Memory management

There are some stock memory management ideas applied here, to store different
shaped clusters while trying to minimize the # of draw calls and supporting
fast per-cluster updates. 
- We use array-based rendering (no indexing/EBOs since we're dealing with just
2D-geometry) with fixed-capacity slot allocation, batching multiple clusters
into shared VBOs. We only use direct rendering. We use `MultiDrawArrays`
underneath to address the specific sub-slices of each batch that stores
cluster vertext data.
- There are distinct memory tiers, and each memory tier has some number of
batches, each of which is further subdivided into individual slots -- each
cluster slots into one. Each batch is backed by a VBO+VAO, and corresponds to
effectively 1 draw call/frame.
    - For super large clusters (there wouldn't be many), we use the `xxlarge`
    tier and create single-slot buffers tied to the lifetime of the cluster
    itself.
- Batches are allowed to grow, up to some memory limit and # of growth cycles.
- There's a small free-list maintained per memory tier for fast allocations
when there's a lot of churn.
- Updates happen by copying in vertices into specific slots, which corresponds
to a partial GPU buffer write (`glBufferSubData`). 
- There's a compaction loop that tries to consolidate active slots into fewer
batches within the same memory tier. Empty batches are deleted, free-ing up GPU
memory. We use CPU-side copying here, but it could also be done purely on GPU
(`glCopyBufferSubData`). We do at most one compaction step per frame (we try
compactions every 60 frames).

```python
===== Memory Controller Stats =====
19 compactions (8 slots relocated, 19 batches deleted, 2198.00μs last), 18 growth events (201.00μs last)
94.3% slots active (3441/3649), 100.0% batches active (6/6), 163 free-list slots, 164.9M GPU, 3441 clusters (1.0M triangles, 3.1M vertices)
  [   small] ███████████░ 97% slots active (2977/3072), 100% batches active (3/3), 95 free-list slots, 75.5M GPU (551.5K triangles, 1.7M vertices)
      batch#019  ████████ 100% slots active (1024/1024), 25.2M GPU (192.3K triangles, 576.8K vertices), 3× growth (262.1K -> 1.0M)
      batch#020  ████████ 100% slots active (1024/1024), 25.2M GPU (192.1K triangles, 576.4K vertices), 3× growth (262.1K -> 1.0M)
      batch#021  ███████░ 91% slots active (929/1024), 25.2M GPU (167.1K triangles, 501.3K vertices), 3× growth (262.1K -> 1.0M)
  [  medium] ██████████░░ 87% slots active (444/512), 100% batches active (1/1), 68 free-list slots, 50.3M GPU (260.0K triangles, 780.0K vertices)
      batch#018  ██████░░ 87% slots active (444/512), 50.3M GPU (260.0K triangles, 780.0K vertices), 3× growth (524.3K -> 2.1M)
  [   large] ███░░░░░░░░░ 30% slots active (19/64), 100% batches active (1/1), 0 free-list slots, 25.2M GPU (42.3K triangles, 126.8K vertices)
      batch#023  ██░░░░░░ 30% slots active (19/64), 25.2M GPU (42.3K triangles, 126.8K vertices), 1× growth (1.0M -> 1.0M)
  [ xxlarge] ████████████ 100% slots active (1/1), 100% batches active (1/1), 0 free-list slots, 13.9M GPU (192.8K triangles, 578.5K vertices)
      batch#024  ████████ 100% slots active (1/1), 13.9M GPU (192.8K triangles, 578.5K vertices), 1× growth (578.5K -> 578.5K)
```

```python
=== Performance statistics ===
Frame rate:     119.3 FPS (8.37 ms/frame, 6 draw calls/frame)
Shapes:         3441 clusters, 1046579 triangles, 3139737 vertices
GPU memory:     157.24 MiB
Render time:    839.00 µs (last draw), 43.41 ms (last prepare)
Compaction:     19 events (8 slots relocated, 19 batches deleted), 2198.00 μs (last)
Throughput:     374.49 M vertices/sec
==============================
```

```python
[compaction] scanning across 5 buckets for candidates (min-util=25.0%)
[compaction] [small] batch[1/3]#19 - TOO DENSE (100.0% util, 1024/1024 slots active)
[compaction] [small] batch[2/3]#20 - TOO DENSE (100.0% util, 1024/1024 slots active)
[compaction] [small] batch[3/3]#21 - TOO DENSE (90.7% util, 929/1024 slots active)
[compaction] [medium] batch[1/1]#18 - TOO DENSE (86.7% util, 444/512 slots active)
[compaction] [large] batch[1/1]#23 - TOO DENSE (29.7% util, 19/64 slots active)
[compaction] [xxlarge] batch[1/1]#24 - TOO DENSE (100.0% util, 1/1 slots active)
[compaction] scan completed with 6 total batches, 0 empty, 0 candidates for compaction
```
