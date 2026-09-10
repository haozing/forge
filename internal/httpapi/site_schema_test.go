package httpapi

// site_schema_test.go — field-level contract gate for the site management
// surface: the site.Site representation struct and the openapi PublicSite
// schema must agree in both directions. The path-level gate cannot see
// field drift (the Notification gate set this precedent).

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"agentchunzhi/internal/site"
)

var siteSchemaPropertyRe = regexp.MustCompile(`(?m)^        ([a-z_]+): \{`)

func siteJSONFields(t *testing.T) map[string]bool {
	t.Helper()
	fields := map[string]bool{}
	structType := reflect.TypeOf(site.Site{})
	for i := 0; i < structType.NumField(); i++ {
		tag := structType.Field(i).Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}

func TestOpenAPIPublicSiteSchemaMatchesSiteRepresentation(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, line := range lines {
		if line == "    PublicSite:" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("PublicSite schema not found in openapi.yaml")
	}
	var block []string
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			break
		}
		block = append(block, line)
	}
	joined := strings.Join(block, "\n")
	propsIndex := strings.Index(joined, "properties:")
	if propsIndex < 0 {
		t.Fatal("PublicSite schema has no properties block")
	}
	schemaFields := map[string]bool{}
	for _, m := range siteSchemaPropertyRe.FindAllStringSubmatch(joined[propsIndex:], -1) {
		schemaFields[m[1]] = true
	}
	if len(schemaFields) == 0 {
		t.Fatal("no properties extracted from the PublicSite schema")
	}

	representation := siteJSONFields(t)
	for field := range representation {
		if !schemaFields[field] {
			t.Errorf("site representation field %q missing from the openapi PublicSite schema", field)
		}
	}
	for field := range schemaFields {
		if !representation[field] {
			t.Errorf("openapi PublicSite field %q not present on the Site representation", field)
		}
	}
}
