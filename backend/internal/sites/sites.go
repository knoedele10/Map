// Package sites 加载结构化景点数据。
//
// 它有两个用途：
//  1. GET /api/v1/sites/:id 给前端渲染；
//  2. 作为**只读工具**在生成前注入模型上下文——用户问"乐山大佛多高"时，
//     直接把结构化字段喂给模型，比让模型从散文里猜数字可靠得多。
//
// 工具是白名单化的：只有这里的查询能力，没有写操作、没有网络、没有代码执行。
package sites

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"ragkb/internal/types"
)

// Store 是只读的景点数据集合。
type Store struct {
	mu    sync.RWMutex
	byID  map[string]types.Site
	order []string
	path  string
}

// Load 从 JSON 文件加载。文件不存在时返回空集合（不算错误，服务照常运行）。
// 兼容两种格式：{"sites":[...]} 与裸数组 [...]。
func Load(path string) (*Store, error) {
	s := &Store{byID: map[string]types.Site{}, path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("读取景点数据 %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return s, nil
	}

	var list []types.Site
	var wrapper struct {
		Sites []types.Site `json:"sites"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Sites != nil {
		list = wrapper.Sites
	} else if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("解析景点数据 %s: %w", path, err)
	}

	for _, site := range list {
		if site.ID == "" {
			return nil, fmt.Errorf("景点数据 %s 里有条目缺少 id", path)
		}
		s.byID[site.ID] = site
		s.order = append(s.order, site.ID)
	}
	sort.Strings(s.order)
	return s, nil
}

// Path 返回数据文件位置。
func (s *Store) Path() string { return s.path }

// Len 返回景点数量。
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// Get 按 id、name 或 alias 精确查询（大小写不敏感）。
func (s *Store) Get(idOrName string) (types.Site, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key := strings.TrimSpace(idOrName)
	if site, ok := s.byID[key]; ok {
		return site, true
	}
	for _, id := range s.order {
		site := s.byID[id]
		if strings.EqualFold(site.Name, key) {
			return site, true
		}
		for _, a := range site.Aliases {
			if strings.EqualFold(strings.TrimSpace(a), key) {
				return site, true
			}
		}
	}
	return types.Site{}, false
}

// List 返回全部景点（按 id 排序）。
func (s *Store) List() []types.Site {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]types.Site, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byID[id])
	}
	return out
}

// Resolve 在用户问题里找出提到的景点（名称或别名子串匹配）。
//
// 为什么用子串匹配而不是让模型调函数：确定性、零额外 token、跨供应商可用。
// 匹配结果按名称长度降序，避免"乐山"把"乐山大佛"盖掉。
func (s *Store) Resolve(question string) []types.Site {
	s.mu.RLock()
	defer s.mu.RUnlock()

	q := strings.ToLower(question)
	type match struct {
		site types.Site
		key  string
	}
	var found []match
	seen := map[string]bool{}
	for _, id := range s.order {
		site := s.byID[id]
		keys := append([]string{site.Name}, site.Aliases...)
		for _, k := range keys {
			k = strings.TrimSpace(k)
			if len([]rune(k)) < 2 { // 单字别名噪声太大，忽略
				continue
			}
			if strings.Contains(q, strings.ToLower(k)) && !seen[site.ID] {
				found = append(found, match{site: site, key: k})
				seen[site.ID] = true
			}
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		return len([]rune(found[i].key)) > len([]rune(found[j].key))
	})
	out := make([]types.Site, 0, len(found))
	for i, m := range found {
		if i >= 3 { // 一次最多注入 3 个景点，防止上下文被工具结果淹没
			break
		}
		out = append(out, m.site)
	}
	return out
}

// Block 把景点渲染成给模型看的结构化文本块（不是给用户看的）。
func Block(site types.Site) string {
	var b strings.Builder
	fmt.Fprintf(&b, "景点：%s", site.Name)
	if len(site.Aliases) > 0 {
		fmt.Fprintf(&b, "（别名：%s）", strings.Join(site.Aliases, "、"))
	}
	b.WriteString("\n")
	writeIf := func(label, v string) {
		if strings.TrimSpace(v) != "" {
			fmt.Fprintf(&b, "%s：%s\n", label, v)
		}
	}
	writeIf("朝代", site.Dynasty)
	writeIf("地点", site.Place)
	writeIf("主题", site.Topic)
	writeIf("简介", site.Summary)
	writeIf("详述", site.Description)
	if len(site.Facts) > 0 {
		keys := make([]string, 0, len(site.Facts))
		for k := range site.Facts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("结构化字段：\n")
		for _, k := range keys {
			fmt.Fprintf(&b, "  - %s：%s\n", k, site.Facts[k])
		}
	}
	writeIf("演绎说明（非史实）", site.Interpretation)
	if len(site.Sources) > 0 {
		b.WriteString("出处：\n")
		for _, src := range site.Sources {
			line := "  - " + src.Title
			if src.License != "" {
				line += "（许可：" + src.License + "）"
			}
			if src.URL != "" {
				line += " " + src.URL
			}
			b.WriteString(line + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
