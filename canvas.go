package main

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

type Rect struct{ X, Y, W, H int }

type Span struct {
	Text  string
	Style tcell.Style
}

func plain(s string) []Span { return []Span{{Text: s}} }

type ckind int

const (
	cLength ckind = iota
	cPercent
	cMin
)

type Constraint struct {
	Kind  ckind
	Value int
}

func Length(v int) Constraint  { return Constraint{cLength, v} }
func Percent(v int) Constraint { return Constraint{cPercent, v} }
func Min(v int) Constraint     { return Constraint{cMin, v} }

func splitSizes(total int, cs []Constraint) []int {
	if total < 0 {
		total = 0
	}
	sizes := make([]int, len(cs))
	fixedSum := 0
	var minIdx []int
	for i, c := range cs {
		switch c.Kind {
		case cLength:
			sizes[i] = c.Value
			fixedSum += sizes[i]
		case cPercent:
			sizes[i] = total * c.Value / 100
			fixedSum += sizes[i]
		case cMin:
			minIdx = append(minIdx, i)
		}
	}

	if fixedSum > total {

		scale := 0.0
		if fixedSum > 0 {
			scale = float64(total) / float64(fixedSum)
		}
		used := 0
		lastFixed := -1
		for i, c := range cs {
			if c.Kind == cMin {
				sizes[i] = 0
				continue
			}
			sizes[i] = int(float64(sizes[i]) * scale)
			used += sizes[i]
			lastFixed = i
		}

		if lastFixed >= 0 && total-used > 0 {
			sizes[lastFixed] += total - used
		}
		return sizes
	}

	remaining := total - fixedSum
	if len(minIdx) > 0 {
		each := remaining / len(minIdx)
		extra := remaining % len(minIdx)
		for n, i := range minIdx {
			v := each
			if n < extra {
				v++
			}
			sizes[i] = v
		}
	}
	return sizes
}

func splitVertical(r Rect, cs []Constraint) []Rect {
	sizes := splitSizes(r.H, cs)
	out := make([]Rect, len(cs))
	y := r.Y
	for i, h := range sizes {
		out[i] = Rect{X: r.X, Y: y, W: r.W, H: h}
		y += h
	}
	return out
}

func splitHorizontal(r Rect, cs []Constraint) []Rect {
	sizes := splitSizes(r.W, cs)
	out := make([]Rect, len(cs))
	x := r.X
	for i, w := range sizes {
		out[i] = Rect{X: x, Y: r.Y, W: w, H: r.H}
		x += w
	}
	return out
}

const (
	runeRoundedUL = '╭'
	runeRoundedUR = '╮'
	runeRoundedLL = '╰'
	runeRoundedLR = '╯'
)

func drawBox(s tcell.Screen, r Rect, title string, style tcell.Style) Rect {
	if r.W < 2 || r.H < 2 {
		return Rect{r.X, r.Y, 0, 0}
	}
	for x := r.X; x < r.X+r.W; x++ {
		s.SetContent(x, r.Y, tcell.RuneHLine, nil, style)
		s.SetContent(x, r.Y+r.H-1, tcell.RuneHLine, nil, style)
	}
	for y := r.Y; y < r.Y+r.H; y++ {
		s.SetContent(r.X, y, tcell.RuneVLine, nil, style)
		s.SetContent(r.X+r.W-1, y, tcell.RuneVLine, nil, style)
	}
	s.SetContent(r.X, r.Y, runeRoundedUL, nil, style)
	s.SetContent(r.X+r.W-1, r.Y, runeRoundedUR, nil, style)
	s.SetContent(r.X, r.Y+r.H-1, runeRoundedLL, nil, style)
	s.SetContent(r.X+r.W-1, r.Y+r.H-1, runeRoundedLR, nil, style)

	if title != "" {
		titleStyle := style.Bold(true)
		drawText(s, r.X+2, r.Y, []Span{{Text: " " + title + " ", Style: titleStyle}}, r.W-4)
	}
	return Rect{r.X + 1, r.Y + 1, r.W - 2, r.H - 2}
}

func drawText(s tcell.Screen, x, y int, spans []Span, maxWidth int) {
	col := x
	remaining := maxWidth
	for _, sp := range spans {
		for _, r := range sp.Text {
			if remaining <= 0 {
				return
			}
			s.SetContent(col, y, r, nil, sp.Style)
			col++
			remaining--
		}
	}
}

func drawParagraph(s tcell.Screen, r Rect, title string, lines [][]Span, borderStyle tcell.Style) {
	inner := drawBox(s, r, title, borderStyle)
	for i, line := range lines {
		if i >= inner.H {
			break
		}
		drawText(s, inner.X, inner.Y+i, line, inner.W)
	}
}

func drawTable(s tcell.Screen, r Rect, title string, header []Span, rows [][]Span, colWidths []int, borderStyle, headerStyle tcell.Style) {
	inner := drawBox(s, r, title, borderStyle)
	if inner.H == 0 {
		return
	}

	drawRow := func(y int, cells []Span, widths []int) {
		x := inner.X
		for i, cell := range cells {
			w := 10
			if i < len(widths) {
				w = widths[i]
			}
			runes := []rune(cell.Text)
			if len(runes) > w {
				runes = runes[:w]
			}

			text := string(runes) + strings.Repeat(" ", w-len(runes))
			drawText(s, x, y, []Span{{Text: text, Style: cell.Style}}, w)
			x += w + 1
		}
	}

	y := inner.Y
	if header != nil {
		drawRow(y, header, colWidths)
		y++
	}
	for _, row := range rows {
		if y >= inner.Y+inner.H {
			break
		}
		drawRow(y, row, colWidths)
		y++
	}
}


func computeBarWidth(innerW, reserved, min, max int) int {
	w := innerW - reserved
	if w < min {
		w = min
	}
	if w > max {
		w = max
	}
	return w
}

// segmentedBar renders width cells split across fracs (each 0..1, clamped and
// capped so their sum never exceeds 1) as filled blocks in styles, with any
// remainder left as the empty track. Used for bars that break "used" down
// into sub-categories (e.g. RAM used vs. reclaimable cache).
func segmentedBar(width int, fracs []float32, styles []tcell.Style) []Span {
	if width < 0 {
		width = 0
	}
	spans := make([]Span, 0, len(fracs)+1)
	filledTotal := 0
	for i, f := range fracs {
		if f < 0 {
			f = 0
		}
		if f > 1 {
			f = 1
		}
		n := int(f * float32(width))
		if filledTotal+n > width {
			n = width - filledTotal
		}
		filledTotal += n
		if n > 0 {
			spans = append(spans, Span{Text: strings.Repeat("█", n), Style: styles[i]})
		}
	}
	if filledTotal < width {
		spans = append(spans, Span{Text: strings.Repeat("░", width-filledTotal)})
	}
	return spans
}

// Eighth-width block runes: a bar of N cells resolves to 8N steps instead of
// N, so a short bar stops jumping in 10% jerks.
var eighthBlocks = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

// gradStops is the meter gradient, low→high. Every cell of a bar is coloured
// by its own position along the track (btop's look), so a full bar shows the
// whole ramp rather than one flat severity colour.
var gradStops = []int32{0x33c9a4, 0xf2c94c, 0xff6b6b}

// useTruecolor gates 24-bit gradients. On an 8-colour TERM tcell quantises
// every hex stop to roughly the same hue, so a gradient renders as one flat
// block — the discrete fallback below stays readable there. Set once at
// startup from the config.
var useTruecolor = true

// fallbackStops is the 8-colour approximation of gradStops, used when the
// terminal can't do 24-bit.
var fallbackStops = []tcell.Color{tcell.ColorGreen, tcell.ColorYellow, tcell.ColorRed}

func gradientColor(t float64) tcell.Color {
	if !useTruecolor {
		switch {
		case t < 0.6:
			return fallbackStops[0]
		case t < 0.85:
			return fallbackStops[1]
		default:
			return fallbackStops[2]
		}
	}
	switch {
	case t <= 0:
		return tcell.NewHexColor(gradStops[0])
	case t >= 1:
		return tcell.NewHexColor(gradStops[len(gradStops)-1])
	}
	seg := t * float64(len(gradStops)-1)
	i := int(seg)
	f := seg - float64(i)
	a, b := gradStops[i], gradStops[i+1]
	lerp := func(shift uint) int32 {
		av := (a >> shift) & 0xff
		bv := (b >> shift) & 0xff
		return av + int32(f*float64(bv-av))
	}
	return tcell.NewHexColor(lerp(16)<<16 | lerp(8)<<8 | lerp(0))
}

// gradientBar draws a per-cell gradient meter with 1/8-cell resolution on top
// of base (so callers with a panel background keep it). Replaces bar() at
// every call site that wants the modern look.
func gradientBar(width int, fraction float32, base tcell.Style) []Span {
	if width <= 0 {
		return nil
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	denom := float64(width - 1)
	if denom <= 0 {
		denom = 1
	}
	exact := float64(fraction) * float64(width)
	full := int(exact)
	if full > width {
		full = width
	}

	spans := make([]Span, 0, width+1)
	for i := 0; i < full; i++ {
		spans = append(spans, Span{"█", base.Foreground(gradientColor(float64(i) / denom))})
	}
	if full < width {
		if eighth := int((exact - float64(full)) * 8); eighth > 0 {
			spans = append(spans, Span{string(eighthBlocks[eighth]), base.Foreground(gradientColor(float64(full) / denom))})
			full++
		}
	}
	if full < width {
		spans = append(spans, Span{strings.Repeat("░", width-full), base.Foreground(colorTrack)})
	}
	return spans
}

// graphSpans is renderSparkline with each column coloured by its own value,
// so a history strip reads as a heat trace instead of a grey squiggle. Only
// the trailing `width` samples are drawn.
func graphSpans(data []float32, maxValue float32, width int, base tcell.Style) []Span {
	if width <= 0 || maxValue <= 0 {
		return nil
	}
	if len(data) > width {
		data = data[len(data)-width:]
	}
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	spans := make([]Span, 0, width)
	// Left-pad with empty track so the trace grows in from the right and the
	// strip keeps a fixed width from the first frame.
	if pad := width - len(data); pad > 0 {
		spans = append(spans, Span{strings.Repeat("·", pad), base.Foreground(colorTrack)})
	}
	for _, v := range data {
		n := float64(v / maxValue)
		if n < 0 {
			n = 0
		}
		if n > 1 {
			n = 1
		}
		idx := int(n*float64(len(blocks)-1) + 0.5)
		spans = append(spans, Span{string(blocks[idx]), base.Foreground(gradientColor(n))})
	}
	return spans
}

