// Package segment plans protected text against exact immutable request budgets.
package segment

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/inference"
	"github.com/sh4869221b/yakuori/internal/prompt"
	"github.com/sh4869221b/yakuori/internal/protect"
	"github.com/sh4869221b/yakuori/internal/unit"
	"github.com/sh4869221b/yakuori/internal/validate"
)

const Schema = "safe-boundaries-v1"
const BoundaryFixtures = "safe-boundaries-fixtures-v1"

var (
	ErrContextLimit   = errors.New("segment context limit")
	ErrInvalidPlan    = errors.New("invalid segment plan")
	ErrInvalidRequest = errors.New("invalid segment request")
	ErrInvalidResult  = errors.New("invalid segment result")
)

func ProfileInput() validate.SegmentationInput {
	return validate.SegmentationInput{Schema: Schema, BoundaryFixtures: BoundaryFixtures}
}

type Plan struct{ data *planData }
type planData struct {
	parent   unit.UnitID
	prepared protect.Prepared
	leading  protect.Piece
	segments []Segment
}
type Segment struct {
	owner            *planData
	ordinal          int
	piece, separator protect.Piece
	request          inference.GenerationRequest
	count            int
}

func (p Plan) ParentID() unit.UnitID {
	if p.data == nil {
		return unit.UnitID{}
	}
	return p.data.parent
}
func (p Plan) Segments() []Segment {
	if p.data == nil {
		return nil
	}
	return slices.Clone(p.data.segments)
}
func (s Segment) ParentID() unit.UnitID {
	if s.owner == nil {
		return unit.UnitID{}
	}
	return s.owner.parent
}
func (s Segment) Ordinal() int                              { return s.ordinal }
func (s Segment) SourceRange() protect.ByteRange            { return s.piece.SourceRange() }
func (s Segment) PreparedRange() protect.ByteRange          { return s.piece.PreparedRange() }
func (s Segment) Source() string                            { return s.piece.Source() }
func (s Segment) Text() string                              { return s.piece.Text() }
func (s Segment) GenerationRequired() bool                  { return s.piece.GenerationRequired() }
func (s Segment) Request() inference.GenerationRequest      { return s.request }
func (s Segment) PromptTokens() int                         { return s.count }
func (s Segment) Separator() string                         { return s.separator.Text() }
func (s Segment) SeparatorSourceRange() protect.ByteRange   { return s.separator.SourceRange() }
func (s Segment) SeparatorPreparedRange() protect.ByteRange { return s.separator.PreparedRange() }
func (p Plan) LeadingSeparator() string {
	if p.data == nil {
		return ""
	}
	return p.data.leading.Text()
}
func (p Plan) LeadingSourceRange() protect.ByteRange {
	if p.data == nil {
		return protect.ByteRange{}
	}
	return p.data.leading.SourceRange()
}
func (p Plan) LeadingPreparedRange() protect.ByteRange {
	if p.data == nil {
		return protect.ByteRange{}
	}
	return p.data.leading.PreparedRange()
}

// Build finishes admission for every segment before returning a usable plan.
// Candidates are tested longest first without assuming monotonic token counts.
func Build(ctx context.Context, parent unit.UnitID, prepared protect.Prepared, info inference.ModelInfo, build func(context.Context, string) (inference.GenerationRequest, error), count func(context.Context, inference.GenerationRequest) (int, error)) (Plan, error) {
	return BuildWithLimits(ctx, parent, prepared, info, build, count, config.DefaultLimits())
}

// BuildWithLimits bounds source bytes before whole-unit tokenization and bounds
// retained segment requests while planning against the smaller context cap.
func BuildWithLimits(ctx context.Context, parent unit.UnitID, prepared protect.Prepared, info inference.ModelInfo, build func(context.Context, string) (inference.GenerationRequest, error), count func(context.Context, inference.GenerationRequest) (int, error), limits config.Limits) (Plan, error) {
	if err := limits.Check(); err != nil {
		return Plan{}, err
	}
	info.ContextTokens = min(info.ContextTokens, limits.ContextTokens)

	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if parent == (unit.UnitID{}) || build == nil || count == nil {
		return Plan{}, ErrInvalidPlan
	}
	if info.ContextTokens <= 0 {
		return Plan{}, ErrContextLimit
	}
	whole, err := prepared.Slice(0, len(prepared.Text()))
	if err != nil {
		return Plan{}, err
	}
	if err := limits.CheckUnitTextBytes(len(whole.Source())); err != nil {
		return Plan{}, err
	}
	d := &planData{parent: parent, prepared: prepared}
	d.leading, err = prepared.Slice(0, 0)
	if err != nil {
		return Plan{}, err
	}
	var policy inference.EffectivePolicy
	var identity inference.RequestIdentity
	initialized := false
	admit := func(piece protect.Piece) (inference.GenerationRequest, int, bool, error) {
		if err := ctx.Err(); err != nil {
			return inference.GenerationRequest{}, 0, false, err
		}
		if !piece.GenerationRequired() {
			return inference.GenerationRequest{}, 0, true, nil
		}
		request, err := build(ctx, piece.Text())
		if err != nil {
			return inference.GenerationRequest{}, 0, false, fmt.Errorf("build request: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return inference.GenerationRequest{}, 0, false, err
		}
		id, p := request.Identity(), request.Policy()
		if id.ModelSHA256 != info.ModelSHA256 || id.Tokenizer != info.Tokenizer || id.Template != info.Template || id.PromptSchema != prompt.SchemaV1 || p.Schema != info.PolicySchema || p.Schema != inference.PolicySchemaV1 || p.MaxOutputTokens <= 0 || p.MaxOutputTokens > limits.MaxOutputTokens || p.RequestTimeout <= 0 || p.RequestTimeout > limits.RequestTimeout {
			return inference.GenerationRequest{}, 0, false, ErrInvalidRequest
		}
		if initialized && (id != identity || !reflect.DeepEqual(p, policy)) {
			return inference.GenerationRequest{}, 0, false, ErrInvalidRequest
		}
		if !initialized {
			identity, policy, initialized = id, p, true
		}
		n, err := count(ctx, request)
		if err != nil {
			return inference.GenerationRequest{}, 0, false, fmt.Errorf("count request: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return inference.GenerationRequest{}, 0, false, err
		}
		if n <= 0 || n != len(request.TokenIDs()) {
			return inference.GenerationRequest{}, 0, false, ErrInvalidRequest
		}
		return request, n, n <= info.ContextTokens && p.MaxOutputTokens <= info.ContextTokens-n, nil
	}
	add := func(piece protect.Piece, cut protect.Cut, request inference.GenerationRequest, n int) error {
		separator, err := prepared.Slice(cut.End, cut.Next)
		if err != nil {
			return err
		}
		d.segments = append(d.segments, Segment{owner: d, ordinal: len(d.segments), piece: piece, separator: separator, request: request, count: n})
		return nil
	}
	request, n, fits, err := admit(whole)
	if err != nil {
		return Plan{}, err
	}
	if fits {
		if err := add(whole, protect.Cut{End: len(prepared.Text()), Next: len(prepared.Text())}, request, n); err != nil {
			return Plan{}, err
		}
		return Plan{d}, nil
	}
	cuts := prepared.Cuts()
	start, end := 0, len(prepared.Text())
	if len(cuts) > 0 && cuts[0].End == 0 {
		start = cuts[0].Next
		d.leading, err = prepared.Slice(0, start)
		if err != nil {
			return Plan{}, err
		}
	}
	// Whole whitespace has no nonempty body to split.
	if start == end {
		return Plan{}, ErrContextLimit
	}
	if len(cuts) == 0 || cuts[len(cuts)-1].Next != end {
		cuts = append(cuts, protect.Cut{End: end, Next: end})
	}
	for start < end {
		if err := limits.CheckSegments(len(d.segments) + 1); err != nil {
			return Plan{}, err
		}
		selected := false
		for i := len(cuts) - 1; i >= 0; i-- {
			cut := cuts[i]
			if cut.End <= start || (start == 0 && cut.End == end) {
				continue
			}
			piece, err := prepared.Slice(start, cut.End)
			if err != nil {
				return Plan{}, err
			}
			req, tokens, fits, err := admit(piece)
			if err != nil {
				return Plan{}, err
			}
			if !fits {
				continue
			}
			if err := add(piece, cut, req, tokens); err != nil {
				return Plan{}, err
			}
			start, selected = cut.Next, true
			break
		}
		if !selected {
			return Plan{}, ErrContextLimit
		}
	}
	plan := Plan{d}
	if err := plan.checkPartition(); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// Result binds output to the exact segment snapshot used for generation.
type Result struct {
	Segment Segment
	Text    string
}

func Join(plan Plan, results []Result) (string, error) {
	if err := plan.checkPartition(); err != nil {
		return "", err
	}
	if len(results) != len(plan.data.segments) {
		return "", ErrInvalidResult
	}
	var joined strings.Builder
	joined.WriteString(plan.data.leading.Text())
	for i, result := range results {
		expected := plan.data.segments[i]
		if result.Segment.owner != plan.data || result.Segment.ordinal != i {
			return "", ErrInvalidResult
		}
		if err := plan.data.prepared.CheckCandidate(expected.piece, result.Text); err != nil {
			return "", fmt.Errorf("segment %d: %w", i, err)
		}
		joined.WriteString(result.Text)
		joined.WriteString(expected.separator.Text())
	}
	return joined.String(), nil
}

func (p Plan) checkPartition() error {
	if p.data == nil || len(p.data.segments) == 0 {
		return ErrInvalidPlan
	}
	d := p.data
	whole, err := d.prepared.Slice(0, len(d.prepared.Text()))
	if err != nil {
		return ErrInvalidPlan
	}
	source, prepared := 0, 0
	pieces := []protect.Piece{d.leading}
	for i, s := range d.segments {
		if s.owner != d || s.ordinal != i || (s.piece.PreparedRange().Start == s.piece.PreparedRange().End && len(d.segments) > 1) {
			return ErrInvalidPlan
		}
		pieces = append(pieces, s.piece, s.separator)
	}
	for _, piece := range pieces {
		sr, pr := piece.SourceRange(), piece.PreparedRange()
		if sr.Start != source || pr.Start != prepared {
			return ErrInvalidPlan
		}
		expected, err := d.prepared.Slice(pr.Start, pr.End)
		if err != nil || expected != piece || expected.SourceRange() != sr {
			return ErrInvalidPlan
		}
		source, prepared = sr.End, pr.End
	}
	if source != whole.SourceRange().End || prepared != len(d.prepared.Text()) {
		return ErrInvalidPlan
	}
	return nil
}
