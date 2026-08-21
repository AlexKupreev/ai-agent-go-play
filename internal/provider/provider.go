package provider

import (
	"context"
	"fmt"
)

// UnknownModelError classifies a provider rejection of the selected model and carries the
// recovery path all the way through run events to CLI and chat frontends.
type UnknownModelError struct {
	Model string
	Err   error
}

func (e *UnknownModelError) Error() string {
	return fmt.Sprintf("model %q is unavailable; use /model - in chat (or clear the model override) to return to the engine default", e.Model)
}

func (e *UnknownModelError) Unwrap() error { return e.Err }

// Provider is the vendor-neutral port. The kernel depends only on this; vendor
// SDKs live behind adapters that implement it.
type Provider interface {
	// Step runs exactly one model turn.
	Step(ctx context.Context, req StepRequest) (StepResponse, error)
}
