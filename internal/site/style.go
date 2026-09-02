package site

// style.go — the L1 style parameter space of the public-site delivery face
// (design doc §7). Style is a closed declarative document: preset ⊕ token
// overrides ⊕ layout ⊕ IA parameters. Validation runs on both the write path
// (PATCH style_config, release publish, agent patches) and the render path;
// the CSS generation itself lives in internal/delivery (StyleEngine).

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Style document enums (design doc §7.2). Every leaf outside these sets is
// rejected at both write and render time.
var (
	stylePresets = map[string]bool{"calm": true, "magazine": true, "minimal": true, "warm": true, "archive": true}

	styleColorModes     = map[string]bool{"light": true, "dark": true, "auto": true}
	styleHeadingFonts   = map[string]bool{"serif": true, "sans": true}
	styleDensities      = map[string]bool{"airy": true, "normal": true, "compact": true}
	styleRadii          = map[string]bool{"sharp": true, "soft": true, "round": true}
	styleShadows        = map[string]bool{"flat": true, "subtle": true, "lifted": true}
	styleHomeStyles     = map[string]bool{"hero": true, "plain": true, "grid": true}
	styleListStyles     = map[string]bool{"list": true, "grid": true}
	styleCardRatios     = map[string]bool{"16:9": true, "4:3": true, "1:1": true, "text": true}
	styleSidebars       = map[string]bool{"none": true, "toc": true, "tags": true}
	styleHomeComponents = map[string]bool{"featured": true, "latest": true, "tag_cloud": true}
)

// Style ranges (inclusive).
const (
	styleBodySizeMin, styleBodySizeMax       = 15, 19
	styleReadingWidthMin, styleReadingWidthMax = 640, 860
	styleSummaryLengthMin, styleSummaryLengthMax = 80, 320
	stylePostsPerPageMin, stylePostsPerPageMax   = 6, 24
	styleHomeComponentsMax                    = 3
)

// stylePreset is the factory palette and layout default set of one preset.
type stylePreset struct {
	Primary, Surface, Text, Muted string
	HeadingFont                   string
	BodySize, ReadingWidth        int
	Density, Radius, Shadow       string
	HomeStyle, ListStyle          string
	CardRatio, Sidebar            string
}

// stylePresetsDefinition carries every built-in preset. Factory palettes pass
// the WCAG AA 4.5:1 primary-on-surface contrast check (unit-tested).
var stylePresetsDefinition = map[string]stylePreset{
	"calm": {
		Primary: "#2E7D32", Surface: "#FFFFFF", Text: "#1B1F1C", Muted: "#5C6B62",
		HeadingFont: "sans", BodySize: 16, ReadingWidth: 720,
		Density: "normal", Radius: "soft", Shadow: "subtle",
		HomeStyle: "plain", ListStyle: "list", CardRatio: "text", Sidebar: "none",
	},
	"magazine": {
		Primary: "#8C2F39", Surface: "#FBF9F6", Text: "#231F20", Muted: "#6E675F",
		HeadingFont: "serif", BodySize: 16, ReadingWidth: 760,
		Density: "compact", Radius: "sharp", Shadow: "lifted",
		HomeStyle: "hero", ListStyle: "list", CardRatio: "16:9", Sidebar: "none",
	},
	"minimal": {
		Primary: "#111111", Surface: "#FFFFFF", Text: "#111111", Muted: "#666666",
		HeadingFont: "sans", BodySize: 16, ReadingWidth: 700,
		Density: "airy", Radius: "sharp", Shadow: "flat",
		HomeStyle: "plain", ListStyle: "list", CardRatio: "text", Sidebar: "none",
	},
	"warm": {
		Primary: "#B45309", Surface: "#FDF9F3", Text: "#292018", Muted: "#7A6A58",
		HeadingFont: "sans", BodySize: 17, ReadingWidth: 720,
		Density: "normal", Radius: "soft", Shadow: "subtle",
		HomeStyle: "hero", ListStyle: "grid", CardRatio: "4:3", Sidebar: "tags",
	},
	"archive": {
		Primary: "#2F5D74", Surface: "#F7F8F7", Text: "#1D2429", Muted: "#5B6B73",
		HeadingFont: "serif", BodySize: 16, ReadingWidth: 760,
		Density: "normal", Radius: "sharp", Shadow: "flat",
		HomeStyle: "plain", ListStyle: "grid", CardRatio: "text", Sidebar: "tags",
	},
}

// DefaultStylePreset is the fallback preset name.
const DefaultStylePreset = "calm"

// ResolveStylePreset returns the preset definition by name (calm when unset
// or unknown — unknown names never survive validation, this is the render-side
// tolerant default).
func ResolveStylePreset(name string) stylePreset {
	if preset, ok := stylePresetsDefinition[name]; ok {
		return preset
	}
	return stylePresetsDefinition[DefaultStylePreset]
}

// StyleConfig is the parsed, fully defaulted style document. Unset fields
// fall back to the preset defaults (design doc §7.2 patch-merge semantics).
type StyleConfig struct {
	Preset string
	// Color tokens; empty strings follow the preset.
	PrimaryColor, SurfaceColor, TextColor, ColorMode string
	// Typography.
	HeadingFont  string
	BodySize     int
	ReadingWidth int
	// Surface feel.
	Density, Radius, Shadow string
	// Layout.
	HomeStyle, ListStyle, CardRatio, Sidebar string
	// IA parameters.
	HomeComponents []string
	SummaryLength  int
	PostsPerPage   int
}

// ParseStyleConfig validates and normalizes one style document into the
// fully defaulted form. An empty document yields the calm preset defaults.
// Invalid values answer ErrInvalidInput (write and render both reject).
func ParseStyleConfig(raw json.RawMessage) (StyleConfig, error) {
	var document map[string]any
	if len(strings.TrimSpace(string(raw))) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err := decoder.Decode(&document); err != nil {
			return StyleConfig{}, fmt.Errorf("%w: style document is not a JSON object", ErrInvalidInput)
		}
		if document == nil {
			return StyleConfig{}, fmt.Errorf("%w: style document is not a JSON object", ErrInvalidInput)
		}
		if err := validateStyleDocument(document); err != nil {
			return StyleConfig{}, err
		}
	}
	presetName := DefaultStylePreset
	if value, ok := document["preset"].(string); ok && stylePresets[value] {
		presetName = value
	}
	preset := ResolveStylePreset(presetName)
	config := StyleConfig{
		Preset:         presetName,
		PrimaryColor:   preset.Primary,
		SurfaceColor:   preset.Surface,
		TextColor:      preset.Text,
		ColorMode:      "auto",
		HeadingFont:    preset.HeadingFont,
		BodySize:       preset.BodySize,
		ReadingWidth:   preset.ReadingWidth,
		Density:        preset.Density,
		Radius:         preset.Radius,
		Shadow:         preset.Shadow,
		HomeStyle:      preset.HomeStyle,
		ListStyle:      preset.ListStyle,
		CardRatio:      preset.CardRatio,
		Sidebar:        preset.Sidebar,
		HomeComponents: []string{"featured", "latest", "tag_cloud"},
		SummaryLength:  160,
		PostsPerPage:   12,
	}
	tokens, _ := document["tokens"].(map[string]any)
	if color, ok := tokens["color"].(map[string]any); ok {
		if value, ok := color["primary"].(string); ok && value != "" {
			config.PrimaryColor = value
		}
		if value, ok := color["surface"].(string); ok && value != "" {
			config.SurfaceColor = value
		}
		if value, ok := color["text"].(string); ok && value != "" {
			config.TextColor = value
		}
		if value, ok := color["mode"].(string); ok && styleColorModes[value] {
			config.ColorMode = value
		}
	}
	if typography, ok := tokens["typography"].(map[string]any); ok {
		if value, ok := typography["heading_font"].(string); ok && styleHeadingFonts[value] {
			config.HeadingFont = value
		}
		if number, ok := numberValue(typography["body_size"]); ok {
			config.BodySize = int(number)
		}
		if number, ok := numberValue(typography["reading_width"]); ok {
			config.ReadingWidth = int(number)
		}
	}
	if value, ok := tokens["density"].(string); ok && styleDensities[value] {
		config.Density = value
	}
	if value, ok := tokens["radius"].(string); ok && styleRadii[value] {
		config.Radius = value
	}
	if value, ok := tokens["shadow"].(string); ok && styleShadows[value] {
		config.Shadow = value
	}
	if layout, ok := document["layout"].(map[string]any); ok {
		if value, ok := layout["home_style"].(string); ok && styleHomeStyles[value] {
			config.HomeStyle = value
		}
		if value, ok := layout["list_style"].(string); ok && styleListStyles[value] {
			config.ListStyle = value
		}
		if value, ok := layout["card_ratio"].(string); ok && styleCardRatios[value] {
			config.CardRatio = value
		}
		if value, ok := layout["sidebar"].(string); ok && styleSidebars[value] {
			config.Sidebar = value
		}
	}
	if ia, ok := document["ia"].(map[string]any); ok {
		if components, ok := ia["home_components"].([]any); ok && len(components) > 0 {
			config.HomeComponents = config.HomeComponents[:0]
			for _, item := range components {
				name, _ := item.(string)
				config.HomeComponents = append(config.HomeComponents, name)
			}
		}
		if number, ok := numberValue(ia["summary_length"]); ok {
			config.SummaryLength = int(number)
		}
		if number, ok := numberValue(ia["posts_per_page"]); ok {
			config.PostsPerPage = int(number)
		}
	}
	if err := config.CheckContrast(); err != nil {
		return StyleConfig{}, err
	}
	return config, nil
}

// numberValue accepts json.Number or float64 leaves.
func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		return number, err == nil
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

// validateStyleDocument rejects unknown keys, wrong leaf types and out-of-range
// values. Null leaves are allowed here (patch semantics: null resets a token)
// — validation of the merged result happens in ParseStyleConfig.
func validateStyleDocument(document map[string]any) error {
	if err := validateStyleKeys("preset", document, map[string]bool{
		"preset": true, "tokens": true, "layout": true, "ia": true,
	}); err != nil {
		return err
	}
	if value, ok := document["preset"]; ok && value != nil {
		name, ok := value.(string)
		if !ok || !stylePresets[name] {
			return fmt.Errorf("%w: unknown style preset", ErrInvalidInput)
		}
	}
	tokens, _ := document["tokens"].(map[string]any)
	if document["tokens"] != nil && tokens == nil {
		return fmt.Errorf("%w: style tokens must be an object", ErrInvalidInput)
	}
	if err := validateStyleKeys("tokens", tokens, map[string]bool{
		"color": true, "typography": true, "density": true, "radius": true, "shadow": true,
	}); err != nil {
		return err
	}
	color, _ := tokens["color"].(map[string]any)
	if tokens["color"] != nil && color == nil {
		return fmt.Errorf("%w: style color tokens must be an object", ErrInvalidInput)
	}
	if err := validateStyleKeys("tokens.color", color, map[string]bool{
		"primary": true, "surface": true, "text": true, "mode": true,
	}); err != nil {
		return err
	}
	for _, key := range []string{"primary", "surface", "text"} {
		value, ok := color[key]
		if !ok || value == nil {
			continue
		}
		hex, ok := value.(string)
		if !ok || (hex != "" && !validStyleHex(hex)) {
			return fmt.Errorf("%w: style color %s must be a #rrggbb hex or empty", ErrInvalidInput, key)
		}
	}
	if value, ok := color["mode"]; ok && value != nil {
		mode, ok := value.(string)
		if !ok || !styleColorModes[mode] {
			return fmt.Errorf("%w: style color mode must be light, dark or auto", ErrInvalidInput)
		}
	}
	typography, _ := tokens["typography"].(map[string]any)
	if tokens["typography"] != nil && typography == nil {
		return fmt.Errorf("%w: style typography must be an object", ErrInvalidInput)
	}
	if err := validateStyleKeys("tokens.typography", typography, map[string]bool{
		"heading_font": true, "body_size": true, "reading_width": true,
	}); err != nil {
		return err
	}
	if value, ok := typography["heading_font"]; ok && value != nil {
		font, ok := value.(string)
		if !ok || !styleHeadingFonts[font] {
			return fmt.Errorf("%w: heading_font must be serif or sans", ErrInvalidInput)
		}
	}
	for _, entry := range []struct {
		key          string
		min, max     float64
	}{
		{"body_size", styleBodySizeMin, styleBodySizeMax},
		{"reading_width", styleReadingWidthMin, styleReadingWidthMax},
	} {
		value, ok := typography[entry.key]
		if !ok || value == nil {
			continue
		}
		number, ok := numberValue(value)
		if !ok || number != math.Trunc(number) || number < entry.min || number > entry.max {
			return fmt.Errorf("%w: style %s must be an integer between %d and %d",
				ErrInvalidInput, entry.key, int(entry.min), int(entry.max))
		}
	}
	for _, entry := range []struct {
		key   string
		allow map[string]bool
	}{
		{"density", styleDensities}, {"radius", styleRadii}, {"shadow", styleShadows},
	} {
		value, ok := tokens[entry.key]
		if !ok || value == nil {
			continue
		}
		choice, ok := value.(string)
		if !ok || !entry.allow[choice] {
			return fmt.Errorf("%w: style %s value is not allowed", ErrInvalidInput, entry.key)
		}
	}
	layout, _ := document["layout"].(map[string]any)
	if document["layout"] != nil && layout == nil {
		return fmt.Errorf("%w: style layout must be an object", ErrInvalidInput)
	}
	if err := validateStyleKeys("layout", layout, map[string]bool{
		"home_style": true, "list_style": true, "card_ratio": true, "sidebar": true,
	}); err != nil {
		return err
	}
	for _, entry := range []struct {
		key   string
		allow map[string]bool
	}{
		{"home_style", styleHomeStyles}, {"list_style", styleListStyles},
		{"card_ratio", styleCardRatios}, {"sidebar", styleSidebars},
	} {
		value, ok := layout[entry.key]
		if !ok || value == nil {
			continue
		}
		choice, ok := value.(string)
		if !ok || !entry.allow[choice] {
			return fmt.Errorf("%w: style layout %s value is not allowed", ErrInvalidInput, entry.key)
		}
	}
	ia, _ := document["ia"].(map[string]any)
	if document["ia"] != nil && ia == nil {
		return fmt.Errorf("%w: style ia must be an object", ErrInvalidInput)
	}
	if err := validateStyleKeys("ia", ia, map[string]bool{
		"home_components": true, "summary_length": true, "posts_per_page": true,
	}); err != nil {
		return err
	}
	if value, ok := ia["home_components"]; ok && value != nil {
		components, ok := value.([]any)
		if !ok || len(components) > styleHomeComponentsMax {
			return fmt.Errorf("%w: home_components must be an ordered subset of featured, latest, tag_cloud", ErrInvalidInput)
		}
		seen := map[string]bool{}
		for _, item := range components {
			name, ok := item.(string)
			if !ok || !styleHomeComponents[name] || seen[name] {
				return fmt.Errorf("%w: home_components must be an ordered subset of featured, latest, tag_cloud", ErrInvalidInput)
			}
			seen[name] = true
		}
	}
	for _, entry := range []struct {
		key      string
		min, max float64
	}{
		{"summary_length", styleSummaryLengthMin, styleSummaryLengthMax},
		{"posts_per_page", stylePostsPerPageMin, stylePostsPerPageMax},
	} {
		value, ok := ia[entry.key]
		if !ok || value == nil {
			continue
		}
		number, ok := numberValue(value)
		if !ok || number != math.Trunc(number) || number < entry.min || number > entry.max {
			return fmt.Errorf("%w: style ia %s must be an integer between %d and %d",
				ErrInvalidInput, entry.key, int(entry.min), int(entry.max))
		}
	}
	return nil
}

// validateStyleKeys rejects keys outside the allowed set of one object node.
func validateStyleKeys(node string, object map[string]any, allowed map[string]bool) error {
	for key := range object {
		if !allowed[key] {
			return fmt.Errorf("%w: unknown style key %s.%s", ErrInvalidInput, node, key)
		}
	}
	return nil
}

// validStyleHex accepts #rgb and #rrggbb.
func validStyleHex(value string) bool {
	if len(value) != 7 && len(value) != 4 {
		return false
	}
	if value[0] != '#' {
		return false
	}
	for index := 1; index < len(value); index++ {
		char := value[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

// MergeStylePatch deep-merges one partial style document over the stored one
// along the schema-defined object nodes (tokens, tokens.color,
// tokens.typography, layout, ia); null leaves reset the token to the preset
// default by deleting the key. The merged document is fully re-validated.
func MergeStylePatch(current, patch json.RawMessage) (json.RawMessage, error) {
	base := map[string]any{}
	if len(strings.TrimSpace(string(current))) > 0 {
		if err := json.Unmarshal(current, &base); err != nil {
			return nil, fmt.Errorf("%w: stored style document is invalid", ErrInvalidInput)
		}
	}
	var overlay map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(patch)))
	decoder.UseNumber()
	if err := decoder.Decode(&overlay); err != nil || overlay == nil {
		return nil, fmt.Errorf("%w: style patch must be a JSON object", ErrInvalidInput)
	}
	if err := validateStyleDocument(overlay); err != nil {
		return nil, err
	}
	// Deep-merge along the fixed object nodes so one token patch never wipes
	// the other stored tokens.
	merged := deepMergeStyle(base, overlay)
	if err := validateStyleDocument(merged); err != nil {
		return nil, err
	}
	body, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	// The full parse enforces the value gates validateStyleDocument cannot
	// see — the WCAG primary/surface contrast check in particular.
	if _, err := ParseStyleConfig(body); err != nil {
		return nil, err
	}
	return body, nil
}

// deepMergeStyle merges two style documents; b wins. Null leaves in b delete
// the key from the result (reset to preset default).
func deepMergeStyle(a, b map[string]any) map[string]any {
	result := copyStyleMap(a)
	for key, value := range b {
		if value == nil {
			delete(result, key)
			continue
		}
		if styleTopObjectNodes[key] {
			if incoming, ok := value.(map[string]any); ok {
				existing, _ := result[key].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				result[key] = mergeStyleNode(key, existing, incoming)
				continue
			}
		}
		result[key] = value
	}
	return result
}

// mergeStyleNode merges one object node. tokens nests one more level
// (color/typography); layout and ia are scalar-leaf nodes.
func mergeStyleNode(name string, a, b map[string]any) map[string]any {
	result := copyStyleMap(a)
	for key, value := range b {
		if value == nil {
			delete(result, key)
			continue
		}
		if name == "tokens" && (key == "color" || key == "typography") {
			if incoming, ok := value.(map[string]any); ok {
				existing, _ := result[key].(map[string]any)
				if existing == nil {
					existing = map[string]any{}
				}
				result[key] = mergeStyleNode(name+"."+key, existing, incoming)
				continue
			}
		}
		result[key] = value
	}
	return result
}

// styleTopObjectNodes are the top-level style document keys merged deeply.
var styleTopObjectNodes = map[string]bool{"tokens": true, "layout": true, "ia": true}

func copyStyleMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

// ---------------------------------------------------------------------------
// WCAG contrast (design doc §7.3: custom primary is rejected below 4.5:1)
// ---------------------------------------------------------------------------

// CheckContrast verifies the effective primary/surface pair of the resolved
// config reaches the WCAG AA 4.5:1 text-contrast threshold.
func (c StyleConfig) CheckContrast() error {
	ratio := ContrastRatio(c.PrimaryColor, c.SurfaceColor)
	if ratio < 4.5 {
		return fmt.Errorf("%w: primary color contrast %.2f:1 is below the 4.5:1 WCAG AA threshold", ErrInvalidInput, ratio)
	}
	return nil
}

// ContrastRatio computes the WCAG relative-luminance contrast ratio of two
// #rrggbb colors (1..21). Malformed colors answer 0 so callers fail closed.
func ContrastRatio(first, second string) float64 {
	firstR, firstG, firstB, ok := parseHexColor(first)
	if !ok {
		return 0
	}
	secondR, secondG, secondB, ok := parseHexColor(second)
	if !ok {
		return 0
	}
	firstLuminance := relativeLuminance(firstR, firstG, firstB)
	secondLuminance := relativeLuminance(secondR, secondG, secondB)
	lighter := math.Max(firstLuminance, secondLuminance)
	darker := math.Min(firstLuminance, secondLuminance)
	return (lighter + 0.05) / (darker + 0.05)
}

func parseHexColor(value string) (uint8, uint8, uint8, bool) {
	value = strings.TrimSpace(value)
	if len(value) == 4 {
		expanded := "#" + strings.Repeat(string(value[1]), 2) + strings.Repeat(string(value[2]), 2) + strings.Repeat(string(value[3]), 2)
		value = expanded
	}
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, false
	}
	var channels [3]uint8
	for index := 0; index < 3; index++ {
		digit := digitValue(value[1+index*2])
		if digit < 0 {
			return 0, 0, 0, false
		}
		digit2 := digitValue(value[2+index*2])
		if digit2 < 0 {
			return 0, 0, 0, false
		}
		channels[index] = uint8(digit*16 + digit2)
	}
	return channels[0], channels[1], channels[2], true
}

func digitValue(char byte) int {
	switch {
	case char >= '0' && char <= '9':
		return int(char - '0')
	case char >= 'a' && char <= 'f':
		return int(char-'a') + 10
	case char >= 'A' && char <= 'F':
		return int(char-'A') + 10
	default:
		return -1
	}
}

func relativeLuminance(r, g, b uint8) float64 {
	return 0.2126*srgbChannel(r) + 0.7152*srgbChannel(g) + 0.0722*srgbChannel(b)
}

func srgbChannel(channel uint8) float64 {
	value := float64(channel) / 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}
