// Package segmentinfer classifies rebate programs into canonical audience segments.
//
// Mirrors the two-stage approach of categoryinfer:
//  1. Embedding similarity against the 7 canonical segment names.
//  2. GPT-4o mini fallback when embedding confidence is below threshold.
package segmentinfer

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/incenva/rebate-scraper/internal/llm"
)

// DefaultThreshold is the minimum cosine similarity required to accept an
// embedding match without falling back to GPT-4o mini.
const DefaultThreshold = 0.78

// canonicalSegments mirrors SEGMENTS in src/lib/taxonomyConfig.ts.
var canonicalSegments = []string{
	"Residential", "Commercial", "Industrial",
	"Multifamily", "Agricultural", "Government", "Nonprofit",
}

// SegmentInferrer classifies rebate programs into canonical audience segments
// using embedding similarity and GPT-4o mini as a fallback.
type SegmentInferrer struct {
	client    *llm.Client
	segments  []string
	embedsMu  sync.RWMutex
	embeds    map[string][]float32
	threshold float64
	cache     sync.Map
}

// New returns a SegmentInferrer backed by the given llm.Client.
// Pass threshold=0 to use DefaultThreshold.
func New(client *llm.Client, threshold float64) *SegmentInferrer {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	segs := make([]string, len(canonicalSegments))
	copy(segs, canonicalSegments)
	sort.Strings(segs)
	return &SegmentInferrer{
		client:    client,
		segments:  segs,
		embeds:    make(map[string][]float32, len(segs)),
		threshold: threshold,
	}
}

// Infer returns the canonical audience segment(s) for the given program name
// and description. Returns nil (not an error) when no confident match exists.
func (si *SegmentInferrer) Infer(programName, description string) ([]string, error) {
	text := strings.TrimSpace(programName + ". " + description)
	if text == "" {
		return nil, nil
	}

	if cached, ok := si.cache.Load(text); ok {
		return cached.([]string), nil
	}

	if err := si.ensureEmbeds(); err != nil {
		return nil, fmt.Errorf("segmentinfer: init embeddings: %w", err)
	}

	inputVec, err := si.client.Embed(text)
	if err != nil {
		return nil, fmt.Errorf("segmentinfer: embed input: %w", err)
	}

	bestSeg, bestScore := si.bestMatch(inputVec)

	var result []string
	if bestScore >= si.threshold {
		result = []string{bestSeg}
	} else {
		seg, err := si.client.ClassifySegment(text, si.segments)
		if err != nil {
			return nil, fmt.Errorf("segmentinfer: classify: %w", err)
		}
		if seg != "" {
			result = []string{seg}
		}
	}

	si.cache.Store(text, result)
	return result, nil
}

func (si *SegmentInferrer) ensureEmbeds() error {
	si.embedsMu.RLock()
	ready := len(si.embeds) > 0
	si.embedsMu.RUnlock()
	if ready {
		return nil
	}
	si.embedsMu.Lock()
	defer si.embedsMu.Unlock()
	if len(si.embeds) > 0 {
		return nil
	}
	for _, seg := range si.segments {
		vec, err := si.client.Embed(seg)
		if err != nil {
			return fmt.Errorf("embed segment %q: %w", seg, err)
		}
		si.embeds[seg] = vec
	}
	return nil
}

func (si *SegmentInferrer) bestMatch(vec []float32) (string, float64) {
	si.embedsMu.RLock()
	defer si.embedsMu.RUnlock()
	var bestSeg string
	var bestScore float64
	for seg, segVec := range si.embeds {
		if s := cosineSimilarity(vec, segVec); s > bestScore {
			bestScore = s
			bestSeg = seg
		}
	}
	return bestSeg, bestScore
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
