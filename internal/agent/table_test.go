package agent

import (
	"reflect"
	"testing"
)

func TestSelectFilterAndNumericSort(t *testing.T) {
	headers, rows, err := readTable("name,category,amount\na,coffee,2\nb,tea,20\nc,coffee,10\n")
	if err != nil {
		t.Fatal(err)
	}
	a := map[string]any{"operation": "select", "filter_column": "category", "filter_value": "coffee", "select_columns": []any{"name", "amount"}, "sort_by": "amount", "sort_order": "desc", "sort_numeric": true}
	cols, output, err := calculateTable(headers, rows, a)
	if err != nil || !reflect.DeepEqual(cols, []string{"name", "amount"}) || !reflect.DeepEqual(output, [][]string{{"c", "10"}, {"a", "2"}}) {
		t.Fatal(cols, output, err)
	}
	a["sort_by"] = "missing"
	if _, _, err = calculateTable(headers, rows, a); err == nil {
		t.Fatal("unknown sort accepted")
	}
}
func TestInvalidNumericValuesNeverBecomeZero(t *testing.T) {
	h, r, _ := readTable("category,amount\ncoffee,not-a-number\n")
	if _, _, err := calculateTable(h, r, map[string]any{"operation": "sum", "group_by": "category", "value": "amount"}); err == nil {
		t.Fatal("invalid number accepted")
	}
}
