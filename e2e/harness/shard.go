package harness

import (
	"hash/fnv"
	"os"
	"strconv"
	"testing"
)

// shardGate skips the test unless it belongs to this runner's shard. Sharding
// lets the suite fan out across N isolated containers: each runs with
// SHARD_INDEX in [0,SHARD_TOTAL) and executes only the tests whose name hashes
// into its slice. Artifacts from all shards are merged at render time
// (`report merge`). With SHARD_TOTAL unset or <=1, every test runs.
//
// Subtests (e.g. EachWire variants) are gated independently by their full name,
// so a parent test's variants can spread across shards.
func shardGate(t *testing.T) {
	total := envInt("SHARD_TOTAL", 1)
	if total <= 1 {
		return
	}
	index := envInt("SHARD_INDEX", 0)
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name()))
	if int(h.Sum32()%uint32(total)) != index {
		t.Skipf("e2e: not in shard %d/%d", index, total)
	}
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
