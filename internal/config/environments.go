package config

// Environment is a preset that fills both addresses of a configuration.
type Environment struct {
	API string // REST API base, the gateway, ends with /api/v1
	DBC string // SQL endpoint, Trino HTTP protocol
}

var Environments = map[string]Environment{
	"prod": {API: "https://partner.peaka.studio/api/v1", DBC: "https://dbc.peaka.studio:4567"},
	"eu":   {API: "https://partner.eu.peaka.studio/api/v1", DBC: "https://dbc.eu.peaka.studio:4567"},
	"test": {API: "https://api-test.peaka.host/api/v1", DBC: "https://dbc-test.peaka.host:4567"},
}

const DefaultEnvironment = "prod"

func EnvironmentNames() []string { return []string{"prod", "eu", "test"} }
