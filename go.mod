module github.com/xiaolai/recite

go 1.27

toolchain go1.27.2

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/coder/websocket v1.8.15
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/xiaolai/sayso/go v0.1.3
	github.com/yuin/goldmark v1.8.6
	go.uber.org/goleak v1.3.0
	golang.org/x/text v0.41.0
)

replace github.com/xiaolai/sayso/go => ../sayso/go
