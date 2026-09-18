// Package textutil — 无状态检索文本原语（借鉴 agentblog infra/retrieval 的
// 分层哲学）：词法转义、术语提取、泛型 RRF 融合、内容指纹。零 IO、零
// 第三方依赖；SQL 组装归各域 repository。任何域（query/modeling/lint）
// 需要检索词处理或结果融合时复用同一实现，不再各自为政。
package textutil

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// ---------- 词法转义 ----------

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike escapes the LIKE wildcards (%, _, \) so user input can be
// embedded in a LIKE pattern verbatim.
func EscapeLike(input string) string {
	return likeEscaper.Replace(input)
}

// ---------- 术语提取 ----------

var (
	reASCIIWord = regexp.MustCompile(`[0-9A-Za-z_]+`)
	reCJK       = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3040}-\x{30FF}]`)
	reCJKPair   = regexp.MustCompile(`[\x{4E00}-\x{9FFF}\x{3040}-\x{30FF}]{2}`)
)

// ExtractTerms 提取检索词：ASCII 词原样 + CJK 二元组（bigram，LIKE 场景
// 不依赖分词器），去重后按出现顺序截断到 limit。
func ExtractTerms(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := map[string]bool{}
	terms := make([]string, 0, limit)
	add := func(term string) bool {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] {
			return true
		}
		seen[term] = true
		terms = append(terms, term)
		return len(terms) < limit
	}
	for _, word := range reASCIIWord.FindAllString(text, -1) {
		if !add(strings.ToLower(word)) {
			return terms
		}
	}
	for _, pair := range reCJKPair.FindAllString(text, -1) {
		if !add(pair) {
			return terms
		}
	}
	// 单个 CJK 字符（成对提取漏掉的边界字）。
	for _, char := range reCJK.FindAllString(text, -1) {
		if !add(char) {
			return terms
		}
	}
	return terms
}

// ---------- RRF 融合 ----------

const defaultRRFk = 60

// RankedList 是一路排序结果（ID + 可选载荷），顺序即排名（0 最优）。
type RankedList[T any] struct {
	ID    string
	Value T
}

// RRF 融合多路排序结果（Reciprocal Rank Fusion，k 取行业标准 60）：
// 每路贡献 1/(k+rank+1)，总分相同者按输入顺序稳定保序。T 任意（无需
// 可比较），融合只依赖 ID 与名次。
func RRF[T any](k float64, lists ...[]RankedList[T]) []RankedList[T] {
	if k <= 0 {
		k = defaultRRFk
	}
	scores := map[string]float64{}
	first := map[string]T{}
	for _, list := range lists {
		for rank, item := range list {
			scores[item.ID] += 1 / (k + float64(rank+1))
			if _, ok := first[item.ID]; !ok {
				first[item.ID] = item.Value
			}
		}
	}
	ids := make([]string, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if scores[ids[i]] != scores[ids[j]] {
			return scores[ids[i]] > scores[ids[j]]
		}
		return false // 同分保持输入顺序（SliceStable）
	})
	result := make([]RankedList[T], 0, len(ids))
	for _, id := range ids {
		result = append(result, RankedList[T]{ID: id, Value: first[id]})
	}
	return result
}

// ---------- 内容指纹 ----------

// Fingerprint 是内容的规范化 sha256 指纹：文档判重、嵌入失效检测（嵌入
// 模型版本变更时指纹一并变更）共用。
func Fingerprint(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}
