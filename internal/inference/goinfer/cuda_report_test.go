//go:build cuda && cudasmoke

package goinfer

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
)

type cudaRequest struct {
	RenderedPrompt            string                    `json:"rendered_prompt"`
	Spans                     []inference.PromptSpan    `json:"spans"`
	TokenIDs                  []int                     `json:"token_ids"`
	Identity                  inference.RequestIdentity `json:"identity"`
	Policy                    inference.EffectivePolicy `json:"policy"`
	Finish                    inference.Finish          `json:"finish"`
	PromptTokens              int                       `json:"prompt_tokens"`
	OutputTokens              int                       `json:"output_tokens"`
	Text                      string                    `json:"text"`
	GenerationTokensPerSecond float64                   `json:"generation_tokens_per_second"`
	GenerationMS              float64                   `json:"generation_ms"`
	Observation               cudaObservation           `json:"observation"`
	Error                     string                    `json:"error,omitempty"`
}

func cudaRequestRecord(request inference.GenerationRequest) cudaRequest {
	return cudaRequest{RenderedPrompt: request.RenderedPrompt(), Spans: request.Spans(), TokenIDs: request.TokenIDs(), Identity: request.Identity(), Policy: request.Policy(), PromptTokens: len(request.TokenIDs())}
}
func cudaResultRecord(request inference.GenerationRequest, result inference.GenerationResult, err error, elapsed time.Duration) cudaRequest {
	record := cudaRequestRecord(request)
	record.Finish, record.PromptTokens, record.OutputTokens, record.Text, record.GenerationMS = result.Finish, result.PromptTokens, result.OutputTokens, result.Text, float64(elapsed)/float64(time.Millisecond)
	if record.GenerationMS > 0 {
		record.GenerationTokensPerSecond = float64(result.OutputTokens) / (record.GenerationMS / 1000)
	}
	if err != nil {
		record.Error = err.Error()
	}
	return record
}
func cudaWriteReport(t *testing.T, report interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(os.Getenv("YAKUORI_CUDA_REPORT"), append(data, '\n'), 0600); err != nil {
		t.Error(err)
	}
}
func cudaEnvironment(t *testing.T) (string, string) {
	t.Helper()
	model, backend, report := os.Getenv("YAKUORI_CUDA_MODEL"), os.Getenv("YAKUORI_CUDA_BACKEND"), os.Getenv("YAKUORI_CUDA_REPORT")
	if model == "" || (backend != "cpu" && backend != "cuda") || report == "" {
		t.Fatal("explicit YAKUORI_CUDA_MODEL, YAKUORI_CUDA_BACKEND cpu|cuda and YAKUORI_CUDA_REPORT required")
	}
	return model, backend
}
