package httpapi

import (
	"errors"
	"strings"
	"testing"

	assetservice "agentchunzhi/internal/asset"
)

func TestParseImportCSVRowsMapsHeaderAndCoercesTypes(t *testing.T) {
	payload := "\xef\xbb\xbf" + "title,markdown,priority,year,tags,flag\n" +
		"\"Shot, wide\",\"line1\nline2\",high,2024,\"[\"\"a\"\",1]\",true\n" +
		"Plain,Body,low,007,false-nope,x\n"
	rows, err := parseImportCSVRows([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	first := rows[0]
	if first["title"] != "Shot, wide" || first["markdown"] != "line1\nline2" {
		t.Fatalf("quoted cells must survive commas and newlines: %#v", first)
	}
	if first["priority"] != "high" {
		t.Fatalf("enum strings stay strings: %#v", first["priority"])
	}
	if year, ok := first["year"].(int64); !ok || year != 2024 {
		t.Fatalf("canonical integers coerce to numbers: %#v", first["year"])
	}
	if _, isSlice := first["tags"].([]any); !isSlice {
		t.Fatalf("JSON array cells should parse: %#v", first["tags"])
	}
	if flag, ok := first["flag"].(bool); !ok || !flag {
		t.Fatalf("boolean cells coerce: %#v", first["flag"])
	}
	second := rows[1]
	if second["year"] != "007" {
		t.Fatalf("leading-zero ids must not be mangled: %#v", second["year"])
	}
	if second["tags"] != "false-nope" {
		t.Fatalf("non-boolean text stays string: %#v", second["tags"])
	}
	if second["flag"] != "x" {
		t.Fatalf("trailing column mapping: %#v", second["flag"])
	}
}

func TestParseImportCSVRowsToleratesRaggedLinesPerRow(t *testing.T) {
	payload := "title,priority,size\n" + "Broken\n" + "\"Full\",high,wide,EXTRA\n"
	rows, err := parseImportCSVRows([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("short and long rows are both kept: %d", len(rows))
	}
	short := rows[0]
	marker, ok := short[assetservice.ImportPreRowErrorsKey].([]assetservice.ImportRowError)
	if !ok || len(marker) != 1 || marker[0].Code != "field_count" || marker[0].Message == "" {
		t.Fatalf("short row must carry a field_count finding: %#v", short)
	}
	long := rows[1]
	if long["column_4"] != "EXTRA" {
		t.Fatalf("cells beyond the header stay visible under column_N: %#v", long)
	}
}

func TestParseImportCSVRowsRejectsEmptyPayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":       "",
		"bom only":    "\xef\xbb\xbf",
		"header only": "title,priority\n",
		"blank lines": "title,priority\n\n   , \n\n",
	} {
		if _, err := parseImportCSVRows([]byte(payload)); !errors.Is(err, errEmptyImportPayload) {
			t.Fatalf("%s: expected empty payload error, got %v", name, err)
		}
	}
}

func TestParseImportCSVRowsFailsOnUnbalancedQuotes(t *testing.T) {
	payload := "title,priority\n\"Unclosed,high\n"
	if _, err := parseImportCSVRows([]byte(payload)); err == nil || errors.Is(err, errEmptyImportPayload) {
		t.Fatalf("malformed quoting must fail ingestion, got %v", err)
	}
}

func TestDetectImportFormatSniffsAndRespectsExplicitHints(t *testing.T) {
	cases := []struct {
		contentType string
		body        string
		want        string
	}{
		{"text/csv", "{\"x\":1}", "csv"},
		{"application/json; charset=utf-8", "title,a", "json"},
		{"application/csv", "", "csv"},
		{"application/json", "", "json"},
		{"", "{\"rows\":[]}", "json"},
		{"", "\xef\xbb\xbf a,b,c", "csv"},
		{"", "title,value\nA,1", "csv"},
		{"", "   [1,2]", "json"},
		{"", "", ""},
	}
	for _, item := range cases {
		got := detectImportFormat(item.contentType, []byte(item.body))
		if got != item.want {
			t.Fatalf("detect(%q, %q) = %q want %q", item.contentType, item.body, got, item.want)
		}
	}
	if detected := detectImportFormat("", []byte(strings.Repeat("x", 8))); detected != "csv" {
		t.Fatalf("leading non-JSON character sniffs as csv: %q", detected)
	}
}
