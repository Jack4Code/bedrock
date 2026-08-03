module github.com/Jack4Code/bedrock/grpc

go 1.25.5

// Development only: consumers resolve the parent from its published version.
// Dropped when this module is tagged against a released bedrock.
replace github.com/Jack4Code/bedrock => ../

require (
	github.com/Jack4Code/bedrock v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.0
)

require (
	github.com/BurntSushi/toml v1.5.0 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
