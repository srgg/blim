module github.com/srg/blim

go 1.24.2

require (
	github.com/aarzilli/golua v0.0.0-20250217091409-248753f411c4
	github.com/cornelk/hashmap v1.0.8
	github.com/creack/pty v1.1.24
	github.com/fatih/color v1.18.0
	github.com/go-ble/ble v0.0.0-20240122180141-8c5522f54333
	github.com/hexops/gotextdiff v1.0.3
	github.com/mcuadros/go-defaults v1.2.0
	github.com/sirupsen/logrus v1.9.3
	github.com/spf13/cobra v1.10.1
	github.com/stretchr/testify v1.11.1
	golang.org/x/term v0.36.0
)

require (
	github.com/dop251/goja v0.0.0-20260106131823-651366fbe6e3
	github.com/hedzr/go-ringbuf/v2 v2.2.2
	github.com/smallnest/ringbuffer v0.0.0-20250317021400-0da97b586904
	github.com/srgg/testify v0.1.1
	github.com/wk8/go-ordered-map/v2 v2.1.8
	github.com/yudai/gojsondiff v1.0.0
	golang.org/x/sys v0.37.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/JuulLabs-OSS/cbgo v0.0.2 // indirect
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.1.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dlclark/regexp2 v1.11.4 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/google/pprof v0.0.0-20250403155104-27863c87afa6 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mailru/easyjson v0.9.1 // indirect
	github.com/mattn/go-colorable v0.1.14 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mgutz/ansi v0.0.0-20200706080929-d51e80ef957d // indirect
	github.com/mgutz/logxi v0.0.0-20161027140823-aebf8a7d67ab // indirect
	github.com/nsf/jsondiff v0.0.0-20230430225905-43f6cf3098c1 // indirect
	github.com/onsi/ginkgo v1.16.5 // indirect
	github.com/onsi/gomega v1.38.2 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/raff/goble v0.0.0-20200327175727-d63360dcfd80 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/sergi/go-diff v1.4.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/yudai/golcs v0.0.0-20170316035057-ecda9a501e82 // indirect
	github.com/yudai/pp v2.0.1+incompatible // indirect
	golang.org/x/text v0.28.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

//replace github.com/aarzilli/golua => /Users/srg/src/golua

replace github.com/go-ble/ble => github.com/srgg/go-ble v0.0.0-20251019021511-162008c817dc

replace github.com/hedzr/go-ringbuf/v2 => /Users/srg/src/go-ringbuf

tool github.com/srgg/testify/depend/cmd/dependgen

//replace github.com/srgg/testify => /Users/srg/src/testify
