package contextpack

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
)

const (
	maxBytes       = 2 << 20
	maxEntries     = 4096
	maxStringBytes = 16 << 10
	maxDepth       = 8
)

// Read validates one complete v1 JSON document from r. Diagnostics contain
// fixed reasons and positions, with no input text or underlying I/O errors.
func Read(r io.Reader) (*Document, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, errors.New("read failed")
	}
	if len(data) > maxBytes {
		return nil, errors.New("input exceeds byte limit")
	}
	if err := checkJSON(data); err != nil {
		return nil, err
	}
	var pack Pack
	if err := jsonv2.Unmarshal(data, &pack, jsonv2.RejectUnknownMembers(true)); err != nil {
		var semantic *jsonv2.SemanticError
		if errors.As(err, &semantic) {
			reason := "unexpected JSON type"
			if errors.Is(semantic.Err, jsonv2.ErrUnknownName) {
				reason = "unknown JSON member"
			}
			return nil, fmt.Errorf("%s at byte %d", reason, semantic.ByteOffset)
		}
		return nil, errors.New("invalid JSON document")
	}
	if err := validate(pack); err != nil {
		return nil, err
	}
	pack.sortSets()
	canonical, err := json.Marshal(pack)
	if err != nil {
		return nil, errors.New("encode canonical JSON failed")
	}
	if len(canonical) > maxBytes {
		return nil, errors.New("canonical JSON exceeds byte limit")
	}
	return &Document{Pack: pack, Canonical: canonical, Fingerprint: sha256.Sum256(canonical)}, nil
}

func checkJSON(data []byte) error {
	// Token decoding rejects duplicate member names, invalid UTF-8 and unpaired
	// surrogates by default. Check limits before allocating typed collections.
	d := jsontext.NewDecoder(bytes.NewReader(data))
	root, err := d.ReadToken()
	if err != nil || root.Kind() != jsontext.KindBeginObject {
		return errors.New("expected a JSON object at root")
	}
	entries := 0
	for d.StackDepth() > 0 {
		parent, _ := d.StackIndex(d.StackDepth())
		token, err := d.ReadToken()
		if err != nil {
			return fmt.Errorf("invalid JSON syntax at byte %d", d.InputOffset())
		}
		kind := token.Kind()
		if parent == jsontext.KindBeginArray && kind != jsontext.KindEndArray {
			entries++
			if entries > maxEntries {
				return fmt.Errorf("array entries exceed limit at byte %d", d.InputOffset())
			}
		}
		if d.StackDepth() > maxDepth {
			return fmt.Errorf("JSON nesting exceeds limit at byte %d", d.InputOffset())
		}
		if kind == jsontext.KindNull {
			return fmt.Errorf("JSON null at byte %d", d.InputOffset())
		}
		if kind == jsontext.KindString && len(token.String()) > maxStringBytes {
			return fmt.Errorf("JSON string exceeds byte limit at byte %d", d.InputOffset())
		}
	}
	if _, err := d.ReadToken(); err != io.EOF {
		return fmt.Errorf("trailing data at byte %d", d.InputOffset())
	}
	return nil
}
