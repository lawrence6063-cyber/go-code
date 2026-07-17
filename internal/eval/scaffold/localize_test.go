package scaffold

import (
	"reflect"
	"testing"
)

// TestRankFiles_RelevanceOrder 覆盖：与 query 词高重合的文件排在前。
func TestRankFiles_RelevanceOrder(t *testing.T) {
	docs := []Doc{
		{Path: "src/session.py", Text: "class Session: def resolve_redirects(self, req): handle cookie merge across redirect"},
		{Path: "src/models.py", Text: "class Response: status_code headers content"},
		{Path: "src/utils.py", Text: "def to_key_val_list(value): helper utilities"},
	}
	got := RankFiles("Session.resolve_redirects loses cookie on redirect", docs, 2)
	if len(got) == 0 || got[0] != "src/session.py" {
		t.Errorf("top file = %v, want src/session.py first", got)
	}
	if len(got) > 2 {
		t.Errorf("k=2 should cap to 2, got %d", len(got))
	}
}

// TestRankFiles_PathTokensMatter 覆盖：文件名/路径 token 也参与匹配。
func TestRankFiles_PathTokensMatter(t *testing.T) {
	docs := []Doc{
		{Path: "django/db/models/query.py", Text: "generic orm code"},
		{Path: "django/http/response.py", Text: "generic http code"},
	}
	got := RankFiles("bug in queryset query filtering", docs, 1)
	if len(got) != 1 || got[0] != "django/db/models/query.py" {
		t.Errorf("path-token match failed, got %v", got)
	}
}

// TestRankFiles_EmptyInputs 覆盖空文档/空查询。
func TestRankFiles_EmptyInputs(t *testing.T) {
	if got := RankFiles("anything", nil, 5); got != nil {
		t.Errorf("empty docs should return nil, got %v", got)
	}
	if got := RankFiles("", []Doc{{Path: "a", Text: "b"}}, 5); got != nil {
		t.Errorf("empty query should return nil, got %v", got)
	}
}

// TestRankFiles_Deterministic 覆盖并列打分按路径稳定排序。
func TestRankFiles_Deterministic(t *testing.T) {
	docs := []Doc{
		{Path: "b.py", Text: "cookie redirect session"},
		{Path: "a.py", Text: "cookie redirect session"},
	}
	got := RankFiles("cookie redirect", docs, 0)
	want := []string{"a.py", "b.py"} // 同分 → 路径升序
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tie order = %v, want %v", got, want)
	}
}

// TestRankFiles_IrrelevantDropped 覆盖：得分为 0 的无关文件不返回。
func TestRankFiles_IrrelevantDropped(t *testing.T) {
	docs := []Doc{
		{Path: "match.py", Text: "cookie redirect session"},
		{Path: "nope.py", Text: "totally unrelated widget rendering"},
	}
	got := RankFiles("cookie redirect", docs, 0)
	if len(got) != 1 || got[0] != "match.py" {
		t.Errorf("irrelevant file should be dropped, got %v", got)
	}
}
