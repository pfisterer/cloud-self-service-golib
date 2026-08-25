module github.com/pfisterer/cloud-self-service-golib

// The lowest Go version among the consuming services (dynamic-zones-api), not
// the highest. A library that declares more than a consumer has forces that
// consumer to upgrade in the same change, which is exactly the coupling this
// module is meant to avoid.
go 1.25.0

require (
	github.com/modelcontextprotocol/go-sdk v1.7.0
	go.uber.org/zap v1.28.0
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.2
)

require (
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/oauth2 v0.35.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)
