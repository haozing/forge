package site

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStyleConfigDefaults(t *testing.T) {
	config, err := ParseStyleConfig(nil)
	if err != nil {
		t.Fatalf("empty document must parse: %v", err)
	}
	if config.Preset != DefaultStylePreset {
		t.Fatalf("preset default = %s", config.Preset)
	}
	if config.BodySize != 16 || config.ReadingWidth != 720 {
		t.Fatalf("calm preset defaults not applied: %+v", config)
	}
	if err := config.CheckContrast(); err != nil {
		t.Fatalf("default palette must pass contrast: %v", err)
	}
}

func TestAllPresetsPassContrast(t *testing.T) {
	for name := range stylePresetsDefinition {
		document, err := json.Marshal(map[string]any{"preset": name})
		if err != nil {
			t.Fatal(err)
		}
		config, err := ParseStyleConfig(document)
		if err != nil {
			t.Fatalf("preset %s must parse: %v", name, err)
		}
		if config.Preset != name {
			t.Fatalf("preset %s not applied", name)
		}
		if err := config.CheckContrast(); err != nil {
			t.Fatalf("preset %s fails WCAG AA: %v", name, err)
		}
	}
}

func TestParseStyleConfigOverrides(t *testing.T) {
	document := json.RawMessage(`{
		"preset": "magazine",
		"tokens": {
			"color": {"primary": "#1B4F91", "mode": "dark"},
			"typography": {"heading_font": "sans", "body_size": 18, "reading_width": 800},
			"density": "compact", "radius": "round", "shadow": "lifted"
		},
		"layout": {"home_style": "hero", "list_style": "grid", "card_ratio": "4:3", "sidebar": "tags"},
		"ia": {"home_components": ["latest", "tag_cloud"], "summary_length": 200, "posts_per_page": 20}
	}`)
	config, err := ParseStyleConfig(document)
	if err != nil {
		t.Fatalf("valid overrides rejected: %v", err)
	}
	if config.PrimaryColor != "#1B4F91" || config.ColorMode != "dark" || config.BodySize != 18 ||
		config.ReadingWidth != 800 || config.Density != "compact" || config.Radius != "round" ||
		config.Shadow != "lifted" || config.HomeStyle != "hero" || config.ListStyle != "grid" ||
		config.CardRatio != "4:3" || config.Sidebar != "tags" || config.SummaryLength != 200 ||
		config.PostsPerPage != 20 {
		t.Fatalf("overrides not applied: %+v", config)
	}
	if len(config.HomeComponents) != 2 || config.HomeComponents[0] != "latest" || config.HomeComponents[1] != "tag_cloud" {
		t.Fatalf("home_components order wrong: %v", config.HomeComponents)
	}
}

func TestParseStyleConfigRejections(t *testing.T) {
	cases := map[string]string{
		"unknown preset":        `{"preset":"nope"}`,
		"unknown top key":       `{"tokensx":{}}`,
		"unknown token key":     `{"tokens":{"weight":1}}`,
		"bad hex":               `{"tokens":{"color":{"primary":"green"}}}`,
		"bad mode":              `{"tokens":{"color":{"mode":"sepia"}}}`,
		"bad font":              `{"tokens":{"typography":{"heading_font":"comic"}}}`,
		"body_size low":         `{"tokens":{"typography":{"body_size":14}}}`,
		"body_size high":        `{"tokens":{"typography":{"body_size":20}}}`,
		"body_size fractional":  `{"tokens":{"typography":{"body_size":16.5}}}`,
		"reading_width low":     `{"tokens":{"typography":{"reading_width":600}}}`,
		"bad density":           `{"tokens":{"density":"dense"}}`,
		"bad home_style":        `{"layout":{"home_style":"landing"}}`,
		"bad card_ratio":        `{"layout":{"card_ratio":"21:9"}}`,
		"unknown component":     `{"ia":{"home_components":["carousel"]}}`,
		"duplicate components":  `{"ia":{"home_components":["latest","latest"]}}`,
		"too many components":   `{"ia":{"home_components":["featured","latest","tag_cloud","extra"]}}`,
		"summary_length high":   `{"ia":{"summary_length":400}}`,
		"posts_per_page low":    `{"ia":{"posts_per_page":3}}`,
		"low contrast primary":  `{"tokens":{"color":{"primary":"#EEEEEE"}}}`,
		"non object document":   `[1,2,3]`,
	}
	for name, document := range cases {
		if _, err := ParseStyleConfig(json.RawMessage(document)); err == nil {
			t.Errorf("%s: document accepted", name)
		}
	}
}

func TestMergeStylePatchDeepMerge(t *testing.T) {
	current := json.RawMessage(`{"preset":"calm","tokens":{"color":{"primary":"#2E7D32"},"density":"normal"},"layout":{"list_style":"list"}}`)
	patch := json.RawMessage(`{"tokens":{"color":{"primary":"#1B4F91"},"mode_if_absent":null},"layout":{"list_style":"grid"}}`)
	merged, err := MergeStylePatch(current, patch)
	if err != nil {
		// mode_if_absent is an unknown key and must reject the patch.
		if !strings.Contains(err.Error(), "unknown style key") {
			t.Fatalf("unexpected error: %v", err)
		}
	} else {
		t.Fatal("unknown key in patch accepted")
	}
	patch = json.RawMessage(`{"tokens":{"color":{"primary":"#1B4F91"}},"layout":{"list_style":"grid"}}`)
	merged, err = MergeStylePatch(current, patch)
	if err != nil {
		t.Fatalf("valid patch rejected: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(merged, &document); err != nil {
		t.Fatal(err)
	}
	tokens := document["tokens"].(map[string]any)
	color := tokens["color"].(map[string]any)
	if color["primary"] != "#1B4F91" {
		t.Fatalf("primary not merged: %v", color)
	}
	if tokens["density"] != "normal" {
		t.Fatalf("sibling token wiped by deep merge: %v", tokens)
	}
	layout := document["layout"].(map[string]any)
	if layout["list_style"] != "grid" {
		t.Fatalf("layout not merged: %v", layout)
	}
	if document["preset"] != "calm" {
		t.Fatalf("preset lost: %v", document)
	}
}

func TestMergeStylePatchNullResets(t *testing.T) {
	current := json.RawMessage(`{"tokens":{"color":{"primary":"#1B4F91"},"density":"compact"},"layout":{"list_style":"grid"}}`)
	merged, err := MergeStylePatch(current, json.RawMessage(`{"tokens":{"density":null}}`))
	if err != nil {
		t.Fatalf("null reset rejected: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(merged, &document); err != nil {
		t.Fatal(err)
	}
	tokens := document["tokens"].(map[string]any)
	if _, exists := tokens["density"]; exists {
		t.Fatalf("null leaf must delete the key: %v", tokens)
	}
	if tokens["color"].(map[string]any)["primary"] != "#1B4F91" {
		t.Fatalf("null reset wiped a sibling: %v", tokens)
	}
	if _, err := ParseStyleConfig(merged); err != nil {
		t.Fatalf("merged document must fully validate: %v", err)
	}
}

func TestMergeStylePatchRejectsLowContrast(t *testing.T) {
	// Regression: the merge path must enforce the WCAG gate too, not only
	// the fresh-parse path (found by itd_p8 S1-STYLE-REJECT-low-contrast).
	base, err := MergeStylePatch(json.RawMessage(`{"preset":"magazine"}`), json.RawMessage(`{"tokens":{"color":{"primary":"#1B4F91"}}}`))
	if err != nil {
		t.Fatalf("valid merge rejected: %v", err)
	}
	if _, err := MergeStylePatch(base, json.RawMessage(`{"tokens":{"color":{"primary":"#EEEEEE"}}}`)); err == nil {
		t.Fatal("low-contrast primary accepted through the merge path")
	}
}

func TestContrastRatioKnownPairs(t *testing.T) {
	if ratio := ContrastRatio("#000000", "#FFFFFF"); ratio < 20 || ratio > 21 {
		t.Fatalf("black/white ratio = %.2f", ratio)
	}
	if ratio := ContrastRatio("#2E7D32", "#FFFFFF"); ratio < 4.5 {
		t.Fatalf("calm primary/white = %.2f (below AA)", ratio)
	}
	if ratio := ContrastRatio("not-a-color", "#FFFFFF"); ratio != 0 {
		t.Fatalf("malformed color must fail closed, got %.2f", ratio)
	}
}
