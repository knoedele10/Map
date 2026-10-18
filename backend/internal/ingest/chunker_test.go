package ingest

import (
	"strings"
	"testing"
)

func TestLongTableRowsRespectSize(t *testing.T) {
	for _, text := range []string{strings.Repeat("长", 130), strings.Repeat("短", 39) + "\n" + strings.Repeat("长", 130) + "\n尾行"} {
		for _, chunk := range ChunkTable(text, ChunkOptions{Size: 40, MaxPerDoc: 100}) {
			if len([]rune(chunk)) > 40 {
				t.Fatalf("oversized table chunk: %d", len([]rune(chunk)))
			}
		}
	}
}

func TestSplitSentences(t *testing.T) {
	got := SplitSentences("第一句。第二句！第三句？\n第四句", 100)
	want := []string{"第一句。", "第二句！", "第三句？", "第四句"}
	if len(got) != len(want) {
		t.Fatalf("句数不对: got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 句: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestSplitSentencesHardSplit(t *testing.T) {
	long := strings.Repeat("甲", 25)
	got := SplitSentences(long, 10)
	if len(got) != 3 {
		t.Fatalf("超长句应被硬切成 3 段，得到 %d: %v", len(got), got)
	}
	for _, s := range got {
		if len([]rune(s)) > 10 {
			t.Errorf("硬切后仍超长: %q", s)
		}
	}
}

func TestNormalizeSpaces(t *testing.T) {
	got := normalizeSpaces("甲   乙" + string(rune(0x3000)) + "丙" + string(rune(0x09)) + "丁")
	if got != "甲 乙 丙 丁" {
		t.Fatalf("空白压缩失败: %q", got)
	}
}

func TestChunkTextSizeRespected(t *testing.T) {
	text := strings.Repeat("一二三四五六七八九十。", 20)
	chunks := ChunkText(text, ChunkOptions{Size: 50, Overlap: 10, MinChars: 5})
	if len(chunks) < 3 {
		t.Fatalf("应切出多块，得到 %d", len(chunks))
	}
	for i, c := range chunks {
		if n := len([]rune(c)); n > 60 { // 允许一个句子的溢出
			t.Errorf("第 %d 块过长: %d 字", i, n)
		}
	}
}

func TestChunkTextKeepsShortDoc(t *testing.T) {
	got := ChunkText("太短", ChunkOptions{Size: 100, Overlap: 10, MinChars: 50})
	if len(got) != 1 || got[0] != "太短" {
		t.Fatalf("短文档不应被丢弃，得到 %v", got)
	}
}

func TestChunkTextMaxPerDoc(t *testing.T) {
	text := strings.Repeat("一二三四五六七八九十。", 100)
	got := ChunkText(text, ChunkOptions{Size: 30, Overlap: 5, MinChars: 1, MaxPerDoc: 3})
	if len(got) != 3 {
		t.Fatalf("应被限制为 3 块，得到 %d", len(got))
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if got := ChunkText("     ", ChunkOptions{Size: 100}); len(got) != 0 {
		t.Fatalf("空文本应切出 0 块，得到 %v", got)
	}
}

func TestChunkTable(t *testing.T) {
	var rows []string
	for i := 0; i < 30; i++ {
		rows = append(rows, "行数据一二三四五")
	}
	got := ChunkTable(strings.Join(rows, "\n"), ChunkOptions{Size: 60, MinChars: 1})
	if len(got) < 2 {
		t.Fatalf("30 行应按 size 切成多块，得到 %d", len(got))
	}
	for _, c := range got {
		if !strings.Contains(c, "\n") {
			t.Errorf("表格块应保留多行结构: %q", c)
		}
	}
}
