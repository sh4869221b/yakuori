package goinfer

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"testing"

	"github.com/townsendmerino/goinfer/decoder"
)

func TestOpenUsesStableSnapshot(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "rewrite", true: "atomic replace"}[replace], func(t *testing.T) {
			config, load, model, tok := loadFixture(t)
			var snapshot string
			checkSnapshot := func(path string) {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil || string(data) != fixtureModel {
					t.Fatalf("snapshot bytes=%q error=%v", data, err)
				}
			}
			load.model = func(path string, _ decoder.Options) (modelBackend, error) {
				snapshot = path
				if snapshot == config.path {
					t.Fatal("model loaded from mutable original")
				}
				if replace {
					replacement := config.path + ".new"
					if err := os.WriteFile(replacement, []byte("new model"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, config.path); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(config.path, []byte("new model"), 0600); err != nil {
					t.Fatal(err)
				}
				checkSnapshot(path)
				return model, nil
			}
			load.tokenizer = func(path string) (requestTokenizer, error) {
				if path != snapshot {
					t.Fatalf("tokenizer path=%q model path=%q", path, snapshot)
				}
				checkSnapshot(path)
				return tok, nil
			}
			engine, err := open(context.Background(), config, load)
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close()
			info, err := engine.Info(context.Background())
			if err != nil || info.ModelSHA256 != sha256.Sum256([]byte(fixtureModel)) || info.Tokenizer.ModelSHA256 != info.ModelSHA256 {
				t.Fatalf("snapshot identity=%+v error=%v", info, err)
			}
			if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("snapshot remains after Open: %v", err)
			}
		})
	}
}

func TestOpenRemovesSnapshotOnFailure(t *testing.T) {
	config, load, model, _ := loadFixture(t)
	var snapshot string
	load.tokenizer = func(path string) (requestTokenizer, error) {
		snapshot = path
		return nil, errors.New("tokenizer failed")
	}
	engine, err := open(context.Background(), config, load)
	if engine != nil || err == nil || model.closes != 1 {
		t.Fatalf("engine=%v error=%v closes=%d", engine, err, model.closes)
	}
	if _, err := os.Stat(snapshot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("snapshot remains after failure: %v", err)
	}
}
