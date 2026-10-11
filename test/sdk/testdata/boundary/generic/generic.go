package generic

import "github.com/crevissepartners/projmux/internal/core/persona"

type Result[T any] struct{ Value T }

func Read() Result[persona.Entry] { return Result[persona.Entry]{} }
