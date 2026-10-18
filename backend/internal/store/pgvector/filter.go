package pgvector

import (
	"fmt"
	"strings"

	"ragkb/internal/types"
)

// buildFilter 把 types.Filter 编译成一段**参数化** SQL 条件。
//
// 这是本包最容易语义漂移的地方：SQL 的语义必须与 types.Filter.Match 完全一致
// （Filter.Match 是权威语义，见 internal/types/types.go）。逐条对应：
//
//	Filter.Match 里的判断                            这里的 SQL
//	---------------------------------------------  ------------------------------------------
//	containsFold(DocIDs, c.DocID)                  lower(btrim(doc_id)) = ANY($n::text[])
//	m == c.Modality（精确比较，不折叠不 trim）       modality = ANY($n::text[])
//	containsFold(Dynasty / Place / Topic / …)      lower(btrim(col)) = ANY($n::text[])
//	intersectsFold(Tags, c.Tags)                   EXISTS(unnest(tags) …)  —— 空 tags 不匹配
//	len(c.Abilities)==0 视为通用内容，始终可检索     cardinality(abilities) = 0 OR EXISTS(…)
//	len(c.Abilities)>0 时必须与过滤集有交集          EXISTS(unnest(abilities) …)
//
// 所有值都走占位符，**不做任何字符串拼接**：子句文本里只有列名和 SQL 关键字，
// 用户输入全部落在 args 里。
//
// next 是下一个可用的占位符序号（稠密检索里 $1 / $2 被查询向量和 k 占用了，
// 所以过滤条件从 $3 开始）。返回的 clause 为空串表示"这个 Filter 不筛掉任何东西"。
func buildFilter(f types.Filter, next int) (string, []any) {
	if f.Empty() {
		return "", nil
	}

	var clauses []string
	var args []any

	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", next+len(args)-1)
	}

	// 标量维度：与 containsFold 一致 —— 两边都 trim + 折叠大小写。
	scalar := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		clauses = append(clauses, fmt.Sprintf("lower(btrim(%s)) = ANY(%s::text[])", col, arg(foldAll(vals))))
	}

	// 数组维度：与 intersectsFold 一致。emptyAlwaysPasses 对应
	// "abilities 为空数组的块视为通用内容，始终可检索"。
	array := func(col string, vals []string, emptyAlwaysPasses bool) {
		if len(vals) == 0 {
			return
		}
		p := arg(foldAll(vals))
		hit := fmt.Sprintf(
			"EXISTS (SELECT 1 FROM unnest(%s) AS _v WHERE lower(btrim(_v)) = ANY(%s::text[]))", col, p)
		if emptyAlwaysPasses {
			clauses = append(clauses, fmt.Sprintf("(cardinality(%s) = 0 OR %s)", col, hit))
		} else {
			clauses = append(clauses, hit)
		}
	}

	// 顺序与 Filter.Match 里的判断顺序保持一致，方便逐行对照。
	scalar("doc_id", f.DocIDs)
	if len(f.Modalities) > 0 {
		mods := make([]string, 0, len(f.Modalities))
		for _, m := range f.Modalities {
			mods = append(mods, string(m))
		}
		clauses = append(clauses, fmt.Sprintf("modality = ANY(%s::text[])", arg(mods)))
	}
	scalar("dynasty", f.Dynasty)
	scalar("place", f.Place)
	scalar("topic", f.Topic)
	scalar("source_type", f.SourceType)
	scalar("credibility", f.Credibility)
	array("tags", f.Tags, false)
	array("abilities", f.Abilities, true)

	return strings.Join(clauses, " AND "), args
}

// foldAll 把一组值 trim + 小写，与 containsFold 的 TrimSpace + EqualFold 对齐。
func foldAll(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, strings.ToLower(strings.TrimSpace(v)))
	}
	return out
}
