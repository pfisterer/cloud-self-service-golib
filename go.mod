module github.com/pfisterer/cloud-self-service-golib

// The lowest Go version among the consuming services (dynamic-zones-api), not
// the highest. A library that declares more than a consumer has forces that
// consumer to upgrade in the same change, which is exactly the coupling this
// module is meant to avoid.
go 1.25.0

require go.uber.org/zap v1.28.0

require go.uber.org/multierr v1.10.0 // indirect
