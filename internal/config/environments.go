package config

// Environment is a preset that fills both addresses of a configuration.
type Environment struct {
	API    string // REST API base, the gateway, ends with /api/v1
	DBC    string // SQL endpoint, Trino HTTP protocol
	OAuth  string // OAuth 2.0 authorization server (issuer) for browser login
	Studio string // the studio web app, for pages the CLI opens in the browser (file pickers)
}

var Environments = map[string]Environment{
	"prod": {API: "https://partner.peaka.studio/api/v1", DBC: "https://dbc.peaka.studio:4567", OAuth: "https://oauth.service.peaka.studio", Studio: "https://peaka.studio"},
	"eu":   {API: "https://partner.eu.peaka.studio/api/v1", DBC: "https://dbc.eu.peaka.studio:4567", OAuth: "https://oauth.service.eu.peaka.studio", Studio: "https://eu.peaka.studio"},
	"test": {API: "https://api-test.peaka.host/api/v1", DBC: "https://dbc-test.peaka.host:4567", OAuth: "https://oauth-test.service.peaka.host", Studio: "https://test.peaka.host"},
}

const DefaultEnvironment = "prod"

func EnvironmentNames() []string { return []string{"prod", "eu", "test"} }
