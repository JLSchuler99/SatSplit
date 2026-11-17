package vision

import (
	"context"
	"errors"
)

// WithFallback returns a Service that reads with primary and, when the result
// cannot be trusted, reads again with secondary. A result is trusted when the
// items add up to what the receipt printed, or when the receipt printed
// nothing to check against. The better of the two results is returned and
// Attempts lists the rounds of both.
func WithFallback(primary, secondary Service) Service {
	if secondary == nil {
		return primary
	}
	return &fallback{primary: primary, secondary: secondary}
}

type fallback struct {
	primary   Service
	secondary Service
}

func (f *fallback) ExtractItems(ctx context.Context, imagePath string, hint Hint) (Extraction, error) {
	first, firstErr := f.primary.ExtractItems(ctx, imagePath, hint)
	if firstErr == nil && (!first.Verifiable || first.Balanced) {
		return first, nil
	}

	second, secondErr := f.secondary.ExtractItems(ctx, imagePath, hint)
	attempts := append(append([]Attempt{}, first.Attempts...), second.Attempts...)

	switch {
	case firstErr != nil && secondErr != nil:
		out := Extraction{Model: second.Model, Attempts: attempts}
		return out, errors.Join(firstErr, secondErr)
	case firstErr != nil:
		second.Attempts = attempts
		return second, nil
	case secondErr != nil:
		first.Attempts = attempts
		return first, nil
	}
	// Both parsed. Prefer a balanced result, then the smaller difference.
	if second.Balanced || !second.Verifiable || abs64(second.DiffCents) <= abs64(first.DiffCents) {
		second.Attempts = attempts
		return second, nil
	}
	first.Attempts = attempts
	return first, nil
}
