package vectorvalue

import (
	"math"
	"testing"
)

func TestLiteral(t *testing.T) {
	got, err := Literal([]float64{1, -0.25, 0})
	if err != nil {
		t.Fatal(err)
	}
	if got != "[1,-0.25,0]" {
		t.Fatalf("Literal() = %q", got)
	}
}

func TestLiteralRejectsInvalidValues(t *testing.T) {
	for _, values := range [][]float64{nil, {}, {math.NaN()}, {math.Inf(1)}} {
		if _, err := Literal(values); err == nil {
			t.Fatalf("Literal(%v) unexpectedly succeeded", values)
		}
	}
}
