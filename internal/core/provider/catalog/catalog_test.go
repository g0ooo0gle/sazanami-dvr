package catalog

import (
	"testing"
	"time"
)

func TestCloneProgramPageDeepCopiesPointers(t *testing.T) {
	start := time.Now()
	duration := time.Minute
	video := ProgramVideo{StreamContent: 1, ComponentType: 2}
	original := ProgramPage{Items: []ProgramObservation{{Start: &start, Duration: &duration,
		Extended: []ProgramExtended{{Heading: "見出し", Body: "本文"}}, Genres: []ProgramGenre{{Level1: 1}}, Video: &video,
		Audios: []ProgramAudio{{SamplingRate: 48_000, Languages: []string{"jpn"}}}}}}
	clone := CloneProgramPage(original)
	*clone.Items[0].Start = time.Time{}
	*clone.Items[0].Duration = 0
	clone.Items[0].Extended[0].Body = "変更"
	clone.Items[0].Genres[0].Level1 = 2
	clone.Items[0].Video.ComponentType = 3
	clone.Items[0].Audios[0].Languages[0] = "eng"
	if original.Items[0].Start.IsZero() || *original.Items[0].Duration == 0 || original.Items[0].Extended[0].Body != "本文" ||
		original.Items[0].Genres[0].Level1 != 1 || original.Items[0].Video.ComponentType != 2 || original.Items[0].Audios[0].Languages[0] != "jpn" {
		t.Fatal("clone retained pointer fields")
	}
}
