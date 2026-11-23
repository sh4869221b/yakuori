package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/sh4869221b/yakuori/internal/dictionary"
)

const (
	maxXMLBytes = 2 << 20
	maxRows     = 4096
)

func convert(input io.Reader, report io.Writer, profile exportProfile) (dictionary.Document, error) {
	data, err := io.ReadAll(io.LimitReader(input, maxXMLBytes+1))
	if err != nil {
		return dictionary.Document{}, errors.New("cannot read XML")
	}
	if len(data) > maxXMLBytes {
		return dictionary.Document{}, errors.New("XML exceeds 2 MiB")
	}
	decoder := xml.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	p := xmlReader{Decoder: decoder}
	doc := dictionary.Document{Version: dictionary.Version}
	err = p.readExport(&doc, report, profile)
	return doc, err
}

func (p *xmlReader) readExport(doc *dictionary.Document, report io.Writer, profile exportProfile) error {
	if _, err := p.start("SSTXMLRessources"); err != nil {
		return err
	}
	if _, err := p.start("Params"); err != nil {
		return err
	}
	for _, param := range []struct{ field, want string }{
		{"Addon", profile.scope.Artifact}, {"Source", profile.sourceLanguage}, {"Dest", profile.targetLanguage}, {"Version", "2"},
	} {
		got, _, err := p.text(param.field)
		if err != nil {
			return err
		}
		if got != param.want {
			return fmt.Errorf("line %d: Params/%s does not match the selected XML v2 profile", p.line, param.field)
		}
	}
	if err := p.end("Params"); err != nil {
		return err
	}
	if _, err := p.start("Content"); err != nil {
		return err
	}

	seen := make(map[string]struct{})
	for {
		token, err := p.next("Content/String", false)
		if err != nil {
			return err
		}
		if token == (xml.EndElement{Name: xml.Name{Local: "Content"}}) {
			break
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name != (xml.Name{Local: "String"}) {
			return fmt.Errorf("line %d: expected Content/String element", p.line)
		}
		if len(doc.Entries) == maxRows {
			return fmt.Errorf("line %d: Content exceeds %d String elements", p.line, maxRows)
		}
		line := p.line
		entry, err := p.readString(start, report, profile)
		if err != nil {
			return err
		}
		if _, ok := seen[entry.ID]; ok {
			return fmt.Errorf("line %d: String identity is duplicated", line)
		}
		seen[entry.ID] = struct{}{}
		doc.Entries = append(doc.Entries, entry)
	}
	if len(doc.Entries) == 0 {
		return fmt.Errorf("line %d: Content requires at least one String", p.line)
	}
	if err := p.end("SSTXMLRessources"); err != nil {
		return err
	}
	if _, err := p.next("document", false); err != io.EOF {
		return fmt.Errorf("line %d: unexpected data after SSTXMLRessources", p.line)
	}
	return nil
}

func (p *xmlReader) readString(start xml.StartElement, report io.Writer, profile exportProfile) (dictionary.Entry, error) {
	var empty dictionary.Entry
	attrs, err := p.attributes(start, "List", "Partial")
	if err != nil {
		return empty, err
	}
	if _, err := strconv.ParseUint(attrs["List"], 10, 32); err != nil {
		return empty, fmt.Errorf("line %d: String@List requires a decimal index", p.line)
	}
	if partial, ok := attrs["Partial"]; ok {
		switch partial {
		case "1":
			fmt.Fprintf(report, "line %d: String@Partial (incompleteTrans) is not represented in neutral JSON v1\n", p.line)
		case "2":
			fmt.Fprintf(report, "line %d: String@Partial (lockedTrans) is not represented in neutral JSON v1\n", p.line)
		default:
			return empty, fmt.Errorf("line %d: unsupported String@Partial", p.line)
		}
	}
	edid, _, err := p.text("EDID")
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(edid) == "" {
		return empty, fmt.Errorf("line %d: missing EDID identity", p.line)
	}
	rec, recAttrs, err := p.text("REC", "id")
	if err != nil {
		return empty, err
	}
	if strings.TrimSpace(rec) == "" {
		return empty, fmt.Errorf("line %d: missing REC identity", p.line)
	}
	identity := []string{attrs["List"], edid, rec}
	if id, ok := recAttrs["id"]; ok {
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return empty, fmt.Errorf("line %d: REC@id requires a decimal index", p.line)
		}
		identity = append(identity, id)
	}
	source, _, err := p.text("Source")
	if err != nil {
		return empty, err
	}
	target, _, err := p.text("Dest")
	if err != nil {
		return empty, err
	}
	if err := p.end("String"); err != nil {
		return empty, err
	}
	return profile.entry(identity, source, target), nil
}

type xmlReader struct {
	*xml.Decoder
	line int
}

// next leaves XML text untouched inside leaf fields and skips XML whitespace
// between elements. The fixed element readers below bound the document depth.
func (p *xmlReader) next(field string, inText bool) (xml.Token, error) {
	for {
		offset := p.InputOffset()
		p.line, _ = p.InputPos()
		token, err := p.Token()
		if err == io.EOF {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid XML in %s", p.line, field)
		}
		switch t := token.(type) {
		case xml.CharData:
			if inText || len(bytes.Trim(t, " \t\r\n")) > 0 {
				return token, nil
			}
		case xml.Comment:
		case xml.ProcInst:
			if offset != 0 || t.Target != "xml" {
				return nil, fmt.Errorf("line %d: unsupported XML processing instruction in %s", p.line, field)
			}
		case xml.Directive:
			return nil, fmt.Errorf("line %d: unsupported XML directive in %s", p.line, field)
		default:
			return token, nil
		}
	}
}

func (p *xmlReader) start(field string, allowedAttrs ...string) (map[string]string, error) {
	token, err := p.next(field, false)
	if err != nil && err != io.EOF {
		return nil, err
	}
	start, ok := token.(xml.StartElement)
	if !ok || start.Name != (xml.Name{Local: field}) {
		return nil, fmt.Errorf("line %d: expected %s element", p.line, field)
	}
	return p.attributes(start, allowedAttrs...)
}

func (p *xmlReader) attributes(start xml.StartElement, allowed ...string) (map[string]string, error) {
	var attrs map[string]string
	if len(start.Attr) > 0 {
		attrs = make(map[string]string, len(start.Attr))
	}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" || !slices.Contains(allowed, attr.Name.Local) {
			return nil, fmt.Errorf("line %d: unsupported attribute on %s", p.line, start.Name.Local)
		}
		if _, duplicate := attrs[attr.Name.Local]; duplicate {
			return nil, fmt.Errorf("line %d: duplicate attribute on %s", p.line, start.Name.Local)
		}
		attrs[attr.Name.Local] = attr.Value
	}
	return attrs, nil
}

func (p *xmlReader) end(field string) error {
	token, err := p.next(field, false)
	if err != nil && err != io.EOF {
		return err
	}
	if token != (xml.EndElement{Name: xml.Name{Local: field}}) {
		return fmt.Errorf("line %d: expected end of %s", p.line, field)
	}
	return nil
}

func (p *xmlReader) text(field string, allowedAttrs ...string) (string, map[string]string, error) {
	attrs, err := p.start(field, allowedAttrs...)
	if err != nil {
		return "", nil, err
	}
	line := p.line
	var content strings.Builder
	for {
		token, err := p.next(field, true)
		if err != nil && err != io.EOF {
			return "", nil, err
		}
		switch t := token.(type) {
		case xml.CharData:
			content.Write(t)
		case xml.EndElement:
			p.line = line
			return content.String(), attrs, nil
		default:
			return "", nil, fmt.Errorf("line %d: %s requires XML text", p.line, field)
		}
	}
}
