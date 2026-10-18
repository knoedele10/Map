package bm25

import (
	"fmt"
	"math"
	"reflect"
	"sync"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"english lowercased", "Hello World", []string{"hello", "world"}},
		{"chinese unigram and bigram", "知识库", []string{"知", "识", "库", "知识", "识库"}},
		{"mixed cjk and ascii", "Go知识库", []string{"go", "知", "识", "库", "知识", "识库"}},
		{"punctuation separates", "hello, world! 你好。", []string{"hello", "world", "你", "好", "你好"}},
		{"digits", "abc123 456", []string{"abc123", "456"}},
		{"empty", "", []string{}},
		{"only punctuation", "!!!，。  ", []string{}},
		{"single cjk", "知", []string{"知"}},
		{"cyrillic", "Привет, мир!", []string{"привет", "мир"}},
		{"cjk ascii cjk", "模型abc向量", []string{"模", "型", "模型", "abc", "向", "量", "向量"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Tokenize(tc.in)
			if got == nil {
				t.Fatalf("Tokenize(%q) returned nil, want non-nil", tc.in)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Tokenize(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	def := New(0, -1)
	if def.k1 != 1.5 {
		t.Errorf("k1 = %v, want 1.5", def.k1)
	}
	if def.b != 0.75 {
		t.Errorf("b = %v, want 0.75", def.b)
	}

	custom := New(2.0, 0.3)
	if custom.k1 != 2.0 || custom.b != 0.3 {
		t.Errorf("custom params = (%v, %v), want (2, 0.3)", custom.k1, custom.b)
	}

	zeroB := New(1.5, 0)
	if zeroB.b != 0 {
		t.Errorf("b = %v, want 0 (b=0 is valid)", zeroB.b)
	}
}

func TestSearchRareTermRanksFirst(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("common1", "common word appears here")
	x.Add("common2", "common word again")
	x.Add("common3", "common word repeated")
	x.Add("rare", "rare word only once")

	got := x.Search("common rare", 10)
	if len(got) == 0 {
		t.Fatal("expected hits, got none")
	}
	if got[0].ID != "rare" {
		t.Fatalf("top hit = %q, want %q (rare term should outrank common term); all=%v", got[0].ID, "rare", got)
	}
}

func TestSearchExactMatchFirst(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("exact", "deep learning")
	x.Add("partial", "deep neural network learning model")

	got := x.Search("deep learning", 10)
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2: %v", len(got), got)
	}
	if got[0].ID != "exact" {
		t.Fatalf("top hit = %q, want %q; all=%v", got[0].ID, "exact", got)
	}
}

func TestSearchTermFrequency(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("once", "quick brown fox")
	x.Add("thrice", "quick quick quick fox")

	got := x.Search("quick", 10)
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2: %v", len(got), got)
	}
	if got[0].ID != "thrice" {
		t.Fatalf("top hit = %q, want %q (higher tf wins)", got[0].ID, "thrice")
	}
}

func TestChineseRecall(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("d1", "知识库系统支持多模态检索")
	x.Add("d2", "今天天气很好")

	got := x.Search("知识库", 10)
	if len(got) != 1 || got[0].ID != "d1" {
		t.Fatalf("query 知识库 = %v, want d1 only", got)
	}
}

func TestAddReplacesDocument(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("a", "hello world")
	x.Add("a", "goodbye moon")

	if x.Len() != 1 {
		t.Fatalf("Len = %d, want 1", x.Len())
	}
	if hits := x.Search("hello", 5); len(hits) != 0 {
		t.Fatalf("stale term still matches: %v", hits)
	}
	if _, ok := x.index["hello"]; ok {
		t.Fatalf("stale posting for term %q still present", "hello")
	}
	if _, ok := x.df["world"]; ok {
		t.Fatalf("stale df entry for term %q still present", "world")
	}
	hits := x.Search("goodbye", 5)
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("new term did not match: %v", hits)
	}
	if x.df["goodbye"] != 1 {
		t.Fatalf("df[goodbye] = %d, want 1", x.df["goodbye"])
	}
	// (2 + 2) / 1 = 2: replacement must not double count lengths.
	if x.totalLen != 2 || x.avgdl != 2 {
		t.Fatalf("totalLen/avgdl = %d/%v, want 2/2", x.totalLen, x.avgdl)
	}
}

func TestRemove(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("a", "alpha beta")
	x.Add("b", "beta gamma")
	x.Add("c", "gamma delta")

	if x.Len() != 3 {
		t.Fatalf("Len = %d, want 3", x.Len())
	}
	x.Remove("b")
	if x.Len() != 2 {
		t.Fatalf("Len after remove = %d, want 2", x.Len())
	}
	if x.totalLen != 4 {
		t.Fatalf("totalLen = %d, want 4", x.totalLen)
	}
	if x.avgdl != 2 {
		t.Fatalf("avgdl = %v, want 2", x.avgdl)
	}
	hits := x.Search("beta", 5)
	if len(hits) != 1 || hits[0].ID != "a" {
		t.Fatalf("beta hits = %v, want only a", hits)
	}
	if hits := x.Search("gamma", 5); len(hits) != 1 || hits[0].ID != "c" {
		t.Fatalf("gamma hits = %v, want only c", hits)
	}
	if _, ok := x.df["beta"]; !ok || x.df["beta"] != 1 {
		t.Fatalf("df[beta] = %d, want 1", x.df["beta"])
	}

	// Re-adding after removal keeps statistics consistent.
	x.Add("d", "epsilon")
	if x.Len() != 3 {
		t.Fatalf("Len = %d, want 3", x.Len())
	}
	if math.Abs(x.avgdl-5.0/3.0) > 1e-12 {
		t.Fatalf("avgdl = %v, want %v", x.avgdl, 5.0/3.0)
	}
	hits = x.Search("delta", 5)
	if len(hits) != 1 || hits[0].ID != "c" {
		t.Fatalf("delta hits = %v, want only c", hits)
	}

	// Removing an unknown id is a no-op.
	x.Remove("does-not-exist")
	if x.Len() != 3 {
		t.Fatalf("Len after no-op remove = %d, want 3", x.Len())
	}
}

func TestSearchKAndQueryBoundaries(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("a", "hello world")
	x.Add("b", "hello there")

	if got := x.Search("hello", 0); len(got) != 0 {
		t.Fatalf("k=0 returned %v, want empty", got)
	}
	if got := x.Search("hello", -3); len(got) != 0 {
		t.Fatalf("k<0 returned %v, want empty", got)
	}
	if got := x.Search("hello", 10); len(got) != 2 {
		t.Fatalf("k>hits returned %d hits, want 2", len(got))
	}
	if got := x.Search("hello", 1); len(got) != 1 {
		t.Fatalf("k=1 returned %d hits, want 1", len(got))
	}
	if got := x.Search("", 5); len(got) != 0 {
		t.Fatalf("empty query returned %v, want empty", got)
	}
	if got := x.Search("!!!，。", 5); len(got) != 0 {
		t.Fatalf("punctuation-only query returned %v, want empty", got)
	}
	if got := x.Search("absentterm", 5); len(got) != 0 {
		t.Fatalf("unmatched query returned %v, want empty", got)
	}
	if got := x.Search("hello", 5); got == nil {
		t.Fatal("Search returned nil, want non-nil slice")
	}
}

func TestSearchDeterministic(t *testing.T) {
	x := New(1.5, 0.75)
	// Identical texts => identical scores => ordering must fall back to id asc.
	for i := 0; i < 25; i++ {
		x.Add(fmt.Sprintf("doc-%02d", i), "same text for every document")
	}
	first := x.Search("same text document", 10)
	second := x.Search("same text document", 10)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Search is not deterministic: first=%v second=%v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1].ID > first[i].ID {
			t.Fatalf("ties not ordered by id: %v", first)
		}
	}
	if len(first) != 10 {
		t.Fatalf("got %d hits, want 10", len(first))
	}
}

func TestConcurrentSearchAndAdd(t *testing.T) {
	x := New(1.5, 0.75)
	for i := 0; i < 50; i++ {
		x.Add(fmt.Sprintf("seed-%d", i), fmt.Sprintf("seed document number %d", i))
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 300; j++ {
				_ = x.Search("seed document", 5)
				_ = x.Search("并发 检索", 5)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 300; j++ {
			id := fmt.Sprintf("add-%d", j)
			x.Add(id, "concurrent added document 并发写入")
			if j%3 == 0 {
				x.Remove(id)
			}
		}
	}()
	wg.Wait()

	if x.Len() != 50+200 {
		t.Fatalf("Len = %d, want %d", x.Len(), 250)
	}
}

func TestEmptyTextAndClear(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("empty", "") // must not panic
	if x.Len() != 1 {
		t.Fatalf("Len = %d, want 1", x.Len())
	}
	if got := x.Search("anything", 5); len(got) != 0 {
		t.Fatalf("search over empty doc returned %v, want empty", got)
	}
	// avgdl == 0 must not produce NaN scores once real docs arrive.
	x.Add("real", "real content here")
	if got := x.Search("real", 5); len(got) != 1 || math.IsNaN(got[0].Score) {
		t.Fatalf("score = %v, want a non-NaN hit", got)
	}

	x.Clear()
	if x.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", x.Len())
	}
	if got := x.Search("real", 5); len(got) != 0 {
		t.Fatalf("search after Clear returned %v, want empty", got)
	}
	x.Add("after", "after clear")
	if got := x.Search("after", 5); len(got) != 1 {
		t.Fatalf("Add after Clear returned %v, want 1 hit", got)
	}
}

func TestScoresMatchFormula(t *testing.T) {
	x := New(1.5, 0.75)
	x.Add("d1", "alpha beta alpha")
	x.Add("d2", "beta gamma delta epsilon")

	got := x.Search("alpha beta", 10)
	if len(got) != 2 {
		t.Fatalf("got %d hits, want 2: %v", len(got), got)
	}
	byID := map[string]float64{}
	for _, s := range got {
		byID[s.ID] = s.Score
	}

	n := 2.0
	avgdl := (3.0 + 4.0) / 2.0
	k1, b := 1.5, 0.75
	idf := func(df float64) float64 { return math.Log(1 + (n-df+0.5)/(df+0.5)) }
	score := func(tf, dl float64, df float64) float64 {
		return idf(df) * (tf * (k1 + 1)) / (tf + k1*(1-b+b*dl/avgdl))
	}
	// d1: alpha tf=2 (df=1), beta tf=1 (df=2), |d|=3.
	wantD1 := score(2, 3, 1) + score(1, 3, 2)
	// d2: beta tf=1 (df=2), |d|=4.
	wantD2 := score(1, 4, 2)
	if math.Abs(byID["d1"]-wantD1) > 1e-9 {
		t.Errorf("score(d1) = %v, want %v", byID["d1"], wantD1)
	}
	if math.Abs(byID["d2"]-wantD2) > 1e-9 {
		t.Errorf("score(d2) = %v, want %v", byID["d2"], wantD2)
	}
}

// BenchmarkAdd100k measures bulk insertion of 100k short documents.
// Run with: go test -bench=BenchmarkAdd100k -benchtime=1x -run=^$
func BenchmarkAdd100k(b *testing.B) {
	const docs = 100000
	texts := make([]string, docs)
	for i := range texts {
		texts[i] = fmt.Sprintf("多模态 RAG 知识库 document number %d with some extra words", i)
	}
	b.ResetTimer()
	x := New(1.5, 0.75)
	for i := 0; i < docs; i++ {
		x.Add(fmt.Sprintf("doc-%d", i), texts[i])
	}
	if x.Len() != docs {
		b.Fatalf("Len = %d, want %d", x.Len(), docs)
	}
}
