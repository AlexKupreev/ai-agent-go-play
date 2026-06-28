package provider

import "context"

// Provider is the vendor-neutral port. The kernel depends only on this; vendor
// SDKs live behind adapters that implement it.
type Provider interface {
	// Step runs exactly one model turn.
	Step(ctx context.Context, req StepRequest) (StepResponse, error)
}
