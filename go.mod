module github.com/pfisterer/cloud-self-service-golib

// The lowest Go version among the consuming services (dynamic-zones-api), not
// the highest. A library that declares more than a consumer has forces that
// consumer to upgrade in the same change, which is exactly the coupling this
// module is meant to avoid.
go 1.25.0

require (
	go.uber.org/zap v1.28.0
	gorm.io/driver/sqlite v1.6.0
	gorm.io/gorm v1.31.2
)

require (
	github.com/jinzhu/inflection v1.0.0 // indirect
	github.com/jinzhu/now v1.1.5 // indirect
	github.com/mattn/go-sqlite3 v1.14.22 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/text v0.20.0 // indirect
)
