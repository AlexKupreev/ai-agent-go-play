package openai

import (
	"errors"
	"testing"

	oai "github.com/openai/openai-go"
)

func TestIsUnknownModel(t *testing.T) {
	if !isUnknownModel(&oai.Error{Code: "model_not_found", Message: "The model does not exist", Param: "model", StatusCode: 404}) {
		t.Fatal("model_not_found API error was not classified")
	}
	if !isUnknownModel(errors.New("upstream: unknown model llama-missing")) {
		t.Fatal("OpenAI-compatible unknown-model error was not classified")
	}
	if isUnknownModel(errors.New("rate limit exceeded")) {
		t.Fatal("unrelated provider error was classified as an unknown model")
	}
}
