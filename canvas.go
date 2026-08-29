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

func renderSparkline(data []float32, maxValue float32) string {
	if len(data) == 0 {
		return ""
	}
	blocks := []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	var b strings.Builder
	for _, v := range data {
		normalized := float32(0)
		if maxValue > 0 {
			normalized = v / maxValue
			if normalized < 0 {
				normalized = 0
			}
			if normalized > 1 {
				normalized = 1
			}
		}
		idx := int(normalized*float32(len(blocks)-1) + 0.5)
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		b.WriteRune(blocks[idx])
	}
	return b.String()
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

func bar(width int, fraction float32) string {
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(fraction * float32(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}
