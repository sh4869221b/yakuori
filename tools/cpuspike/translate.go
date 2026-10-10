package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/inference/goinfer"
	"github.com/sh4869221b/yakuori/internal/localize"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/segment"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

// This is a bounded research fixture reader, not a game adapter or product limit.
func translateFixture(path string) (unit.Session, error) {
	return translateFixtureWithLimits(path, nil)
}

func translateFixtureWithLimits(path string, limits *config.Limits) (unit.Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return unit.Session{}, err
	}
	defer f.Close()
	limit := 2 << 20
	if limits != nil {
		limit = limits.ArtifactBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return unit.Session{}, err
	}
	if len(data) > limit {
		if limits != nil {
			return unit.Session{}, limits.CheckArtifactBytes(len(data))
		}
		return unit.Session{}, errors.New("research fixture exceeds 2 MiB")
	}
	var texts []string
	if err := json.Unmarshal(data, &texts); err != nil {
		return unit.Session{}, err
	}
	if len(texts) == 0 || len(texts) > 4096 {
		return unit.Session{}, errors.New("research fixture requires 1..4096 units")
	}
	if limits != nil {
		if err := limits.CheckUnits(len(texts)); err != nil {
			return unit.Session{}, err
		}
	}
	totalText := 0
	units := make([]unit.TranslationUnit, 0, len(texts))
	for i, text := range texts {
		if text == "" {
			return unit.Session{}, errors.New("empty research unit")
		}
		totalText += len(text)
		if limits != nil {
			if err := limits.CheckUnitTextBytes(len(text)); err != nil {
				return unit.Session{}, err
			}
			if err := limits.CheckTotalTextBytes(totalText); err != nil {
				return unit.Session{}, err
			}
		}
		id, err := unit.NewUnitID("cpu-research", "v1", fmt.Sprint(i))
		if err != nil {
			return unit.Session{}, err
		}
		u, err := unit.NewTranslationUnit(id, []byte(text), "en", "ja", nil)
		if err != nil {
			return unit.Session{}, err
		}
		units = append(units, u)
	}
	return unit.NewSession(data, units)
}

func runTranslation(ctx context.Context, e *measuredEngine, engine *goinfer.Engine, session unit.Session, policy inference.GenerationPolicy, name string, operational bool) (r translationRun, err error) {
	r = translationRun{Name: name, Output: []string{}, Requests: []translationRequest{}}
	e.run = &r
	started := time.Now()
	defer func() { r.TotalMS = float64(time.Since(started)) / float64(time.Millisecond) }()
	build := func(ctx context.Context, _ unit.UnitID, text string) (inference.GenerationRequest, error) {
		return engine.NewRequest(ctx, prompt.Input{SourceLanguage: "en", TargetLanguage: "ja", Text: text}, policy)
	}
	info, err := e.Info(ctx)
	if err != nil {
		return r, err
	}
	limits := translationLimits(ctx, info, policy)
	if operational {
		limits = config.DefaultLimits()
	}
	// Count every planned segment, including pieces requiring no generation.
	planning := time.Now()
	for _, u := range session.Units() {
		r.UnitBytes = append(r.UnitBytes, len(u.Source()))
		prepared, err := protect.Prepare(session, u.ID())
		if err != nil {
			return r, err
		}
		plan, err := segment.BuildWithLimits(ctx, u.ID(), prepared, info, func(ctx context.Context, text string) (inference.GenerationRequest, error) {
			return build(ctx, u.ID(), text)
		}, e.CountTokens, limits)
		if err != nil {
			r.Error = err.Error()
			return r, err
		}
		r.Segments += len(plan.Segments())
		if err := limits.CheckSegments(r.Segments); err != nil {
			return r, err
		}
	}
	r.PlanningMS = float64(time.Since(planning)) / float64(time.Millisecond)
	profile, err := validate.NewProfileWithSegmentation(info.ModelSHA256, segment.ProfileInput())
	if err != nil {
		return r, err
	}
	core, err := localize.NewSegmentedCoreWithLimits(e, build, limits)
	if err != nil {
		return r, err
	}
	accepted, err := core.Generate(ctx, session, profile)
	// Core includes planning, protection, validation and export preparation.
	// Measure a separate revalidation without attributing Core overhead to validation.
	r.AcceptedUnits = len(accepted)
	r.ValidationFailures, r.ValidationDenominator = mechanicalCounts(session, len(accepted), err)
	validationStarted := time.Now()
	for _, a := range accepted {
		if _, validationErr := validate.Validate(session, a.UnitID(), profile, a.Text()); validationErr != nil {
			return r, validationErr
		}
		r.Output = append(r.Output, a.Text())
	}
	r.RevalidationMS = float64(time.Since(validationStarted)) / float64(time.Millisecond)
	r.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
	if err != nil {
		r.Error = fmt.Sprintf("%v: %v", err, errors.Unwrap(err))
	}
	return r, err
}

// Research admission matches the existing finite fixture reader and explicit trial flags.
func translationLimits(ctx context.Context, info inference.ModelInfo, policy inference.GenerationPolicy) config.Limits {
	limits := config.DefaultLimits()
	limits.ArtifactBytes, limits.UnitTextBytes, limits.TotalTextBytes = 2<<20, 2<<20, 2<<20
	limits.Units, limits.Segments = 4096, 2<<20
	limits.ContextTokens, limits.MaxOutputTokens = info.ContextTokens, policy.MaxOutputTokens()
	limits.RequestTimeout = policy.RequestTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		limits.GenerationTimeout = max(time.Until(deadline), time.Nanosecond)
	}
	return limits
}

func translateMain(args []string) int {
	flags := flag.NewFlagSet("translate", flag.ContinueOnError)
	model := flags.String("model", "", "local model")
	fixture := flags.String("fixture", "", "JSON array of English units")
	cap := flags.Int("context-tokens", 0, "experimental context cap")
	output := flags.Int("max-output-tokens", 0, "output reservation")
	request := flags.Duration("request-timeout", 0, "per-request trial budget")
	generation := flags.Duration("generation-timeout", 0, "per-run trial budget")
	operational := flags.Bool("operational-limits", false, "enforce adopted DefaultLimits; explicit conflicting trial flags are rejected")
	deadlineProbe := flags.Bool("deadline-probe", false, "100ms cancellation, drain, then healthy reuse")
	r := translationReport{Go: runtime.Version(), GOMAXPROCS: runtime.GOMAXPROCS(0), TM: "none: Core.Generate only; no TM lookup or commit"}
	started := time.Now()
	err := flags.Parse(args)
	if err == nil && *operational {
		l := config.DefaultLimits()
		flags.Visit(func(f *flag.Flag) {
			conflict := (f.Name == "context-tokens" && *cap != l.ContextTokens) || (f.Name == "max-output-tokens" && *output != l.MaxOutputTokens) || (f.Name == "request-timeout" && *request != l.RequestTimeout) || (f.Name == "generation-timeout" && *generation != l.GenerationTimeout)
			if conflict {
				err = fmt.Errorf("--%s conflicts with adopted operational limits", f.Name)
			}
		})
		*cap, *output, *request, *generation = l.ContextTokens, l.MaxOutputTokens, l.RequestTimeout, l.GenerationTimeout
	}
	if err == nil && (*model == "" || *fixture == "" || *cap <= *output || *output <= 0 || *request <= 0 || *generation <= 0 || flags.NArg() != 0) {
		err = errors.New("required positive translate flags; context must exceed output; no positional arguments")
	}
	r.OperationalLimits = *operational
	r.Context = *cap
	r.MaxOutput = *output
	r.RequestMS = request.Milliseconds()
	r.GenerationMS = generation.Milliseconds()
	if err == nil {
		for _, entry := range os.Environ() {
			if strings.HasPrefix(entry, "GOINFER_") {
				err = fmt.Errorf("remove ambient GOINFER tuning: %s", strings.SplitN(entry, "=", 2)[0])
				break
			}
		}
	}
	if err == nil {
		err = translateProbe(&r, *model, *fixture, *cap, *output, *request, *generation, *deadlineProbe, *operational)
	}
	r.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
	var usage syscall.Rusage
	if usageErr := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); usageErr != nil {
		err = errors.Join(err, usageErr)
	}
	r.PeakRSSKiB = usage.Maxrss
	if err != nil {
		r.Error = err.Error()
	}
	if encodeErr := json.NewEncoder(os.Stdout).Encode(r); encodeErr != nil {
		fmt.Fprintln(os.Stderr, encodeErr)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func translateProbe(r *translationReport, path, fixture string, cap, output int, request, generation time.Duration, deadline, operational bool) (err error) {
	var limits *config.Limits
	if operational {
		l := config.DefaultLimits()
		limits = &l
	}
	session, err := translateFixtureWithLimits(fixture, limits)
	if err != nil {
		return err
	}
	r.ArtifactBytes = len(session.Artifact())
	r.Units = len(session.Units())
	for _, u := range session.Units() {
		r.TextBytes += len(u.Source())
	}
	load := time.Now()
	engine, err := goinfer.Open(context.Background(), path, "int4")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close()) }()
	r.LoadMS = float64(time.Since(load)) / float64(time.Millisecond)
	measured := &measuredEngine{Engine: engine, cap: cap}
	for _, name := range []string{"cold", "warm"} {
		budget := request
		runBudget := generation
		isDeadline := deadline && name == "cold"
		if isDeadline && operational {
			runBudget = 100 * time.Millisecond
			name = "caller_deadline_100ms"
		} else if isDeadline {
			budget = 100 * time.Millisecond
			name = "deadline_100ms"
		}
		policy, policyErr := inference.NewGenerationPolicy(inference.PolicySchemaV1, output, budget)
		if policyErr != nil {
			return policyErr
		}
		ctx, cancel := context.WithTimeout(context.Background(), runBudget)
		run, runErr := runTranslation(ctx, measured, engine, session, policy, name, operational)
		cancel()
		r.Runs = append(r.Runs, run)
		if isDeadline {
			if !errors.Is(runErr, context.DeadlineExceeded) || len(run.Requests) == 0 || run.Requests[len(run.Requests)-1].Finish != inference.Timeout || run.Requests[len(run.Requests)-1].RequestTimeoutMS != budget.Milliseconds() {
				return fmt.Errorf("100ms cancellation was not observed: %v", runErr)
			}
		} else if runErr != nil {
			return runErr
		}
	}
	return nil
}
