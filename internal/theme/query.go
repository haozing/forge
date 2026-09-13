package theme

import (
	"fmt"
	"sync/atomic"
)

// QueryResult / QueryItem 是模板内 query 原语的返回（§4.5）。
// 数据源由 site 层注入：派生收录视图 + 三道闸视图层生效，模板不存在
// 查出非公开数据的语法。

// QueryItem 是一条公开内容卡片。
type QueryItem struct {
	Title       string
	Href        string
	Summary     string
	PublishedOn string
	CoverURL    string
	// Fields 是该模型 public_view 白名单字段的「展示名 → 格式化值」。
	Fields map[string]string
	// Tags 是标签展示名。
	Tags []string
}

// QueryResult 是一次查询的结果页。
type QueryResult struct {
	Items []QueryItem
	Total int
}

// QueryParams 是 query 的声明式参数（模板传 dict）。
type QueryParams struct {
	Model  string
	Limit  int
	Sort   string
	Filter map[string]string // 公开字段 key → 精确值（public_view 白名单内）
}

// MaxQueryCalls 是单次渲染的 query 调用上限（§4.5）。
const MaxQueryCalls = 8

// MaxQueryLimit 是单次 query 的条数上限。
const MaxQueryLimit = 20

// Queries 是一次渲染的 query 执行环境：绑定站点与预算，模板经
// {{.Query (dict …)}} 调用（Page.Query 委托到这里）。
type Queries struct {
	impl   func(params QueryParams) (*QueryResult, error)
	budget atomic.Int32
}

// NewQueries 构造一次渲染的查询环境。impl 为 nil 时 query 返回空结果
//（引擎以默认主题渲染、且调用方未接数据面的场景）。
func NewQueries(impl func(params QueryParams) (*QueryResult, error)) *Queries {
	return &Queries{impl: impl}
}

// ParseParams 把模板 dict（map[string]any）解析为强类型参数，白名单校验。
func ParseParams(raw map[string]any) (QueryParams, error) {
	var p QueryParams
	if raw == nil {
		return p, fmt.Errorf("query: 参数为空")
	}
	if v, ok := raw["model"].(string); ok && v != "" {
		p.Model = v
	}
	if v, ok := raw["sort"].(string); ok {
		p.Sort = v
	}
	if v, ok := raw["limit"]; ok {
		switch n := v.(type) {
		case int:
			p.Limit = n
		case int64:
			p.Limit = int(n)
		case float64:
			p.Limit = int(n)
		}
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	if p.Limit > MaxQueryLimit {
		p.Limit = MaxQueryLimit
	}
	if fm, ok := raw["filter"].(map[string]any); ok && len(fm) > 0 {
		p.Filter = make(map[string]string, len(fm))
		for k, v := range fm {
			if sv, ok := v.(string); ok {
				p.Filter[k] = sv
			}
		}
	}
	return p, nil
}

// Run 执行一次查询：预算扣减 → 参数解析 → 委托实现。预算耗尽返回错误
//（渲染失败，模板作者在 validate 时即可发现拖库写法）。
func (q *Queries) Run(raw map[string]any) (*QueryResult, error) {
	if q == nil {
		return &QueryResult{}, nil
	}
	if q.budget.Add(1) > MaxQueryCalls {
		return nil, fmt.Errorf("query: 单次渲染最多 %d 次 query", MaxQueryCalls)
	}
	if q.impl == nil {
		return &QueryResult{}, nil
	}
	params, err := ParseParams(raw)
	if err != nil {
		return nil, err
	}
	return q.impl(params)
}

// QueryFor 是模板引擎 FuncMap 注入形态（无接收者），委托到本环境。
func (q *Queries) QueryFor(raw map[string]any) (*QueryResult, error) {
	return q.Run(raw)
}
