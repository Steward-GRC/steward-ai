// Copyright 2026 The Steward Authors
// SPDX-License-Identifier: Apache-2.0

package grpcsvc

import (
	"math"

	aiv1 "github.com/Steward-GRC/steward-ai/gen/go/steward/ai/v1"
	"github.com/Steward-GRC/steward-ai/internal/cache"
	"github.com/Steward-GRC/steward-ai/internal/generation"
)

// toInt32 clamps a domain int into the proto's int32 so a corrupt value can
// never wrap; it keeps gosec's G115 check in one place.
func toInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n) //nosec G115 -- bounded by the guards above
	}
}

func toPBCitations(cits []generation.Citation) []*aiv1.Citation {
	out := make([]*aiv1.Citation, 0, len(cits))
	for _, c := range cits {
		out = append(out, &aiv1.Citation{
			PolicyId:     c.PolicyID,
			PolicyTitle:  c.PolicyTitle,
			VersionNo:    toInt32(c.VersionNo),
			VersionId:    c.VersionID,
			SectionKey:   c.SectionKey,
			ChunkId:      c.ChunkID,
			ChunkIndex:   toInt32(c.ChunkIndex),
			DocumentType: c.DocumentType,
		})
	}
	return out
}

func toPBSegments(segs []generation.AnswerSegment) []*aiv1.AnswerSegment {
	if len(segs) == 0 {
		return nil
	}
	out := make([]*aiv1.AnswerSegment, 0, len(segs))
	for _, seg := range segs {
		sources := make([]*aiv1.SegmentSource, 0, len(seg.Sources))
		for _, src := range seg.Sources {
			sources = append(sources, &aiv1.SegmentSource{
				PolicyId:   src.PolicyID,
				VersionId:  src.VersionID,
				SectionKey: src.SectionKey,
				ChunkId:    src.ChunkID,
				ChunkIndex: toInt32(src.ChunkIndex),
			})
		}
		out = append(out, &aiv1.AnswerSegment{Start: toInt32(seg.Start), End: toInt32(seg.End), Sources: sources})
	}
	return out
}

func toCacheAnswer(a generation.AnswerResponse) cache.Answer {
	out := cache.Answer{
		AnswerText:         a.Answer,
		NoAuthorizedSource: a.NoAuthorizedSource,
		HasSensitiveSource: a.HasSensitiveSource,
	}
	for _, c := range a.Citations {
		out.Citations = append(out.Citations, cache.Citation(c))
	}
	for _, seg := range a.Segments {
		cs := cache.AnswerSegment{Start: seg.Start, End: seg.End}
		for _, src := range seg.Sources {
			cs.Sources = append(cs.Sources, cache.SegmentSource(src))
		}
		out.Segments = append(out.Segments, cs)
	}
	return out
}

func fromCacheAnswer(a cache.Answer) generation.AnswerResponse {
	out := generation.AnswerResponse{
		Answer:             a.AnswerText,
		NoAuthorizedSource: a.NoAuthorizedSource,
		HasSensitiveSource: a.HasSensitiveSource,
	}
	for _, c := range a.Citations {
		out.Citations = append(out.Citations, generation.Citation(c))
	}
	for _, seg := range a.Segments {
		gs := generation.AnswerSegment{Start: seg.Start, End: seg.End}
		for _, src := range seg.Sources {
			gs.Sources = append(gs.Sources, generation.SegmentSource(src))
		}
		out.Segments = append(out.Segments, gs)
	}
	return out
}
