//go:build race

package mesh

// modelSeeds is fewer under the race detector, which runs the model ten times slower:
// CI's race run looks for data races in the model's sessions, and every push runs all
// sixteen seeds without it (make quick).
const modelSeeds = 4
