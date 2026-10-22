package assets

import _ "embed"

//go:embed daemonset-config.yaml
var DaemonsetConfig string

//go:embed gateway-config.yaml
var GatewayConfig string

//go:embed scraper-config.yaml
var ScraperConfig string

//go:embed profiling-config.yaml
var ProfilingConfig string

//go:embed postgres-config.yaml
var PostgresConfig string

//go:embed postgres-setup.sql
var PostgresSetupSQL string

//go:embed redaction-default.yaml
var RedactionDefault string
