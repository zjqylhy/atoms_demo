package agent

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/net/html"
	"io"
	"mockatoms/internal/core"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func checkArtifacts(files map[string]string, manifest core.Manifest) core.Report {
	r := core.Report{ID: core.ID(), Kind: "static", Digest: manifest.Digest(), At: core.Now(), Status: "passed", Checks: []core.Check{}}
	add := func(name, status, detail string) {
		r.Checks = append(r.Checks, core.Check{Name: name, Status: status, Detail: detail})
		if status == "failed" {
			r.Status = "failed"
		}
	}
	if len(files) == 0 {
		add("files", "failed", "项目没有文件")
	}
	names := []string{}
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		content := files[name]
		if manifest[name].Binary {
			add(name, "not_run", "二进制文件已保存，须使用内容断言覆盖验收条件")
			continue
		}
		if strings.TrimSpace(content) == "" {
			add(name, "failed", "文件内容为空")
			continue
		}
		switch path.Ext(name) {
		case ".js", ".mjs", ".cjs", ".jsx", ".ts", ".mts", ".cts", ".tsx", ".css":
			loader := api.LoaderJS
			switch path.Ext(name) {
			case ".jsx":
				loader = api.LoaderJSX
			case ".ts", ".mts", ".cts":
				loader = api.LoaderTS
			case ".tsx":
				loader = api.LoaderTSX
			case ".css":
				loader = api.LoaderCSS
			}
			result := api.Transform(content, api.TransformOptions{Loader: loader, Sourcefile: name, LogLevel: api.LogLevelSilent})
			if len(result.Errors) > 0 {
				add(name, "failed", result.Errors[0].Text)
			} else {
				add(name, "passed", "语法检查通过；运行需单独验证")
			}
		case ".html":
			if _, err := html.Parse(strings.NewReader(content)); err != nil {
				add(name, "failed", err.Error())
				continue
			}
			bad := ""
			tokens := html.NewTokenizer(strings.NewReader(content))
			for {
				typ := tokens.Next()
				if typ == html.ErrorToken {
					break
				}
				if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
					continue
				}
				token := tokens.Token()
				for _, a := range token.Attr {
					if a.Key != "src" && a.Key != "href" {
						continue
					}
					ref := strings.Split(strings.Split(a.Val, "?")[0], "#")[0]
					if ref == "" {
						continue
					}
					if strings.Contains(ref, ":") || strings.HasPrefix(ref, "//") {
						bad = "不支持外部资源或协议：" + a.Val
						break
					}
					target := path.Join(path.Dir(name), ref)
					if _, ok := files[target]; !ok {
						bad = "引用文件不存在：" + target
						break
					}
				}
			}
			if bad != "" {
				add(name, "failed", bad)
			} else {
				add(name, "passed", "HTML 可解析，项目资源引用存在")
			}
		case ".json":
			if !json.Valid([]byte(content)) {
				add(name, "failed", "JSON 格式无效")
			} else {
				add(name, "passed", "JSON 格式有效")
			}
		case ".csv":
			if _, _, err := readTable(content); err != nil {
				add(name, "failed", err.Error())
			} else {
				add(name, "passed", "CSV 字段与行格式有效")
			}
		case ".md", ".txt":
			add(name, "passed", "正文非空；事实与要求覆盖需另行评审")
		default:
			add(name, "not_run", "当前没有该文件类型的运行检查")
		}
	}
	return r
}
func completionCheck(p *core.Project) error {
	task := p.Tasks[p.CurrentTask]
	manifest, err := deliveryManifest(p, task)
	if err != nil {
		return err
	}
	if err := scopedAcceptance(p, task); err != nil {
		return err
	}
	staticCovered := map[string]bool{}
	passed := map[string]bool{}
	for _, r := range p.Reports {
		if !reportCurrent(p, task, r) {
			continue
		}
		kind := r.Kind
		ok := r.Status == "passed"
		if kind == "static" && ok {
			checked := p.Files
			if r.Evidence != nil {
				checked = r.Evidence.Inputs
			}
			for name := range checked {
				staticCovered[name] = true
			}
		}
		if execution := reportExecution(r); execution != "" {
			kind = execution
		}
		if kind == "go" {
			build, test := false, false
			for _, check := range r.Checks {
				build = build || (check.Name == "go_build" && check.Status == "passed")
				test = test || (check.Name == "go_test" && check.Status == "passed")
			}
			ok = ok && build && test
		}
		passed[kind] = ok
	}
	// Historical reports used a single run kind for both JS scripts and web builds.
	for _, kind := range []string{"web", "javascript"} {
		if _, recorded := passed[kind]; !recorded {
			passed[kind] = passed["run"]
		}
	}
	for name := range manifest {
		if !staticCovered[name] {
			return fmt.Errorf("当前交付范围必须先通过 check_artifacts：%s", name)
		}
	}
	web, script, typescript, python, golang := false, false, false, false, false
	for name, f := range manifest {
		if task != nil && task.Plan != nil && task.Plan.Delivery != nil {
			d := task.Plan.Delivery
			if !slices.Contains(d.Targets, name) && !slices.Contains(d.Preserve, name) {
				continue
			}
		}
		if f.Binary && (task == nil || task.Plan == nil || task.Plan.Delivery == nil) {
			return errors.New("二进制交付必须声明交付对象及内容验收条件")
		}
		if f.ReadOnly {
			continue
		}
		switch path.Ext(name) {
		case ".html":
			web = true
		case ".js", ".mjs", ".cjs", ".jsx":
			script = true
		case ".ts", ".mts", ".cts", ".tsx":
			typescript = true
		case ".py":
			python = true
		case ".go":
			golang = true
		}
	}
	if golang && !passed["go"] {
		return errors.New("Go 项目必须通过当前文件版本的 go build 与 go test")
	}
	if python && !passed["python"] {
		return errors.New("Python 脚本必须通过当前文件版本的实际执行与输出断言")
	}
	if web {
		if !passed["web"] || !passed["browser"] {
			return errors.New("网页必须通过当前文件版本的真实运行与浏览器验收")
		}
	} else if typescript && !passed["typescript"] {
		return errors.New("TypeScript 脚本必须通过当前文件版本的转译、执行与输出断言")
	} else if script && !golang && !python && !typescript && !passed["javascript"] {
		return errors.New("脚本必须经过真实运行和输出断言")
	}
	return nil
}
func ValidateTable(content string) error { _, _, err := readTable(content); return err }

func readTable(content string) ([]string, []map[string]string, error) {
	rows := []map[string]string{}
	headers := []string{}
	if strings.HasPrefix(strings.TrimSpace(content), "[") {
		var input []map[string]any
		dec := json.NewDecoder(strings.NewReader(content))
		dec.UseNumber()
		if err := dec.Decode(&input); err != nil {
			return nil, nil, err
		}
		var trailing any
		if dec.Decode(&trailing) != io.EOF {
			return nil, nil, errors.New("JSON 原件包含多余内容")
		}
		keys := map[string]bool{}
		for _, row := range input {
			for key := range row {
				keys[key] = true
			}
		}
		for key := range keys {
			headers = append(headers, key)
		}
		sort.Strings(headers)
		for _, row := range input {
			out := map[string]string{}
			for _, h := range headers {
				v := row[h]
				switch v.(type) {
				case map[string]any, []any:
					return nil, nil, errors.New("仅接受一层表格 JSON")
				}
				if v != nil {
					out[h] = fmt.Sprint(v)
				}
			}
			rows = append(rows, out)
		}
	} else {
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(content, "\ufeff")))
		h, err := reader.Read()
		if err != nil {
			return nil, nil, err
		}
		headers = h
		seen := map[string]bool{}
		for _, col := range headers {
			if col == "" || seen[col] {
				return nil, nil, errors.New("列名为空或重复")
			}
			seen[col] = true
		}
		for {
			values, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, nil, err
			}
			row := map[string]string{}
			for i, h := range headers {
				row[h] = values[i]
			}
			rows = append(rows, row)
			if len(rows) > 2000 {
				return nil, nil, errors.New("最多 2000 行")
			}
		}
	}
	if len(headers) == 0 || len(headers) > 32 || len(rows) > 2000 {
		return nil, nil, errors.New("表格需要 1–32 列且不超过 2000 行")
	}
	return headers, rows, nil
}
func (e *Engine) analyze(s scope, c core.Call, a map[string]any, p *core.Project) (string, error) {
	var failure error
	source := str(a, "source")
	file, ok := p.Files[source]
	if !ok || !file.ReadOnly {
		failure = errors.New("分析必须引用平台保存的只读输入原件")
	}
	var headers []string
	var rows []map[string]string
	if failure == nil {
		b, err := e.Store.Blob(s.Owner, file.Hash)
		if err != nil {
			return "", err
		}
		headers, rows, failure = readTable(string(b))
	}
	var columns []string
	var records [][]string
	if failure == nil {
		columns, records, failure = calculateTable(headers, rows, a)
	}
	if failure != nil {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, failure })
	}
	var out strings.Builder
	writer := csv.NewWriter(&out)
	writer.Write(columns)
	writer.WriteAll(records)
	writer.Flush()
	content := out.String()
	hash := core.Hash([]byte(content))
	name := str(a, "output")
	return e.record(s, c, map[string][]byte{hash: []byte(content)}, func(p *core.Project) (any, error) {
		if path.Ext(name) != ".csv" {
			return nil, errors.New("分析结果须保存为 CSV")
		}
		if _, exists := p.Files[name]; exists {
			return nil, errors.New("输出路径已存在，请选择新结果路径")
		}
		if err := write(p, name, content, "", false); err != nil {
			return nil, err
		}
		report := core.Report{ID: core.ID(), Kind: "analysis", Digest: p.Files.Digest(), At: core.Now(), Status: "passed", Checks: []core.Check{{Name: name, Status: "passed", Detail: "基于输入原件执行确定性计算"}}, Details: json.RawMessage(core.JSON(map[string]any{"source": source, "source_hash": file.Hash, "output": name, "output_hash": hash, "parameters": a, "input_rows": len(rows), "output_rows": len(records)}))}
		p.Reports = append(p.Reports, report)
		return map[string]any{"path": name, "hash": hash, "rows": len(records), "report": report.ID}, nil
	})
}
func (e *Engine) chart(s scope, c core.Call, a map[string]any, p *core.Project) (string, error) {
	source := str(a, "source")
	file, ok := p.Files[source]
	valid := false
	for _, r := range p.Reports {
		if r.Kind == "analysis" && r.Status == "passed" {
			var d map[string]any
			_ = json.Unmarshal(r.Details, &d)
			if d["output"] == source && d["output_hash"] == file.Hash {
				valid = true
			}
		}
	}
	if !ok || !valid {
		return e.record(s, c, nil, func(*core.Project) (any, error) {
			return nil, errors.New("图表必须引用未改动的真实分析结果")
		})
	}
	data, err := e.Store.Blob(s.Owner, file.Hash)
	if err != nil {
		return "", err
	}
	_, rows, err := readTable(string(data))
	if err != nil {
		return "", err
	}
	if len(rows) > 200 {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, errors.New("图表最多 200 个数据点") })
	}
	points := []any{}
	for _, row := range rows {
		v, err := strconv.ParseFloat(row["value"], 64)
		if err != nil {
			return "", err
		}
		points = append(points, map[string]any{"label": row["group"], "value": v})
	}
	content := core.JSON(map[string]any{"type": str(a, "kind"), "title": str(a, "title"), "source": source, "source_hash": file.Hash, "points": points})
	hash := core.Hash([]byte(content))
	name := str(a, "output")
	return e.record(s, c, map[string][]byte{hash: []byte(content)}, func(p *core.Project) (any, error) {
		if !strings.HasSuffix(name, ".chart.json") {
			return nil, errors.New("图表路径必须以 .chart.json 结尾")
		}
		if _, ok := p.Files[name]; ok {
			return nil, errors.New("图表输出路径已存在")
		}
		if err := write(p, name, content, "", false); err != nil {
			return nil, err
		}
		return map[string]any{"path": name, "hash": hash, "points": len(points)}, nil
	})
}
