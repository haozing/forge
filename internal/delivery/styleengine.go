package delivery

// styleengine.go — the StyleEngine of the HTML face (design doc §7.3):
// one validated site.StyleConfig becomes an inline CSS custom-properties
// block plus the layout class names the templates consume. Dark mode is
// derived from the light palette (no hand-written second theme); the color
// mode routes through the data-mode attribute and prefers-color-scheme.

import (
	"fmt"
	"math"
	"strings"

	"agentchunzhi/internal/site"
)

// densityScales maps the density token onto the global spacing unit.
var densityScales = map[string]int{"airy": 10, "normal": 8, "compact": 6}

// radiusScales maps the radius token onto pixels.
var radiusScales = map[string]int{"sharp": 2, "soft": 8, "round": 14}

// shadowStyles maps the shadow token onto box-shadow values.
var shadowStyles = map[string]string{
	"flat":   "none",
	"subtle": "0 1px 3px rgba(0,0,0,.12)",
	"lifted": "0 6px 18px rgba(0,0,0,.16)",
}

// fontStacks are the two built-in families (design doc §7.2: no external
// fonts are ever loaded).
var fontStacks = map[string]string{
	"serif": `Georgia, "Times New Roman", "Songti SC", "Noto Serif CJK SC", serif`,
	"sans":  `system-ui, -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif`,
}

// StyleCSS renders the CSS custom-properties block of one style config
// (light and derived dark palettes, spacing, radius, shadow, mode routing).
func StyleCSS(config site.StyleConfig) string {
	light := cssPalette{
		Primary: config.PrimaryColor,
		Surface: config.SurfaceColor,
		Text:    config.TextColor,
	}
	light.Bg = shiftColor(config.SurfaceColor, -0.02)
	light.Muted = config.TextColor
	light.Border = shiftColor(config.SurfaceColor, -0.10)
	light.OnPrimary = readableOn(config.PrimaryColor)
	dark := deriveDarkPalette(config)

	density := densityScales[config.Density]
	if density == 0 {
		density = 8
	}
	radius := radiusScales[config.Radius]
	shadow := shadowStyles[config.Shadow]
	if shadow == "" {
		shadow = shadowStyles["subtle"]
	}
	headingFont := fontStacks[config.HeadingFont]
	if headingFont == "" {
		headingFont = fontStacks["sans"]
	}

	var builder strings.Builder
	builder.WriteString(":root{")
	builder.WriteString(fmt.Sprintf("--c-primary:%s;--c-on-primary:%s;", light.Primary, light.OnPrimary))
	builder.WriteString(fmt.Sprintf("--c-surface:%s;--c-bg:%s;--c-text:%s;--c-muted:%s;--c-border:%s;",
		light.Surface, light.Bg, light.Text, light.Muted, light.Border))
	builder.WriteString(fmt.Sprintf("--font-heading:%s;--font-body:%s;", headingFont, fontStacks["sans"]))
	builder.WriteString(fmt.Sprintf("--fs-body:%dpx;--reading:%dpx;", config.BodySize, config.ReadingWidth))
	builder.WriteString(fmt.Sprintf("--space:%dpx;--radius:%dpx;--shadow:%s;", density, radius, shadow))
	builder.WriteString("}\n")

	darkVars := fmt.Sprintf("--c-primary:%s;--c-on-primary:%s;--c-surface:%s;--c-bg:%s;--c-text:%s;--c-muted:%s;--c-border:%s;",
		dark.Primary, dark.OnPrimary, dark.Surface, dark.Bg, dark.Text, dark.Muted, dark.Border)
	if config.ColorMode == "auto" {
		// No data-mode attribute is emitted for auto: the media query below
		// applies the derived dark palette to systems that prefer it.
		builder.WriteString("@media (prefers-color-scheme:dark){:root:not([data-mode=light]){" + darkVars + "}}\n")
	} else {
		builder.WriteString("@media (prefers-color-scheme:dark){:root[data-mode=dark]{" + darkVars + "}}\n")
		builder.WriteString(":root[data-mode=dark]{" + darkVars + "}\n")
	}
	return builder.String()
}

// ColorModeAttribute returns the html data-mode attribute value ("", "light"
// or "dark") for the layout root element.
func ColorModeAttribute(config site.StyleConfig) string {
	switch config.ColorMode {
	case "light", "dark":
		return config.ColorMode
	default:
		return ""
	}
}

// LayoutClasses renders the root body classes of one page kind.
func LayoutClasses(config site.StyleConfig, pageKind string) string {
	classes := []string{
		"home--" + config.HomeStyle,
		"list--" + config.ListStyle,
		"ratio--" + cssClassName(config.CardRatio),
		"sidebar--" + config.Sidebar,
		"page--" + pageKind,
	}
	return strings.Join(classes, " ")
}

// cssClassName converts a token like "16:9" into a class-safe name.
func cssClassName(value string) string {
	replaced := strings.ReplaceAll(value, ":", "-")
	return replaced
}

// cssPalette is one complete color set for a mode.
type cssPalette struct {
	Primary, OnPrimary, Surface, Bg, Text, Muted, Border string
}

// hsl is a color in cylindrical coordinates.
type hsl struct {
	H, S, L float64
}

// colorToHSL converts #rrggbb (sRGB) into HSL.
func colorToHSL(hex string) hsl {
	r, g, b, ok := parseRGB(hex)
	if !ok {
		return hsl{H: 210, S: 0.08, L: 0.98}
	}
	red, green, blue := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(red, math.Max(green, blue))
	min := math.Min(red, math.Min(green, blue))
	lightness := (max + min) / 2
	delta := max - min
	hue, saturation := 0.0, 0.0
	if delta > 1e-9 {
		switch max {
		case red:
			hue = math.Mod((green-blue)/delta, 6)
		case green:
			hue = (blue-red)/delta + 2
		default:
			hue = (red-green)/delta + 4
		}
		hue *= 60
		if hue < 0 {
			hue += 360
		}
		saturation = delta / (1 - math.Abs(2*lightness-1))
	}
	return hsl{H: hue, S: saturation, L: lightness}
}

func hslColor(color hsl) string {
	hue := math.Mod(math.Mod(color.H, 360)+360, 360)
	return fmt.Sprintf("hsl(%.0f %.d%% %.0f%%)", hue, int(math.Round(color.S * 100)), math.Round(color.L*100))
}

// parseRGB decodes #rrggbb and #rgb hex values.
func parseRGB(value string) (uint8, uint8, uint8, bool) {
	value = strings.TrimSpace(value)
	if len(value) == 4 {
		value = "#" + strings.Repeat(string(value[1]), 2) + strings.Repeat(string(value[2]), 2) + strings.Repeat(string(value[3]), 2)
	}
	if len(value) != 7 || value[0] != '#' {
		return 0, 0, 0, false
	}
	var channels [3]uint8
	for index := 0; index < 3; index++ {
		upper := hexDigit(value[1+index*2])
		lower := hexDigit(value[2+index*2])
		if upper < 0 || lower < 0 {
			return 0, 0, 0, false
		}
		channels[index] = uint8(upper*16 + lower)
	}
	return channels[0], channels[1], channels[2], true
}

func hexDigit(char byte) int {
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

// shiftColor darkens (factor < 0) or lightens one hex color slightly.
func shiftColor(hex string, factor float64) string {
	r, g, b, ok := parseRGB(hex)
	if !ok {
		return hex
	}
	shift := func(channel uint8) uint8 {
		value := float64(channel) / 255
		if factor < 0 {
			value = value * (1 + factor)
		} else {
			value = value + (1-value)*factor
		}
		scaled := math.Round(value * 255)
		if scaled < 0 {
			scaled = 0
		}
		if scaled > 255 {
			scaled = 255
		}
		return uint8(scaled)
	}
	return fmt.Sprintf("#%02X%02X%02X", shift(r), shift(g), shift(b))
}

// readableOn picks white or near-black text for one background color.
func readableOn(background string) string {
	contrastWhite := site.ContrastRatio(background, "#FFFFFF")
	contrastBlack := site.ContrastRatio(background, "#111111")
	if contrastWhite >= contrastBlack {
		return "#FFFFFF"
	}
	return "#14100E"
}

// deriveDarkPalette inverts the light palette's lightness while keeping the
// hues: the surface becomes a deep tint of itself, the text a bright one and
// the primary is re-lightened until it clears the WCAG AA 4.5:1 threshold
// against the dark surface (design doc §7.3: dark mode is derived, never
// hand-written).
func deriveDarkPalette(config site.StyleConfig) cssPalette {
	surfaceHSL := colorToHSL(config.SurfaceColor)
	primaryHSL := colorToHSL(config.PrimaryColor)
	dark := cssPalette{
		Surface: hslColor(hsl{H: surfaceHSL.H, S: math.Min(surfaceHSL.S*0.6+0.04, 0.18), L: 0.09}),
		Bg:      hslColor(hsl{H: surfaceHSL.H, S: math.Min(surfaceHSL.S*0.6+0.04, 0.18), L: 0.06}),
		Text:    hslColor(hsl{H: surfaceHSL.H, S: 0.05, L: 0.91}),
		Muted:   hslColor(hsl{H: surfaceHSL.H, S: 0.04, L: 0.66}),
		Border:  hslColor(hsl{H: surfaceHSL.H, S: 0.10, L: 0.20}),
	}
	for lightness := 0.55; lightness <= 0.92; lightness += 0.03 {
		candidate := hslColor(hsl{H: primaryHSL.H, S: math.Max(math.Min(primaryHSL.S, 0.72), 0.30), L: lightness})
		if site.ContrastRatio(candidate, dark.Surface) >= 4.5 {
			dark.Primary = candidate
			break
		}
	}
	if dark.Primary == "" {
		dark.Primary = hslColor(hsl{H: primaryHSL.H, S: 0.30, L: 0.92})
	}
	dark.OnPrimary = readableOn(dark.Primary)
	return dark
}
