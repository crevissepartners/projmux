package method

import "github.com/crevissepartners/projmux/internal/core/persona"

type Client struct{}

func (*Client) Read() persona.Entry { return persona.Entry{} }
