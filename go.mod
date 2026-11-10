module mapper

go 1.26.5

require (
	github.com/DataDog/zstd v1.5.7
	github.com/marcgauthier/libmurmur/clients/go v0.0.0
	github.com/marcgauthier/loggy v0.0.0-20261022121611-4421778da37f
	github.com/marcgauthier/websocket v1.0.0
	golang.org/x/crypto v0.41.0
)

require (
	golang.org/x/net v0.43.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/marcgauthier/libmurmur/clients/go => ../libmurmur/clients/go
