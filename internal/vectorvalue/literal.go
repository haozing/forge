package vectorvalue

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

var ErrInvalidVector = errors.New("invalid vector")

// Literal encodes finite float64 values in pgvector's text input format.
func Literal(values []float64) (string, error) {
	if len(values) == 0 {
		return "", ErrInvalidVector
	}
	var b strings.Builder
	b.Grow(len(values) * 12)
	b.WriteByte('[')
	for i, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return "", ErrInvalidVector
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	}
	b.WriteByte(']')
	return b.String(), nil
}
