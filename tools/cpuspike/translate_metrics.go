package main

import (
	"context"
	"errors"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type translationRequest struct {
	Finish            inference.Finish `json:"finish"`
	PromptTokens      int              `json:"prompt_tokens"`
	OutputTokens      int              `json:"output_tokens"`
	Text              string           `json:"text"`
	Milliseconds      float64          `json:"generation_ms"`
	RequestTimeoutMS  int64            `json:"request_timeout_ms"`
	DeadlineOverrunMS float64          `json:"deadline_overrun_ms"`
	Error             string           `json:"error,omitempty"`
}
type translationRun struct {
	Name                  string               `json:"name"`
	TotalMS               float64              `json:"total_ms"`
	GenerationMS          float64              `json:"generation_ms"`
	PlanningMS            float64              `json:"measurement_planning_ms"`
	RevalidationMS        float64              `json:"revalidation_ms"`
	Segments              int                  `json:"segments"`
	UnitBytes             []int                `json:"unit_bytes"`
	AcceptedUnits         int                  `json:"accepted_units"`
	ValidationFailures    int                  `json:"validation_failures"`
	ValidationDenominator int                  `json:"validation_denominator"`
	Output                []string             `json:"output"`
	Requests              []translationRequest `json:"requests"`
	Error                 string               `json:"error,omitempty"`
}
type translationReport struct {
	Go            string           `json:"go"`
	GOMAXPROCS    int              `json:"gomaxprocs"`
	TM            string           `json:"tm"`
	Context       int              `json:"context_tokens"`
	MaxOutput     int              `json:"max_output_tokens"`
	RequestMS     int64            `json:"request_timeout_ms"`
	GenerationMS  int64            `json:"generation_timeout_ms"`
	ArtifactBytes int              `json:"artifact_bytes"`
	TextBytes     int              `json:"total_text_bytes"`
	Units         int              `json:"units"`
	LoadMS        float64          `json:"load_ms"`
	TotalMS       float64          `json:"process_total_ms"`
	PeakRSSKiB    int64            `json:"peak_rss_kib"`
	Runs          []translationRun `json:"runs"`
	Error         string           `json:"error,omitempty"`
}
type measuredEngine struct {
	inference.Engine
	cap int
	run *translationRun
}

func (e *measuredEngine) Info(ctx context.Context) (inference.ModelInfo, error) {
	info, err := e.Engine.Info(ctx)
	info.ContextTokens = min(info.ContextTokens, e.cap)
	return info, err
}
func (e *measuredEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	started := time.Now()
	result, err := e.Engine.Generate(ctx, request)
	finished := time.Now()
	elapsed := float64(finished.Sub(started)) / float64(time.Millisecond)
	record := translationRequest{Finish: result.Finish, PromptTokens: result.PromptTokens, OutputTokens: result.OutputTokens, Text: result.Text, Milliseconds: elapsed, RequestTimeoutMS: result.Policy.RequestTimeout.Milliseconds()}
	if result.Finish == inference.Timeout && !result.Deadline.IsZero() {
		// The backend records the earlier of parent and request deadlines.
		record.DeadlineOverrunMS = max(0, float64(finished.Sub(result.Deadline))/float64(time.Millisecond))
	}
	if err != nil {
		record.Error = err.Error()
	}
	e.run.Requests = append(e.run.Requests, record)
	e.run.GenerationMS += elapsed
	return result, err
}

// Core returns no accepted subset on any later unit failure. Count preceding
// successful checks from the bound failed unit rather than from that empty slice.
func mechanicalCounts(session unit.Session, accepted int, err error) (failures, denominator int) {
	if err == nil {
		return 0, accepted
	}
	var phase *localize.Error
	if !errors.As(err, &phase) {
		return 0, 0
	}
	if phase.Phase == "prepare_export" {
		return 0, len(session.Units())
	}
	for i, u := range session.Units() {
		if u.ID() != phase.UnitID {
			continue
		}
		if phase.Phase == "validate" || phase.Phase == "restore" {
			return 1, i + 1
		}
		return 0, i
	}
	return 0, 0
}
