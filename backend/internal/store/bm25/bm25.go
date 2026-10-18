// Package bm25 implements an in-memory Okapi BM25 sparse retrieval index.
//
// It is a standalone sub-package (not part of the parent store package) and
// depends only on the Go standard library. The index is safe for concurrent
// use: reads take a shared lock, writes take an exclusive lock.
package bm25

import (
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Scored is a single search hit: a document id and its BM25 score.
type Scored struct {
	ID    string
	Score float64
}

// docInfo holds the per-document statistics needed for scoring and removal.
type docInfo struct {
	// length is the number of tokens produced by Tokenize for this document.
	length int
	// terms maps a term to its term frequency in this document.
	terms map[string]int
}

// BM25 is a concurrent-safe in-memory Okapi BM25 index.
//
// The zero value is not usable; construct one with New.
type BM25 struct {
	mu sync.RWMutex

	k1 float64
	b  float64

	// docs maps document id -> document statistics.
	docs map[string]*docInfo
	// index is the inverted index: term -> (document id -> term frequency).
	index map[string]map[string]int
	// df maps term -> document frequency (number of documents containing it).
	df map[string]int
	// totalLen is the sum of every document length (|d|) in the index.
	totalLen int
	// avgdl is totalLen / N, i.e. the average document length.
	avgdl float64
}

// New creates an empty BM25 index.
//
// If k1 <= 0 it defaults to 1.5. If b < 0 it defaults to 0.75.
func New(k1, b float64) *BM25 {
	if k1 <= 0 {
		k1 = 1.5
	}
	if b < 0 {
		b = 0.75
	}
	return &BM25{
		k1:    k1,
		b:     b,
		docs:  make(map[string]*docInfo),
		index: make(map[string]map[string]int),
		df:    make(map[string]int),
	}
}

// Add inserts a document into the index. If id already exists it is replaced:
// the old postings are removed first, then the new ones are inserted, so all
// global statistics (df, avgdl, N) stay consistent.
//
// Add runs in O(len(text)) time plus the cost of inserting the document's
// distinct terms; it never rescans the whole corpus.
func (x *BM25) Add(id string, text string) {
	tokens := Tokenize(text)

	// Count term frequencies outside the lock to keep the critical section short.
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}

	x.mu.Lock()
	defer x.mu.Unlock()

	if old, ok := x.docs[id]; ok {
		x.removeLocked(id, old)
	}

	x.docs[id] = &docInfo{length: len(tokens), terms: tf}
	for term, freq := range tf {
		postings := x.index[term]
		if postings == nil {
			postings = make(map[string]int)
			x.index[term] = postings
		}
		postings[id] = freq
		x.df[term]++
	}
	x.totalLen += len(tokens)
	x.recomputeAvgLocked()
}

// Remove deletes a document from the index. Removing an unknown id is a no-op.
func (x *BM25) Remove(id string) {
	x.mu.Lock()
	defer x.mu.Unlock()

	doc, ok := x.docs[id]
	if !ok {
		return
	}
	x.removeLocked(id, doc)
	x.recomputeAvgLocked()
}

// removeLocked drops a known document. The caller must hold x.mu for writing.
func (x *BM25) removeLocked(id string, doc *docInfo) {
	for term := range doc.terms {
		postings := x.index[term]
		if postings == nil {
			continue
		}
		delete(postings, id)
		if len(postings) == 0 {
			delete(x.index, term)
			delete(x.df, term)
		} else {
			x.df[term]--
		}
	}
	x.totalLen -= doc.length
	delete(x.docs, id)
}

// recomputeAvgLocked refreshes avgdl. The caller must hold x.mu for writing.
func (x *BM25) recomputeAvgLocked() {
	if n := len(x.docs); n > 0 {
		x.avgdl = float64(x.totalLen) / float64(n)
	} else {
		x.avgdl = 0
	}
}

// Search scores the documents that contain at least one query term and returns
// the top k by score. Results are sorted by descending score, then by ascending
// id, so the output is fully deterministic.
//
// Only postings of the query terms are visited; the full document set is never
// scanned. If k <= 0, the query has no valid token, or nothing matches, an
// empty (non-nil) slice is returned.
func (x *BM25) Search(query string, k int) []Scored {
	if k <= 0 {
		return []Scored{}
	}

	tokens := Tokenize(query)
	if len(tokens) == 0 {
		return []Scored{}
	}

	// Deduplicate query terms while preserving order; each distinct term
	// contributes to the sum exactly once.
	seen := make(map[string]struct{}, len(tokens))
	terms := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		terms = append(terms, t)
	}

	x.mu.RLock()
	defer x.mu.RUnlock()

	n := len(x.docs)
	if n == 0 {
		return []Scored{}
	}
	avgdl := x.avgdl
	if avgdl <= 0 {
		avgdl = 1
	}
	k1 := x.k1
	b := x.b

	scores := make(map[string]float64)
	for _, term := range terms {
		postings := x.index[term]
		if len(postings) == 0 {
			continue
		}
		df := float64(x.df[term])
		// Okapi IDF, always positive.
		idf := math.Log(1 + (float64(n)-df+0.5)/(df+0.5))
		for id, tf := range postings {
			doc := x.docs[id]
			if doc == nil {
				continue
			}
			f := float64(tf)
			denom := f + k1*(1-b+b*float64(doc.length)/avgdl)
			if denom == 0 {
				continue
			}
			scores[id] += idf * (f * (k1 + 1)) / denom
		}
	}

	if len(scores) == 0 {
		return []Scored{}
	}

	out := make([]Scored, 0, len(scores))
	for id, score := range scores {
		out = append(out, Scored{ID: id, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Len returns the number of indexed documents.
func (x *BM25) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.docs)
}

// Clear removes every document from the index.
func (x *BM25) Clear() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.docs = make(map[string]*docInfo)
	x.index = make(map[string]map[string]int)
	x.df = make(map[string]int)
	x.totalLen = 0
	x.avgdl = 0
}

// Tokenize splits text into retrieval terms.
//
// Rules:
//   - A contiguous run of letters and digits that is not Han is lowercased and
//     emitted as a single token (ASCII letters/digits included).
//   - A contiguous run of Han (CJK) characters emits every unigram first, then
//     every bigram. For example "知识库" yields
//     ["知","识","库","知识","识库"]; a single Han character yields only its
//     unigram.
//   - Whitespace and punctuation (ASCII and full-width) are separators and
//     produce no token.
//
// The returned slice is never nil.
func Tokenize(s string) []string {
	runes := []rune(s)
	tokens := make([]string, 0, len(runes))
	n := len(runes)

	for i := 0; i < n; {
		r := runes[i]
		switch {
		case unicode.Is(unicode.Han, r):
			j := i
			for j < n && unicode.Is(unicode.Han, runes[j]) {
				j++
			}
			seg := runes[i:j]
			for _, c := range seg {
				tokens = append(tokens, string(c))
			}
			for p := 0; p+1 < len(seg); p++ {
				tokens = append(tokens, string(seg[p:p+2]))
			}
			i = j
		case isWordRune(r):
			j := i
			for j < n && isWordRune(runes[j]) {
				j++
			}
			tokens = append(tokens, strings.ToLower(string(runes[i:j])))
			i = j
		default:
			// Separator: whitespace, punctuation, symbols, control runes.
			i++
		}
	}
	return tokens
}

// isWordRune reports whether r belongs to a non-Han word run (any letter or
// digit). Han is excluded so that CJK segments are handled separately.
func isWordRune(r rune) bool {
	if unicode.Is(unicode.Han, r) {
		return false
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}
