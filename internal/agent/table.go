package agent

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// calculateTable is deliberately a small declarative query surface, not code or SQL.
func calculateTable(headers []string, input []map[string]string, a map[string]any) ([]string, [][]string, error) {
	has := func(name string) bool {
		for _, h := range headers {
			if h == name {
				return true
			}
		}
		return false
	}
	filter, group, value, op := str(a, "filter_column"), str(a, "group_by"), str(a, "value"), str(a, "operation")
	if filter != "" && !has(filter) {
		return nil, nil, errors.New("筛选字段不存在")
	}
	rows := []map[string]string{}
	for _, row := range input {
		if filter == "" || row[filter] == str(a, "filter_value") {
			rows = append(rows, row)
		}
	}
	columns := []string{"group", "value"}
	out := [][]string{}
	if op == "select" {
		columns = nil
		seen := map[string]bool{}
		if raw, ok := a["select_columns"].([]any); ok {
			for _, v := range raw {
				h, ok := v.(string)
				if !ok || !has(h) || seen[h] {
					return nil, nil, errors.New("选择字段不存在或重复")
				}
				seen[h] = true
				columns = append(columns, h)
			}
		}
		if len(columns) == 0 {
			columns = append(columns, headers...)
		}
		for _, row := range rows {
			record := []string{}
			for _, h := range columns {
				record = append(record, row[h])
			}
			out = append(out, record)
		}
	} else {
		if (group != "" && !has(group)) || (op != "count" && !has(value)) {
			return nil, nil, errors.New("指定字段不存在")
		}
		if op != "count" && op != "sum" && op != "mean" && op != "min" && op != "max" {
			return nil, nil, errors.New("统计操作不支持")
		}
		type aggregate struct {
			count         int
			sum, min, max float64
		}
		groups := map[string]*aggregate{}
		for i, row := range rows {
			key := "全部"
			if group != "" {
				key = row[group]
			}
			n := 0.0
			if op != "count" {
				var err error
				n, err = finiteNumber(row[value])
				if err != nil {
					return nil, nil, fmt.Errorf("筛选后第 %d 行字段 %s 不是有限数值", i+1, value)
				}
			}
			g := groups[key]
			if g == nil {
				g = &aggregate{min: n, max: n}
				groups[key] = g
			}
			g.count++
			g.sum += n
			g.min = math.Min(g.min, n)
			g.max = math.Max(g.max, n)
		}
		keys := []string{}
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			g := groups[k]
			n := float64(g.count)
			switch op {
			case "sum":
				n = g.sum
			case "mean":
				n = g.sum / float64(g.count)
			case "min":
				n = g.min
			case "max":
				n = g.max
			}
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, nil, errors.New("计算结果超出数值范围")
			}
			out = append(out, []string{k, strconv.FormatFloat(n, 'g', -1, 64)})
		}
	}
	sortBy := str(a, "sort_by")
	if sortBy != "" {
		column := -1
		for i, h := range columns {
			if h == sortBy {
				column = i
			}
		}
		if column < 0 {
			return nil, nil, errors.New("排序字段不在输出列中")
		}
		numeric := a["sort_numeric"] == true || (op != "select" && sortBy == "value")
		if numeric {
			for _, row := range out {
				if _, err := finiteNumber(row[column]); err != nil {
					return nil, nil, errors.New("数值排序字段包含非数值")
				}
			}
		}
		desc := str(a, "sort_order") == "desc"
		sort.SliceStable(out, func(i, j int) bool {
			if numeric {
				x, _ := finiteNumber(out[i][column])
				y, _ := finiteNumber(out[j][column])
				if desc {
					return x > y
				}
				return x < y
			}
			if desc {
				return out[i][column] > out[j][column]
			}
			return out[i][column] < out[j][column]
		})
	}
	return columns, out, nil
}
func finiteNumber(s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, errors.New("不是有限数值")
	}
	return n, nil
}
