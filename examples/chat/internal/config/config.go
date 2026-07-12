// Package config — typed application settings via sconf: YAML file +
// environment variables (CHAT_ prefix, __ as the section separator) +
// command-line flags, later sources override earlier ones.
package config

import (
	"os"

	"github.com/dvislobokov/sconf"
)

type Settings struct {
	HTTP struct {
		Addr string `default:":8080"`
	}
	DB struct {
		DSN    string `default:"postgres://postgres:postgres@localhost:5432/chat"`
		Schema string `default:"chat"`
	}
	Log struct {
		Level string `default:"information"`
	}
}

// Load reads appsettings.yaml (optional), then CHAT_* env vars
// (e.g. CHAT_DB__DSN), then command-line args.
func Load() (*Settings, error) {
	return sconf.Load[Settings](
		sconf.New().
			AddYAMLFile("appsettings.yaml", sconf.Optional()).
			AddEnvironmentVariables("CHAT_"),
		os.Args[1:],
	)
}
