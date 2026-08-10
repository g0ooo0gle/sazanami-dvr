package programsearch

import (
	"github.com/g0ooo0gle/sazanami-dvr/internal/adapters/ctrlcmd/codec"
	core "github.com/g0ooo0gle/sazanami-dvr/internal/core/autoreservation"
	"github.com/g0ooo0gle/sazanami-dvr/internal/core/catalogmodel"
)

type preparedCondition struct {
	matcher core.ProgramMatcher
}

func prepare(search core.SearchCondition) (preparedCondition, error) {
	matcher, err := core.PrepareProgramMatcher(search)
	if err != nil {
		return preparedCondition{}, failure(codec.Malformed, "program-search-regexp-invalid", 0)
	}
	return preparedCondition{matcher: matcher}, nil
}

func (prepared preparedCondition) matches(program catalogmodel.CurrentProgram) bool {
	return prepared.matcher.Matches(program)
}
