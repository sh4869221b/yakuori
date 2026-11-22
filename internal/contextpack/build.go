package contextpack

import (
	"bytes"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/url"
	"slices"
	"unicode/utf8"

	"github.com/sh4869221b/yakuori/internal/unit"
)

const buildVersion = "context-build-v1"

// BuildScope supplies the languages and scope of one imported artifact.
type BuildScope struct {
	GameNamespace     string
	ArtifactNamespace string
	SourceLanguage    string
	TargetLanguage    string
}

// Build extracts a candidate from stable unit identities and explicit Origin
// fields. It returns the same validated representation as Read.
func Build(session unit.Session, scope BuildScope) (*Document, error) {
	if err := session.Check(); err != nil {
		return nil, err
	}
	artifact := scope.ArtifactNamespace
	escapedArtifact := url.PathEscape(artifact)
	artifactDigest := session.ArtifactDigest()
	pack := Pack{
		Schema: Schema,
		Scope: Scope{
			GameNamespace: scope.GameNamespace,
			Artifacts:     []Artifact{{ArtifactNamespace: artifact, OriginalArtifactDigest: hex.EncodeToString(artifactDigest[:])}},
		},
		SourceLanguage: scope.SourceLanguage,
		TargetLanguage: scope.TargetLanguage,
	}
	characters := make(map[string]int)
	scenes := make(map[string]int)
	for i, translation := range session.Units() {
		at := position("units", i)
		id := translation.ID()
		for _, part := range []string{id.Adapter(), id.FormatSchema(), id.StableID()} {
			if !validBuildValue(part) {
				return nil, invalid(at, "invalid stable unit identity")
			}
		}
		if translation.SourceLanguage() != scope.SourceLanguage || translation.TargetLanguage() != scope.TargetLanguage {
			return nil, invalid(at, "unit language does not match build scope")
		}
		adapterPath := url.PathEscape(id.Adapter()) + "/" + url.PathEscape(id.FormatSchema()) + "/"
		artifactPath := escapedArtifact + "/" + adapterPath
		stable := url.PathEscape(id.StableID())
		sourceDigest := translation.SourceDigest()
		draft := Unit{
			ID: "unit/" + artifactPath + stable, ArtifactNamespace: artifact,
			Adapter: id.Adapter(), FormatSchema: id.FormatSchema(), StableID: id.StableID(),
			RawSourceDigest: hex.EncodeToString(sourceDigest[:]),
			Provenance:      candidateProvenance(Source{Kind: "artifact", ArtifactNamespace: &artifact}),
		}
		origin := translation.Origin()
		var speakerSource Source
		for _, key := range []string{"speaker", "scene", "quest", "previous_unit", "world"} {
			text, ok := origin[key]
			if !ok {
				continue
			}
			if !validBuildValue(text) {
				return nil, invalid(at, "invalid origin metadata for "+key)
			}
			locator := "units/" + adapterPath + stable + "/origin/" + key
			source := Source{Kind: "metadata", ArtifactNamespace: &artifact, MetadataKey: &locator}
			draft.Provenance.Sources = append(draft.Provenance.Sources, source)
			switch key {
			case "speaker":
				characterID := "character/" + artifactPath + url.PathEscape(text)
				draft.SpeakerID = &characterID
				j, exists := characters[characterID]
				if !exists {
					j = len(pack.Characters)
					characters[characterID] = j
					pack.Characters = append(pack.Characters, Character{
						ID: characterID, Artifacts: []string{artifact}, Provenance: candidateProvenance(),
					})
				}
				c := &pack.Characters[j]
				c.Provenance.Sources = append(c.Provenance.Sources, source)
				speakerSource = source
			case "scene":
				sceneID := "scene/" + artifactPath + url.PathEscape(text)
				draft.SceneID = &sceneID
				j, exists := scenes[sceneID]
				if !exists {
					j = len(pack.Scenes)
					scenes[sceneID] = j
					pack.Scenes = append(pack.Scenes, Scene{
						ID: sceneID, Artifacts: []string{artifact}, Provenance: candidateProvenance(),
					})
				}
				s := &pack.Scenes[j]
				s.Provenance.Sources = append(s.Provenance.Sources, source)
			case "quest":
				draft.Tags = []string{"quest:" + text}
			case "previous_unit":
				previousID := "unit/" + artifactPath + url.PathEscape(text)
				draft.PreviousUnitID = &previousID
			case "world":
				pack.World = append(pack.World, World{
					ID: "world/" + artifactPath + stable, Text: text, Artifacts: []string{artifact},
					Provenance: candidateProvenance(source),
				})
			}
		}
		if draft.SceneID != nil && draft.SpeakerID != nil {
			scene := &pack.Scenes[scenes[*draft.SceneID]]
			if !slices.Contains(scene.SpeakerIDs, *draft.SpeakerID) {
				scene.SpeakerIDs = append(scene.SpeakerIDs, *draft.SpeakerID)
			}
			scene.Provenance.Sources = append(scene.Provenance.Sources, speakerSource)
		}
		pack.Units = append(pack.Units, draft)
	}
	data, err := jsonv2.Marshal(pack)
	if err != nil {
		return nil, errors.New("encode context candidate failed")
	}
	return Read(bytes.NewReader(data))
}

func validBuildValue(s string) bool {
	return nonblank(s) && utf8.ValidString(s)
}

func candidateProvenance(sources ...Source) Provenance {
	edited := false
	return Provenance{Method: "extraction", Version: buildVersion, Status: "candidate", UserEdited: &edited, Sources: sources}
}
