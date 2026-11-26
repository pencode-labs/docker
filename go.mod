module github.com/pencode-labs/docker

go 1.27.0

require (
	github.com/onix-engine/onix v0.1.10
	github.com/pencode-labs/pencode v0.1.0-rc.15
)

require golang.org/x/net v0.63.0 // indirect

exclude github.com/onix-engine/onix v0.0.0-00010101000000-000000000000

replace github.com/onix-engine/onix => ../../../onix
