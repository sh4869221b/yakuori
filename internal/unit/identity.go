package unit

import "github.com/sh4869221b/yakuori/internal/identity"

// NewSourceIdentity binds explicit game/artifact scope and manifest key relations
// to the session's original artifact and unit. It does not persist import identity.
func NewSourceIdentity(session Session, unitID UnitID, gameNamespace, artifactNamespace string, keys [][]byte) (identity.SourceIdentity, error) {
	u, err := session.Unit(unitID)
	if err != nil {
		return identity.SourceIdentity{}, err
	}
	spans := make([]identity.ProtectionSpan, len(u.protectionSpans))
	for i, span := range u.protectionSpans {
		spans[i] = identity.ProtectionSpan{Start: span.Start, End: span.End, Kind: string(span.Kind), Pair: span.Pair, Ordered: span.Ordered}
	}
	return identity.NewSourceIdentity(identity.SourceInput{
		Version: identity.SourceVersion, GameNamespace: gameNamespace, ArtifactNamespace: artifactNamespace,
		OriginalArtifactDigest: session.ArtifactDigest(), Adapter: u.id.Adapter(),
		FormatSchema: u.id.FormatSchema(), StableID: u.id.StableID(), Keys: keys,
		RawSourceDigest: u.SourceDigest(), SourceLanguage: u.SourceLanguage(), TargetLanguage: u.TargetLanguage(),
		ProtectionSpans: spans,
	})
}
