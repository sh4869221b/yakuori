//go:build cuda && cudasmoke

package goinfer

import (
	"context"
	"errors"
	"time"

	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/unit"
)

type cudaMatrixRun struct {
	Name                  string        `json:"name"`
	Status                string        `json:"status"`
	TotalMS               float64       `json:"total_ms"`
	GenerationMS          float64       `json:"generation_ms"`
	Segments              int           `json:"segments"`
	AcceptedUnits         int           `json:"accepted_units"`
	ValidationFailures    int           `json:"validation_failures"`
	ValidationDenominator int           `json:"validation_denominator"`
	Output                []string      `json:"output"`
	PlannedRequests       []cudaRequest `json:"planned_requests"`
	Requests              []cudaRequest `json:"requests"`
	Error                 string        `json:"error,omitempty"`
}
type cudaMatrixReport struct {
	Go                string          `json:"go"`
	GOMAXPROCS        int             `json:"gomaxprocs"`
	Setup             cudaSetup       `json:"setup"`
	Fixture           string          `json:"fixture"`
	Pair              string          `json:"pair"`
	Status            string          `json:"status"`
	TM                string          `json:"tm"`
	MetricDefinitions string          `json:"metric_definitions"`
	Units             int             `json:"units"`
	ArtifactBytes     int             `json:"artifact_bytes"`
	TextBytes         int             `json:"total_text_bytes"`
	ProcessTotalMS    float64         `json:"process_total_ms"`
	PeakRSSKiB        int64           `json:"peak_rss_kib"`
	Runs              []cudaMatrixRun `json:"runs"`
	Error             string          `json:"error,omitempty"`
	RequestEquality   string          `json:"paired_request_equality"`
}
type cudaMeasuredEngine struct {
	*Engine
	run *cudaMatrixRun
}

func (e *cudaMeasuredEngine) Generate(ctx context.Context, request inference.GenerationRequest) (inference.GenerationResult, error) {
	started := time.Now()
	result, err := cudaGenerate(ctx, e.Engine, request)
	record := cudaResultRecord(request, result, err, time.Since(started))
	record.Observation = e.model.(*cudaObserver).observe()
	e.run.Requests = append(e.run.Requests, record)
	e.run.GenerationMS += record.GenerationMS
	return result, err
}
func cudaMechanicalCounts(session unit.Session, accepted int, err error) (int, int) {
	if err == nil {
		return 0, accepted
	}
	var phase *localize.Error
	if !errors.As(err, &phase) {
		return 0, 0
	}
	if phase.Phase == "input" || phase.Phase == "protect" || phase.Phase == "plan" {
		return 0, 0
	}
	if phase.Phase == "prepare_export" {
		return 0, len(session.Units())
	}
	for i, u := range session.Units() {
		if u.ID() == phase.UnitID {
			if phase.Phase == "validate" || phase.Phase == "restore" {
				return 1, i + 1
			}
			return 0, i
		}
	}
	return 0, 0
}
