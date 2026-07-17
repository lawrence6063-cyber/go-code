// 本文件实现 SCAFFOLD_SPEC §4.4（S-E）结构化定位的纯逻辑部分：轻量 BM25 文件排序。
// 输入 issue 查询文本 + 候选文件文档（路径 + 内容片段），输出按相关度排序的 top-k 文件路径，
// 供 swebench Adapter 在 repair 提示前注入定位线索（收窄上下文）。纯函数、无 I/O（守 §3.3）。
package scaffold

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// Doc 是一个候选文件文档：Path 为相对仓库根路径，Text 为用于打分的文本（路径 + 内容片段）。
type Doc struct {
	Path string // 文件相对路径
	Text string // 打分文本（通常为路径 token + 内容前若干字节）
}

// BM25 调参（经典默认值）。
const (
	bm25K1 = 1.5 // 词频饱和参数
	bm25B  = 0.75
)

// RankFiles 用 BM25 对 docs 按 query 相关度排序，返回 top-k 文件路径（k<=0 返回全部）。
// 打分并列时按路径升序稳定排序，保证确定性。
func RankFiles(query string, docs []Doc, k int) []string {
	if len(docs) == 0 {
		return nil
	}
	qTerms := tokenize(query)
	if len(qTerms) == 0 {
		return nil
	}
	docTokens := make([][]string, len(docs))
	df := make(map[string]int)
	var totalLen int
	for i, d := range docs {
		toks := tokenize(d.Text + " " + pathTokens(d.Path))
		docTokens[i] = toks
		totalLen += len(toks)
		for t := range uniqueSet(toks) {
			df[t]++
		}
	}
	avgdl := float64(totalLen) / float64(len(docs))
	qSet := uniqueSet(qTerms)

	type scored struct {
		path  string
		score float64
	}
	ranked := make([]scored, len(docs))
	for i, toks := range docTokens {
		ranked[i] = scored{path: docs[i].Path, score: bm25Score(qSet, toks, df, len(docs), avgdl)}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		if ranked[a].score != ranked[b].score {
			return ranked[a].score > ranked[b].score
		}
		return ranked[a].path < ranked[b].path
	})
	out := make([]string, 0, len(ranked))
	for _, r := range ranked {
		if r.score <= 0 {
			continue // 完全不相关的文件不注入
		}
		out = append(out, r.path)
	}
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// bm25Score 计算一篇文档对查询词集合的 BM25 得分。
func bm25Score(qSet map[string]struct{}, docToks []string, df map[string]int, n int, avgdl float64) float64 {
	tf := termFreq(docToks)
	dl := float64(len(docToks))
	var score float64
	for term := range qSet {
		f := float64(tf[term])
		if f == 0 {
			continue
		}
		idf := math.Log(1 + (float64(n)-float64(df[term])+0.5)/(float64(df[term])+0.5))
		norm := f * (bm25K1 + 1) / (f + bm25K1*(1-bm25B+bm25B*dl/avgdl))
		score += idf * norm
	}
	return score
}

// termFreq 统计词频。
func termFreq(toks []string) map[string]int {
	m := make(map[string]int, len(toks))
	for _, t := range toks {
		m[t]++
	}
	return m
}

// uniqueSet 返回 token 去重集合。
func uniqueSet(toks []string) map[string]struct{} {
	m := make(map[string]struct{}, len(toks))
	for _, t := range toks {
		m[t] = struct{}{}
	}
	return m
}

// tokenize 把文本切成小写 token：按非字母数字分割，丢弃长度<2 的短词。
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// pathTokens 把文件路径拆成便于匹配的 token（目录名/文件名/去扩展名），加权路径信息。
func pathTokens(path string) string {
	replaced := strings.NewReplacer("/", " ", "\\", " ", ".", " ", "_", " ", "-", " ").Replace(path)
	return path + " " + replaced
}
