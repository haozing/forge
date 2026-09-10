package httpapi

// openapi_document_test.go — structural gate for the API contract file.
// openapi.yaml is the machine-readable contract published at /openapi.yaml
// (onboarding pack) as well as the source the schema gates read, so a
// duplicate mapping key anywhere in it silently drops the shadowed block for
// every real YAML consumer while the line-based gates keep passing. This test
// fails on duplicate keys and on the specific drift that regression caused
// (two `schemas:` maps under `components`).

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func loadOpenAPIRoot(t *testing.T) map[string]any {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	return document
}

func TestOpenAPIDocumentHasNoDuplicateKeys(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "openapi.yaml"))
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	// yaml.Node preserves structure, so a duplicate sibling key surfaces as a
	// repeated key for the same node rather than being silently overwritten.
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	var walk func(node *yaml.Node, path string)
	walk = func(node *yaml.Node, path string) {
		for _, child := range node.Content {
			switch child.Kind {
			case yaml.MappingNode:
				seen := map[string]int{}
				for i := 0; i+1 < len(child.Content); i += 2 {
					key := child.Content[i].Value
					if first, ok := seen[key]; ok {
						t.Errorf("duplicate key %q under %s (lines %d and %d)",
							key, path, first, child.Content[i].Line)
					}
					seen[key] = child.Content[i].Line
				}
			}
			if len(child.Content) > 0 {
				next := path
				if child.Kind == yaml.MappingNode {
					next = path + "." + child.Content[0].Value
				}
				walk(child, next)
			}
		}
	}
	walk(&document, "$")
}

func TestOpenAPIComponentsSchemasAreSingleMap(t *testing.T) {
	document := loadOpenAPIRoot(t)
	components, ok := document["components"].(map[string]any)
	if !ok {
		t.Fatal("openapi.yaml has no components mapping")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("components.schemas is missing or not a mapping")
	}
	// Schemas that live in the block which the historical duplicate key
	// shadowed; each must be reachable through the parsed document.
	for _, name := range []string{"PublicSite", "PublicSection", "PublicPost", "PublicSiteHome", "SiteCreate", "SitePatch"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("components.schemas.%s unreachable — schema block is likely shadowed or misplaced", name)
		}
	}
}
