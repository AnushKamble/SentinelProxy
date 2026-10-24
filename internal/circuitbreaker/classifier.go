package circuitbreaker

import (
	"context"
	"errors"
	"net"
)

// Classifier evaluates whether an upstream exchange result counts as an eligible failure.
type Classifier struct {
	countedStatuses map[int]bool
}

// NewClassifier creates a Classifier from a list of counted HTTP status codes.
func NewClassifier(countedCodes []int) *Classifier {
	m := make(map[int]bool, len(countedCodes))
	for _, code := range countedCodes {
		m[code] = true
	}
	return &Classifier{
		countedStatuses: m,
	}
}

// IsFailure determines if an HTTP status code or error represents an upstream failure.
// Client cancellations and client 4xx statuses are not counted as failures.
func (c *Classifier) IsFailure(statusCode int, err error) bool {
	// If context was canceled by client, it is NOT an upstream failure
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Upstream timeouts and deadline exceeded count as failures
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	// Transport or connection errors count as failures
	if err != nil {
		return true
	}

	// Check HTTP status code
	return c.countedStatuses[statusCode]
}
