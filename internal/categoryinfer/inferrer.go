// Package categoryinfer provides a hybrid category classifier for rebate programs.
//
// When keyword inference (scrapers.inferCategories) finds no match, this package
// uses a two-stage fallback:
//
//  1. Embedding similarity — embeds the product description with
//     text-embedding-3-small and picks the closest taxonomy category by cosine
//     similarity.  Fast and cheap; handles paraphrases and novel product names
//     without requiring keyword additions.
//
//  2. GPT-4o mini — called only when the best embedding similarity is below the
//     confidence threshold, i.e., truly ambiguous cases.  Returns the single
//     most appropriate category name.
//
// Category embeddings are computed once per process (lazy init, thread-safe)
// and cached.  Product-description results are also cached by input string so
// that repeated product names across states/utilities cost only one API call.
package categoryinfer

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/incenva/rebate-scraper/internal/llm"
	"github.com/incenva/rebate-scraper/models"
)

// DefaultThreshold is the minimum cosine similarity score required for an
// embedding match to be accepted without falling back to GPT-4o mini.
// Values above this → direct assignment; values below → LLM classification.
const DefaultThreshold = 0.72

// CategoryInferrer classifies rebate product descriptions into taxonomy
// categories using embedding similarity and GPT-4o mini as a fallback.
type CategoryInferrer struct {
	client     *llm.Client
	categories []string
	embedsMu   sync.RWMutex
	embeds     map[string][]float32 // category name → embedding vector (lazy)
	threshold  float64
	cache      sync.Map // input string → []string result
}

// New returns a CategoryInferrer backed by the given llm.Client.
// Pass threshold=0 to use DefaultThreshold.
func New(client *llm.Client, threshold float64) *CategoryInferrer {
	if threshold <= 0 {
		threshold = DefaultThreshold
	}
	cats := allCategoryNames()
	return &CategoryInferrer{
		client:    client,
		categories: cats,
		embeds:    make(map[string][]float32, len(cats)),
		threshold: threshold,
	}
}

// Infer returns taxonomy category tags for the given product description.
// Returns nil (not an error) when no confident category can be determined.
// Errors are returned only for unrecoverable API failures.
func (ci *CategoryInferrer) Infer(text string) ([]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}

	if cached, ok := ci.cache.Load(text); ok {
		return cached.([]string), nil
	}

	if err := ci.ensureEmbeds(); err != nil {
		return nil, fmt.Errorf("categoryinfer: init category embeddings: %w", err)
	}

	inputVec, err := ci.client.Embed(text)
	if err != nil {
		return nil, fmt.Errorf("categoryinfer: embed input: %w", err)
	}

	bestCat, bestScore := ci.bestMatch(inputVec)

	var result []string
	if bestScore >= ci.threshold {
		result = []string{bestCat}
	} else {
		// Low confidence — use GPT-4o mini for explicit classification.
		cat, err := ci.client.ClassifyCategory(text, ci.categories)
		if err != nil {
			// Non-fatal: log and return empty so callers can fall back.
			return nil, fmt.Errorf("categoryinfer: classify: %w", err)
		}
		if cat != "" {
			result = []string{cat}
		}
	}

	ci.cache.Store(text, result)
	return result, nil
}

// ensureEmbeds initialises the category embedding table if not yet done.
func (ci *CategoryInferrer) ensureEmbeds() error {
	ci.embedsMu.RLock()
	ready := len(ci.embeds) > 0
	ci.embedsMu.RUnlock()
	if ready {
		return nil
	}

	ci.embedsMu.Lock()
	defer ci.embedsMu.Unlock()
	if len(ci.embeds) > 0 { // re-check after acquiring write lock
		return nil
	}
	for _, cat := range ci.categories {
		vec, err := ci.client.Embed(cat)
		if err != nil {
			return fmt.Errorf("embed category %q: %w", cat, err)
		}
		ci.embeds[cat] = vec
	}
	return nil
}

// bestMatch returns the category with the highest cosine similarity to vec.
func (ci *CategoryInferrer) bestMatch(vec []float32) (string, float64) {
	ci.embedsMu.RLock()
	defer ci.embedsMu.RUnlock()

	var bestCat string
	var bestScore float64
	for cat, catVec := range ci.embeds {
		if s := cosineSimilarity(vec, catVec); s > bestScore {
			bestScore = s
			bestCat = cat
		}
	}
	return bestCat, bestScore
}

// cosineSimilarity computes the cosine similarity between two float32 vectors.
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

// allCategoryNames returns a stable-sorted slice of all taxonomy category names.
func allCategoryNames() []string {
	cats := make([]string, 0, len(models.CategoryPortfolioMap))
	for cat := range models.CategoryPortfolioMap {
		cats = append(cats, cat)
	}
	sort.Strings(cats)
	return cats
}
