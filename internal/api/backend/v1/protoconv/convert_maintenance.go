package protoconv

import (
	"time"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// KeyBackupToProto converts a bootstrap result, including a partial one.
func KeyBackupToProto(k domain.KeyBackup) *v1.BootstrapKeyBackupResponse {
	return &v1.BootstrapKeyBackupResponse{
		RecoveryKey: k.RecoveryKey,
		Version:     k.Version,
		Uploaded:    int64(k.Uploaded),
		Incomplete:  k.Incomplete,
	}
}

// ProtoToKeyBackup converts a bootstrap result back; nil is the zero value.
func ProtoToKeyBackup(pb *v1.BootstrapKeyBackupResponse) domain.KeyBackup {
	return domain.KeyBackup{
		RecoveryKey: pb.GetRecoveryKey(),
		Version:     pb.GetVersion(),
		Uploaded:    int(pb.GetUploaded()),
		Incomplete:  pb.GetIncomplete(),
	}
}

// SpellSuggestionToProto converts the dictionary and frequency-list offer.
func SpellSuggestionToProto(s domain.SpellSuggestion) *v1.DetectLanguagesResponse {
	return &v1.DetectLanguagesResponse{
		Why: s.Why,
		Candidates: mapSlice(s.Candidates, func(c domain.LanguageCandidate) *v1.LanguageCandidate {
			return &v1.LanguageCandidate{
				Tag: c.Tag, Script: c.Script, Words: int64(c.Words), Share: c.Share, Bytes: c.Bytes,
			}
		}),
		Frequencies: mapSlice(s.Frequencies, func(f domain.FrequencyCandidate) *v1.FrequencyCandidate {
			return &v1.FrequencyCandidate{
				Tag: f.Tag, Script: f.Script, Share: f.Share, Bytes: f.Bytes,
				Disk: f.Disk, Words: f.Words, Recommended: f.Recommended, Accepts: f.Accepts,
			}
		}),
	}
}

// ProtoToSpellSuggestion converts the offer back; nil is the zero value.
func ProtoToSpellSuggestion(pb *v1.DetectLanguagesResponse) domain.SpellSuggestion {
	return domain.SpellSuggestion{
		Why: pb.GetWhy(),
		Candidates: mapSlice(pb.GetCandidates(), func(c *v1.LanguageCandidate) domain.LanguageCandidate {
			return domain.LanguageCandidate{
				Tag: c.GetTag(), Script: c.GetScript(), Words: int(c.GetWords()), Share: c.GetShare(), Bytes: c.GetBytes(),
			}
		}),
		Frequencies: mapSlice(pb.GetFrequencies(), func(f *v1.FrequencyCandidate) domain.FrequencyCandidate {
			return domain.FrequencyCandidate{
				Tag: f.GetTag(), Script: f.GetScript(), Share: f.GetShare(), Bytes: f.GetBytes(),
				Disk: f.GetDisk(), Words: f.GetWords(), Recommended: f.GetRecommended(), Accepts: f.GetAccepts(),
			}
		}),
	}
}

// ModelSuggestionToProto converts the local-model offer; the candidate is sent
// only when there is an offer.
func ModelSuggestionToProto(s domain.ModelSuggestion) *v1.DetectModelResponse {
	out := &v1.DetectModelResponse{Offer: s.Offer, Why: s.Why}
	if s.Offer {
		c := s.Candidate
		out.Candidate = &v1.ModelCandidate{Tag: c.Tag, Name: c.Name, Bytes: c.Bytes, Recall: c.Recall}
	}
	return out
}

// ProtoToModelSuggestion converts the offer back; nil is the zero value.
func ProtoToModelSuggestion(pb *v1.DetectModelResponse) domain.ModelSuggestion {
	out := domain.ModelSuggestion{Offer: pb.GetOffer(), Why: pb.GetWhy()}
	if c := pb.GetCandidate(); c != nil {
		out.Candidate = domain.ModelCandidate{Tag: c.GetTag(), Name: c.GetName(), Bytes: c.GetBytes(), Recall: c.GetRecall()}
	}
	return out
}

// MisspellingsToProto converts spell-check findings.
func MisspellingsToProto(ms []domain.Misspelling) []*v1.Misspelling {
	out := make([]*v1.Misspelling, 0, len(ms))
	for _, m := range ms {
		out = append(out, &v1.Misspelling{
			Word: m.Word, Start: int64(m.Start), End: int64(m.End),
			Suggestions: m.Suggestions, Rare: m.Rare, Certain: m.Certain,
		})
	}
	return out
}

// ProtoToMisspellings converts findings back. The result is never nil: an empty
// check is "no misspellings", which callers may range over or compare.
func ProtoToMisspellings(pb []*v1.Misspelling) []domain.Misspelling {
	out := make([]domain.Misspelling, 0, len(pb))
	for _, m := range pb {
		out = append(out, domain.Misspelling{
			Word:        m.GetWord(),
			Start:       int(m.GetStart()),
			End:         int(m.GetEnd()),
			Suggestions: m.GetSuggestions(),
			Rare:        m.GetRare(),
			Certain:     m.GetCertain(),
		})
	}
	return out
}

// ReactionRefusalsToProto converts the learned refusals.
func ReactionRefusalsToProto(rs []domain.ReactionRefusal) []*v1.ReactionRefusal {
	out := make([]*v1.ReactionRefusal, 0, len(rs))
	for _, rf := range rs {
		out = append(out, &v1.ReactionRefusal{Protocol: rf.Protocol, Emoji: rf.Emoji, AtUnixMs: rf.At.UnixMilli()})
	}
	return out
}

// ProtoToReactionRefusals converts the refusals back; never nil, like
// ProtoToMisspellings.
func ProtoToReactionRefusals(pb []*v1.ReactionRefusal) []domain.ReactionRefusal {
	out := make([]domain.ReactionRefusal, 0, len(pb))
	for _, rf := range pb {
		out = append(out, domain.ReactionRefusal{
			Protocol: rf.GetProtocol(),
			Emoji:    rf.GetEmoji(),
			At:       time.UnixMilli(rf.GetAtUnixMs()),
		})
	}
	return out
}
