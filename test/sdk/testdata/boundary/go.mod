module github.com/crevissepartners/projmux/sdk-boundary-fixture

go 1.25.0

require github.com/crevissepartners/projmux v0.0.0

replace github.com/crevissepartners/projmux => ../../../..

require github.com/crevissepartners/projmux/internal/web/fixture v0.0.0

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.9 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/crevissepartners/projmux/internal/web/fixture => ./fakeweb
