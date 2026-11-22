package genericjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/config"
	"github.com/sh4869221b/yakuori/internal/unit"
)

const (
	schemaName = "yakuori.generic-json"
	version    = 1
	// Bound the number of full-source searches needed to check declared literals.
	maxSpans = 64
)

var (
	errJSON      = errors.New("invalid generic JSON structure or value")
	errDuplicate = errors.New("duplicate generic JSON field")
	errRequired  = errors.New("missing required generic JSON field")
	errVersion   = errors.New("unsupported generic JSON schema or version")
)

type document struct {
	metadata Metadata
	units    []sourceUnit
}

type sourceUnit struct {
	id, source string
	context    map[string]string
	spans      []unit.ProtectionSpan
}

type parser struct {
	json   *json.Decoder
	raw    []byte
	limits config.Limits
}

func parse(raw []byte, limits config.Limits) (document, error) {
	if err := limits.CheckArtifactBytes(len(raw)); err != nil {
		return document{}, err
	}
	if !utf8.Valid(raw) {
		return document{}, errJSON
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()

	p := parser{json: d, raw: raw, limits: limits}
	var doc document
	err := p.object(map[string]field{
		"schema":          {read: p.readSchema},
		"version":         {read: p.readVersion},
		"source_language": {read: p.readString(&doc.metadata.SourceLanguage, true)},
		"target_language": {read: p.readString(&doc.metadata.TargetLanguage, true)},
		"scope": {read: func() error {
			return p.object(map[string]field{
				"game_namespace":     {read: p.readString(&doc.metadata.GameNamespace, true)},
				"artifact_namespace": {read: p.readString(&doc.metadata.ArtifactNamespace, true)},
			})
		}},
		"units": {read: func() (err error) {
			doc.units, err = p.readUnits()
			return err
		}},
	})
	if err != nil {
		return document{}, err
	}
	// InputOffset is immediately after the closing brace. Only JSON whitespace
	// may follow. No further value or unrecognized object is decoded.
	if len(bytes.Trim(raw[d.InputOffset():], " \t\r\n")) != 0 {
		return document{}, errJSON
	}
	return doc, nil
}

func (p *parser) readSchema() error {
	value, err := p.string()
	if err == nil && value != schemaName {
		return errVersion
	}
	return err
}

func (p *parser) readVersion() error {
	n, err := p.integer()
	if err == nil && n != version {
		return errVersion
	}
	return err
}

func (p *parser) readUnits() ([]sourceUnit, error) {
	if err := p.delim('['); err != nil {
		return nil, err
	}
	var units []sourceUnit
	ids := make(map[string]struct{})
	total := 0
	for p.json.More() {
		if err := p.limits.CheckUnits(len(units) + 1); err != nil {
			return nil, err
		}
		var u sourceUnit
		err := p.object(map[string]field{
			"id":     {read: p.readString(&u.id, true)},
			"source": {read: p.readString(&u.source, false)},
			"translation": {optional: true, read: func() error {
				_, err := p.text(false)
				return err
			}},
			"protection": {optional: true, read: func() (err error) {
				u.spans, err = p.readSpans()
				return err
			}},
			"context": {optional: true, read: func() (err error) {
				u.context, err = p.readContext()
				return err
			}},
		})
		if err != nil {
			return nil, err
		}
		if _, exists := ids[u.id]; exists {
			return nil, unit.ErrDuplicateUnit
		}
		total += len(u.source) // Non-overlapping decoded strings cannot exceed len(raw).
		if err := p.limits.CheckTotalTextBytes(total); err != nil {
			return nil, err
		}
		ids[u.id] = struct{}{}
		units = append(units, u)
	}
	if len(units) == 0 {
		return nil, errJSON
	}
	return units, p.delim(']')
}

func (p *parser) readContext() (map[string]string, error) {
	var speaker, scene string
	if err := p.object(map[string]field{
		"speaker": {optional: true, read: p.readString(&speaker, true)},
		"scene":   {optional: true, read: p.readString(&scene, true)},
	}); err != nil {
		return nil, err
	}
	context := make(map[string]string, 2)
	if speaker != "" {
		context["speaker"] = speaker
	}
	if scene != "" {
		context["scene"] = scene
	}
	return context, nil
}

func (p *parser) readSpans() ([]unit.ProtectionSpan, error) {
	if err := p.delim('['); err != nil {
		return nil, err
	}
	var spans []unit.ProtectionSpan
	for p.json.More() {
		if len(spans) == maxSpans {
			return nil, &config.LimitError{Limit: "protection spans", Actual: len(spans) + 1, Maximum: maxSpans}
		}
		var span unit.ProtectionSpan
		var kind string
		err := p.object(map[string]field{
			"start":   {read: p.readInt(&span.Start)},
			"end":     {read: p.readInt(&span.End)},
			"kind":    {read: p.readString(&kind, true)},
			"pair":    {optional: true, read: p.readString(&span.Pair, true)},
			"ordered": {read: p.readBool(&span.Ordered)},
		})
		if err != nil {
			return nil, err
		}
		span.Kind = unit.ProtectionKind(kind)
		spans = append(spans, span)
	}
	return spans, p.delim(']')
}

type field struct {
	read     func() error
	optional bool
}

// Every reader consumes one value at its fixed schema position, so input can
// never induce recursive descent past the five containers in a protection span.
func (p *parser) object(fields map[string]field) error {
	if err := p.delim('{'); err != nil {
		return err
	}
	for p.json.More() {
		key, err := p.string()
		if err != nil {
			return err
		}
		f, known := fields[key]
		if !known {
			return errJSON
		}
		if f.read == nil {
			return errDuplicate
		}
		if err := f.read(); err != nil {
			return err
		}
		f.read = nil
		fields[key] = f
	}
	if err := p.delim('}'); err != nil {
		return err
	}
	for _, f := range fields {
		if !f.optional && f.read != nil {
			return errRequired
		}
	}
	return nil
}

func (p *parser) delim(want json.Delim) error {
	token, err := p.json.Token()
	if err != nil || token != want {
		return errJSON
	}
	return nil
}

func (p *parser) string() (string, error) {
	start := p.json.InputOffset()
	token, err := p.json.Token()
	value, ok := token.(string)
	if err != nil || !ok || strings.ContainsRune(value, 0) {
		return "", errJSON
	}
	// encoding/json maps an unpaired surrogate to U+FFFD. A real U+FFFD
	// must survive, so inspect the original escapes only when ambiguous.
	if strings.ContainsRune(value, utf8.RuneError) && !validSurrogates(p.raw[start:p.json.InputOffset()]) {
		return "", errJSON
	}
	return value, nil
}

func validSurrogates(raw []byte) bool {
	for {
		next := bytes.IndexByte(raw, '\\')
		if next < 0 {
			return true
		}
		escape := raw[next+1]
		raw = raw[next+2:]
		if escape != 'u' {
			continue
		}
		// Token already verified the escape syntax and the four hex digits.
		n, _ := strconv.ParseUint(string(raw[:4]), 16, 16)
		raw = raw[4:]
		if n < 0xD800 || n > 0xDFFF {
			continue
		}
		if n >= 0xDC00 || len(raw) < 6 || raw[0] != '\\' || raw[1] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(raw[2:6]), 16, 16)
		if err != nil || low < 0xDC00 || low > 0xDFFF {
			return false
		}
		raw = raw[6:]
	}
}

func (p *parser) text(nonempty bool) (string, error) {
	s, err := p.string()
	if err != nil {
		return "", err
	}
	if nonempty && s == "" {
		return "", errJSON
	}
	if err := p.limits.CheckUnitTextBytes(len(s)); err != nil {
		return "", err
	}
	return s, nil
}

func (p *parser) readString(dst *string, nonempty bool) func() error {
	return func() (err error) {
		*dst, err = p.text(nonempty)
		return err
	}
}

func (p *parser) integer() (int, error) {
	token, err := p.json.Token()
	number, ok := token.(json.Number)
	if err != nil || !ok {
		return 0, errJSON
	}
	value, err := strconv.Atoi(string(number))
	if err != nil {
		return 0, errJSON
	}
	return value, nil
}

func (p *parser) readInt(dst *int) func() error {
	return func() (err error) {
		*dst, err = p.integer()
		return err
	}
}

func (p *parser) readBool(dst *bool) func() error {
	return func() error {
		token, err := p.json.Token()
		value, ok := token.(bool)
		if err != nil || !ok {
			return errJSON
		}
		*dst = value
		return nil
	}
}
