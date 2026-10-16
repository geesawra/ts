package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validConfig = `
[jackett]
url = "http://127.0.0.1:9117/jackett"
api_key = "private-api-key"

[[transmission]]
name = "home"
url = "http://127.0.0.1:9091/transmission/rpc"
`

func loadText(t *testing.T, text string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestDefaultsAndMultipleEndpoints(t *testing.T) {
	cfg, err := loadText(t, validConfig+`
username = "transmission-user"
password = "transmission-password"
download_dir = "/srv/downloads"

[[transmission]]
name = "remote"
url = "https://remote.example/transmission/rpc"
timeout_seconds = 120
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:8080" || cfg.Jackett.TimeoutSeconds != 60 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if len(cfg.Transmission) != 2 || cfg.Transmission[0].TimeoutSeconds != 60 || cfg.Transmission[1].TimeoutSeconds != 120 {
		t.Fatalf("unexpected endpoint defaults: %#v", cfg.Transmission)
	}
	if cfg.OMDb.APIKey != "" || cfg.OMDb.TimeoutSeconds != 60 {
		t.Fatalf("OMDb must be disabled by default: %#v", cfg.OMDb)
	}
	if cfg.Transmission[0].Username != "transmission-user" || cfg.Transmission[0].Password != "transmission-password" || cfg.Transmission[0].DownloadDir != "/srv/downloads" {
		t.Fatalf("endpoint settings were not preserved: %#v", cfg.Transmission[0])
	}
}

func TestExplicitServerSettings(t *testing.T) {
	cfg, err := loadText(t, `[server]
listen = "0.0.0.0:8090"
username = "web-user"
password = "web-password"
`+validConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "0.0.0.0:8090" || cfg.Server.Username != "web-user" || cfg.Server.Password != "web-password" {
		t.Fatalf("server settings were not preserved: %#v", cfg.Server)
	}
}

func TestInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"syntax", "not valid TOML ="},
		{"unknown root field", "typo = true\n" + validConfig},
		{"unknown nested field", validConfig + "passwrod = 'private-password'\n"},
		{"wrong type", strings.Replace(validConfig, `name = "home"`, `name = 23`, 1)},
		{"missing API key", strings.Replace(validConfig, `api_key = "private-api-key"`, "", 1)},
		{"blank API key", strings.Replace(validConfig, `private-api-key`, "  ", 1)},
		{"missing endpoint", "[jackett]\nurl = 'http://localhost:9117'\napi_key = 'key'\n"},
		{"blank name", strings.Replace(validConfig, `name = "home"`, `name = " "`, 1)},
		{"duplicate names", validConfig + "\n[[transmission]]\nname = 'home'\nurl = 'http://other:9091/transmission/rpc'\n"},
		{"server username only", "[server]\nusername = 'user'\n" + validConfig},
		{"server password only", "[server]\npassword = 'private-password'\n" + validConfig},
		{"blank listen", "[server]\nlisten = ''\n" + validConfig},
		{"endpoint username only", validConfig + "username = 'user'\n"},
		{"endpoint password only", validConfig + "password = 'private-password'\n"},
		{"zero Jackett timeout", strings.Replace(validConfig, "[jackett]", "[jackett]\ntimeout_seconds = 0", 1)},
		{"negative Jackett timeout", strings.Replace(validConfig, "[jackett]", "[jackett]\ntimeout_seconds = -1", 1)},
		{"zero endpoint timeout", validConfig + "timeout_seconds = 0\n"},
		{"negative endpoint timeout", validConfig + "timeout_seconds = -1\n"},
		{"overflowing duration", validConfig + "timeout_seconds = 9223372037\n"},
		{"zero disabled OMDb timeout", validConfig + "\n[omdb]\ntimeout_seconds = 0\n"},
		{"negative disabled OMDb timeout", validConfig + "\n[omdb]\ntimeout_seconds = -1\n"},
		{"overflowing disabled OMDb timeout", validConfig + "\n[omdb]\ntimeout_seconds = 9223372037\n"},
		{"zero enabled OMDb timeout", validConfig + "\n[omdb]\napi_key = 'private-api-key'\ntimeout_seconds = 0\n"},
		{"unknown OMDb field", validConfig + "\n[omdb]\napi_key = 'private-api-key'\ntimeuot_seconds = 5\n"},
		{"wrong OMDb key type", validConfig + "\n[omdb]\napi_key = ['private-api-key']\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadText(t, test.text); err == nil {
				t.Fatal("expected invalid configuration to fail")
			} else if strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), "private-api-key") {
				t.Fatalf("error disclosed a secret: %v", err)
			}
		})
	}
}

func TestRejectUnsafeURLs(t *testing.T) {
	urls := []string{
		"", "/relative", "ftp://example.com", "http:///missing-host",
		"http://private-user:private-password@example.com", "https://example.com?api_key=private-api-key",
		"https://example.com?", "https://example.com#private-fragment", "https://example.com#",
		"http://example.com:bad/rpc", "http://bad host/rpc",
	}
	for _, raw := range urls {
		for _, target := range []string{"http://127.0.0.1:9117/jackett", "http://127.0.0.1:9091/transmission/rpc"} {
			t.Run(target+"/"+raw, func(t *testing.T) {
				_, err := loadText(t, strings.Replace(validConfig, target, raw, 1))
				if err == nil {
					t.Fatal("expected unsafe URL to be rejected")
				}
				if strings.Contains(err.Error(), "private-") {
					t.Fatalf("error disclosed a secret: %v", err)
				}
			})
		}
	}
}

func TestOptionalOMDbConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		section string
		key     string
		timeout int
	}{
		{"absent", "", "", 60},
		{"empty section", "[omdb]\n", "", 60},
		{"blank key", "[omdb]\napi_key = '  '\n", "", 60},
		{"key only", "[omdb]\napi_key = 'private-omdb-key'\n", "private-omdb-key", 60},
		{"explicit timeout", "[omdb]\napi_key = ' private-omdb-key '\ntimeout_seconds = 12\n", "private-omdb-key", 12},
		{"disabled custom timeout", "[omdb]\ntimeout_seconds = 7\n", "", 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, err := loadText(t, validConfig+"\n"+test.section)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OMDb.APIKey != test.key || cfg.OMDb.TimeoutSeconds != test.timeout {
				t.Fatal("OMDb settings were not preserved")
			}
		})
	}
}
