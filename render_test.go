package main

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func spanWidth(spans []Span) int {
	n := 0
	for _, s := range spans {
		n += len([]rune(s.Text))
	}
	return n
}

func TestGradientBarWidthAndFill(t *testing.T) {
	for _, f := range []float32{-1, 0, 0.01, 0.5, 0.999, 1, 2} {
		if got := spanWidth(gradientBar(20, f, tcell.StyleDefault)); got != 20 {
			t.Fatalf("fraction %v: bar width = %d, want 20", f, got)
		}
	}
	if gradientBar(0, 0.5, tcell.StyleDefault) != nil {
		t.Fatal("zero-width bar should render nothing")
	}

	// Fill must be monotonic in the fraction, and 1/8-cell resolution means a
	// 1% fill on a 20-wide bar is still visible (the old whole-cell bar showed
	// nothing until 5%).
	countFilled := func(f float32) int {
		n := 0
		for _, s := range gradientBar(20, f, tcell.StyleDefault) {
			for _, r := range s.Text {
				if r != '░' {
					n++
				}
			}
		}
		return n
	}
	if countFilled(0.01) == 0 {
		t.Error("1% fill should light at least a partial cell")
	}
	prev := -1
	for f := float32(0); f <= 1.0; f += 0.05 {
		got := countFilled(f)
		if got < prev {
			t.Fatalf("fill not monotonic at %v: %d after %d", f, got, prev)
		}
		prev = got
	}
}

func TestGradientColorEndpoints(t *testing.T) {
	if got := gradientColor(0); got != tcell.NewHexColor(gradStops[0]) {
		t.Errorf("t=0 gave %v", got)
	}
	if got := gradientColor(1); got != tcell.NewHexColor(gradStops[len(gradStops)-1]) {
		t.Errorf("t=1 gave %v", got)
	}
	// Out-of-range must clamp, not wrap into a wrong colour.
	if gradientColor(-5) != gradientColor(0) || gradientColor(5) != gradientColor(1) {
		t.Error("gradientColor does not clamp out-of-range input")
	}
}

func TestGraphSpansFixedWidth(t *testing.T) {
	// Short, exact and over-long histories must all produce exactly `width`
	// cells, so a graph never shoves the rest of its line off screen.
	for _, n := range []int{0, 1, 9, 10, 50} {
		data := make([]float32, n)
		for i := range data {
			data[i] = float32(i)
		}
		if got := spanWidth(graphSpans(data, 100, 10, tcell.StyleDefault)); got != 10 {
			t.Errorf("len(data)=%d: width = %d, want 10", n, got)
		}
	}
	if graphSpans([]float32{1}, 0, 10, tcell.StyleDefault) != nil {
		t.Error("zero max should render nothing rather than divide by zero")
	}
}

func TestHistMaxNeverZero(t *testing.T) {
	if got := histMax(nil); got != 1 {
		t.Errorf("histMax(nil) = %v, want 1", got)
	}
	if got := histMax([]float32{0, 0}); got != 1 {
		t.Errorf("histMax(zeros) = %v, want 1", got)
	}
	if got := histMax([]float32{3, 9, 2}); got != 9 {
		t.Errorf("histMax = %v, want 9", got)
	}
}

func TestPushHistCaps(t *testing.T) {
	var h []float32
	for i := 0; i < maxHistory*2; i++ {
		h = pushHist(h, float32(i))
	}
	if len(h) != maxHistory {
		t.Fatalf("len = %d, want %d", len(h), maxHistory)
	}
	if h[len(h)-1] != float32(maxHistory*2-1) {
		t.Error("pushHist dropped the newest sample instead of the oldest")
	}
}

func TestClampScrollKeepsSelectionVisible(t *testing.T) {
	visible := func(scroll, sel, height int) bool {
		return sel >= scroll && sel < scroll+height
	}
	// The bug this fixes: selecting a row below the fold left the viewport put,
	// so the selection (and the [x] kill target) was off screen.
	for _, tc := range []struct{ scroll, sel, total, height int }{
		{0, 0, 100, 10},
		{0, 42, 100, 10},  // selection below the fold: must scroll down
		{50, 3, 100, 10},  // selection above: must scroll up
		{0, 99, 100, 10},  // last row
		{90, 95, 100, 10}, // already visible: stay put
		{999, 5, 100, 10}, // absurd scroll gets clamped back
		{-5, 5, 100, 10},  // negative scroll gets clamped
		{0, 2, 3, 10},     // fewer rows than the viewport
	} {
		got := clampScroll(tc.scroll, tc.sel, tc.total, tc.height)
		if got < 0 || got > maxInt(tc.total-tc.height, 0) {
			t.Errorf("%+v: scroll %d out of range", tc, got)
		}
		if !visible(got, tc.sel, tc.height) {
			t.Errorf("%+v: selection %d not visible at scroll %d", tc, tc.sel, got)
		}
	}
	if got := clampScroll(7, -1, 100, 10); got != 7 {
		t.Errorf("no selection should leave scroll alone, got %d", got)
	}
}
