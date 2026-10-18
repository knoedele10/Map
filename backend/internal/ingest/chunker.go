package ingest

import (
	"strings"
	"unicode"
)

// ChunkOptions 控制切块。RAG 效果好坏一大半取决于这三个数字。
type ChunkOptions struct {
	Size      int // 每块最多多少字
	Overlap   int // 相邻块重叠字数（建议 Size 的 10%~15%）
	MinChars  int // 小于这个长度的块丢弃
	MaxPerDoc int // 单文件切块上限，防御异常大文件
}

// sentenceEnd 是句末标点：中英文都要认。
var sentenceEnd = map[rune]bool{
	'。': true, '！': true, '？': true, '；': true, '…': true,
	'.': true, '!': true, '?': true, ';': true,
}

// normalizeSpaces 把连续空白（含制表符与全角空格 U+3000）压成一个半角空格。
// 用码点比较而不是正则转义，避免源文件里出现难以阅读的转义序列。
func normalizeSpaces(s string) string {
	const ideographicSpace = 0x3000
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if r == ' ' || r == 0x09 || r == ideographicSpace {
			pendingSpace = true
			continue
		}
		if pendingSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pendingSpace = false
		b.WriteRune(r)
	}
	return b.String()
}

// SplitSentences 先按标点和换行切句；超长的句子（例如没有标点的表格行）再硬切。
func SplitSentences(text string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = 500
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = normalizeSpaces(text)

	var out []string
	var buf []rune
	flush := func() {
		s := strings.TrimSpace(string(buf))
		buf = buf[:0]
		if s == "" {
			return
		}
		if len([]rune(s)) <= maxLen {
			out = append(out, s)
			return
		}
		// 超长句硬切，保证不会产生超过 maxLen 的单元。
		r := []rune(s)
		for i := 0; i < len(r); i += maxLen {
			end := i + maxLen
			if end > len(r) {
				end = len(r)
			}
			out = append(out, strings.TrimSpace(string(r[i:end])))
		}
	}

	for _, r := range text {
		if r == '\n' {
			flush()
			continue
		}
		buf = append(buf, r)
		if sentenceEnd[r] {
			flush()
		}
	}
	flush()

	// 丢掉纯标点/纯空白产生的碎片
	kept := out[:0]
	for _, s := range out {
		if strings.TrimFunc(s, func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) }) != "" {
			kept = append(kept, s)
		}
	}
	return kept
}

// ChunkText 把长文本切成若干块，相邻块之间保留一段重叠。
//
// 重叠是干什么的：假设答案正好横跨两块边界，没有重叠就可能两边都不完整；
// 重叠 Overlap 个字，边界上的内容至少会在其中一块里是完整的。
func ChunkText(text string, opt ChunkOptions) []string {
	if opt.Size <= 0 {
		opt.Size = 600
	}
	if opt.Overlap < 0 || opt.Overlap >= opt.Size {
		opt.Overlap = opt.Size / 8 // 兜底成 12.5%
	}
	if opt.MinChars <= 0 {
		opt.MinChars = 20
	}

	sentences := SplitSentences(text, opt.Size)
	var chunks []string
	var current []string
	currentLen := 0

	for _, s := range sentences {
		sLen := len([]rune(s))
		if len(current) > 0 && currentLen+sLen > opt.Size {
			chunks = append(chunks, strings.TrimSpace(strings.Join(current, "")))
			// 从尾部倒着取句子，凑够 overlap 个字带到下一块
			var tail []string
			tailLen := 0
			for i := len(current) - 1; i >= 0; i-- {
				l := len([]rune(current[i]))
				if tailLen+l > opt.Overlap {
					break
				}
				tail = append([]string{current[i]}, tail...)
				tailLen += l
			}
			current = tail
			currentLen = tailLen
		}
		for len(current) > 0 && currentLen+sLen > opt.Size {
			currentLen -= len([]rune(current[0]))
			current = current[1:]
		}
		current = append(current, s)
		currentLen += sLen
	}
	if len(current) > 0 {
		chunks = append(chunks, strings.TrimSpace(strings.Join(current, "")))
	}

	out := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if len([]rune(c)) >= opt.MinChars {
			out = append(out, c)
		}
	}
	// 短文档只有一个不足 MinChars 的块时，别把它丢光——否则小文件永远检索不到。
	if len(out) == 0 && len(chunks) > 0 && strings.TrimSpace(chunks[0]) != "" {
		out = append(out, strings.TrimSpace(chunks[0]))
	}
	if opt.MaxPerDoc > 0 && len(out) > opt.MaxPerDoc {
		out = out[:opt.MaxPerDoc]
	}
	return out
}

// ChunkTable 把表格按行切块：表格不按句子切（会把一行数据切碎），
// 而是按行分组，保证每一块都是完整的若干行。
func ChunkTable(text string, opt ChunkOptions) []string {
	if opt.Size <= 0 {
		opt.Size = 600
	}
	rows := strings.Split(text, "\n")
	var out []string
	var current []string
	currentLen := 0
	for _, r := range rows {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		l := len([]rune(r))
		if l > opt.Size {
			if len(current) > 0 {
				out = append(out, strings.Join(current, "\n"))
				current = nil
				currentLen = 0
			}
			row := []rune(r)
			for start := 0; start < len(row); start += opt.Size {
				end := start + opt.Size
				if end > len(row) {
					end = len(row)
				}
				out = append(out, string(row[start:end]))
			}
			continue
		}
		if len(current) > 0 && currentLen+l > opt.Size {
			out = append(out, strings.Join(current, "\n"))
			current = current[(len(current)+1)/2:] // 保留不超过一半的行
			currentLen = 0
			for _, x := range current {
				currentLen += len([]rune(x))
			}
			for len(current) > 0 && currentLen+l > opt.Size {
				currentLen -= len([]rune(current[0]))
				current = current[1:]
			}
		}
		current = append(current, r)
		currentLen += l
	}
	if len(current) > 0 {
		out = append(out, strings.Join(current, "\n"))
	}
	if len(out) == 0 && strings.TrimSpace(text) != "" {
		out = append(out, strings.TrimSpace(text))
	}
	if opt.MaxPerDoc > 0 && len(out) > opt.MaxPerDoc {
		out = out[:opt.MaxPerDoc]
	}
	return out
}
