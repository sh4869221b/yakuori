package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/openai"
	"github.com/sh4869221b/yakuori/internal/prompt"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "smoke failed:", err)
		os.Exit(1)
	}
}

func run() error {
	e, err := openai.NewEngine("gpt-5.4-mini-2026-03-17", os.Getenv("OPENAI_API_KEY"))
	if err != nil {
		return fmt.Errorf("engine initialization")
	}
	defer e.Close()
	policy, err := inference.NewGenerationPolicy(inference.PolicySchemaV1, 128, 30*time.Second)
	if err != nil {
		return fmt.Errorf("policy initialization")
	}
	ctx := context.Background()
	r, err := e.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: "Hello, traveler."}, policy)
	if err != nil {
		return fmt.Errorf("request preparation")
	}
	fmt.Println("count attempts: 1")
	n, err := e.CountTokens(ctx, r)
	if err != nil {
		return fmt.Errorf("input token count")
	}
	fmt.Printf("counted input tokens: %d\n", n)
	if n > 512 {
		return fmt.Errorf("input token limit exceeded")
	}
	fmt.Println("generation attempts: 1")
	result, err := e.Generate(ctx, r)
	if err != nil {
		return fmt.Errorf("generation")
	}
	fmt.Printf("usage: input=%d output=%d\n", result.PromptTokens, result.OutputTokens)
	if result.Finish != inference.Stop || result.PromptTokens != n || result.OutputTokens > 128 || result.Text == "" {
		return fmt.Errorf("generation validation")
	}
	fmt.Println("smoke passed")
	return nil
}
