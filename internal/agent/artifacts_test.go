package agent

import (
	"context"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"strings"
	"testing"
)

func TestAnalysisUsesOriginalNumbersAndChartProvenance(t *testing.T) {
	calls := map[string]int{}
	g := fakeModel(func(_ context.Context, m []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		role := "Mike"
		if strings.Contains(m[0].Content, "的 David。") {
			role = "David"
		}
		n := calls[role]
		calls[role]++
		if role == "Mike" {
			if n == 0 {
				return call("assign_step", map[string]any{"role": "David", "goal": "按品类汇总金额"}), core.Usage{}, nil
			}
			return call("finish_task", map[string]any{"summary": "统计完成", "answer_only": false}), core.Usage{}, nil
		}
		var msg core.ModelMessage
		switch n {
		case 0:
			msg = call("analyze_table", map[string]any{"source": "inputs/sales.csv", "group_by": "category", "value": "amount", "operation": "sum", "output": "totals.csv"})
		case 1:
			msg = call("create_chart", map[string]any{"source": "totals.csv", "kind": "bar", "title": "销售额", "output": "sales.chart.json"})
		case 2:
			msg = call("check_artifacts", map[string]any{})
		default:
			msg = core.ModelMessage{Role: "assistant", Content: "咖啡 48，茶 12，实际计算并生成图表"}
		}
		return msg, core.Usage{}, nil
	})
	e, p := testEngine(t, g)
	source := []byte("category,amount\n咖啡,20\n咖啡,28\n茶,12\n")
	hash := core.Hash(source)
	_, err := e.Store.Mutate("alice", p.ID, map[string][]byte{hash: source}, func(p *core.Project) error {
		p.Files["inputs/sales.csv"] = core.File{Hash: hash, Size: len(source), Kind: "data", ReadOnly: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start("alice", p.ID, "汇总销售额并作图", "build", "start", "submit", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Tasks[p.CurrentTask].Status == "completed" })
	b, _ := e.Store.Blob("alice", p.Files["totals.csv"].Hash)
	if string(b) != "group,value\n咖啡,48\n茶,12\n" {
		t.Fatal("incorrect arithmetic", string(b))
	}
	chart, _ := e.Store.Blob("alice", p.Files["sales.chart.json"].Hash)
	if !strings.Contains(string(chart), p.Files["totals.csv"].Hash) {
		t.Fatal("chart missing source provenance")
	}
	if p.Files["inputs/sales.csv"].Hash != hash {
		t.Fatal("source changed")
	}
}
func TestStaticChecksDoNotClaimRuntimeSuccess(t *testing.T) {
	files := map[string]string{"index.html": "<button>ok</button>", "app.js": "throw new Error('runtime error')"}
	manifest := core.Manifest{}
	for n, c := range files {
		manifest[n] = core.File{Hash: core.Hash([]byte(c)), Size: len(c)}
	}
	r := checkArtifacts(files, manifest)
	if r.Status != "passed" {
		t.Fatal("valid syntax expected")
	}
	p := core.NewProject("a", "test")
	p.Files = manifest
	p.Reports = []core.Report{r}
	if completionCheck(p) == nil {
		t.Fatal("static-only web marked deliverable")
	}
	for _, input := range []string{"x,x\n1,2", `[{"a":1}] trailing`, `[{"a":{"nested":1}}]`} {
		if ValidateTable(input) == nil {
			t.Fatal("invalid original table accepted", input)
		}
	}
}
