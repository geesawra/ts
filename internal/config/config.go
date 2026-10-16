package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Server       Server         `toml:"server"`
	Jackett      Jackett        `toml:"jackett"`
	OMDb         OMDb           `toml:"omdb"`
	Transmission []Transmission `toml:"transmission"`
}

type Server struct {
	Listen   string `toml:"listen"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type Jackett struct {
	URL            string `toml:"url"`
	APIKey         string `toml:"api_key"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type OMDb struct {
	APIKey         string `toml:"api_key"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

type Transmission struct {
	Name           string `toml:"name"`
	URL            string `toml:"url"`
	Username       string `toml:"username"`
	Password       string `toml:"password"`
	DownloadDir    string `toml:"download_dir"`
	TimeoutSeconds int    `toml:"timeout_seconds"`
}

// Load reads a complete configuration and rejects unknown TOML fields.
func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()

	cfg := Config{Server: Server{Listen: "127.0.0.1:8080"}}
	// Pointers distinguish an omitted timeout from an explicitly invalid zero.
	var document struct {
		Server  Server `toml:"server"`
		Jackett struct {
			URL            string `toml:"url"`
			APIKey         string `toml:"api_key"`
			TimeoutSeconds *int   `toml:"timeout_seconds"`
		} `toml:"jackett"`
		OMDb struct {
			APIKey         string `toml:"api_key"`
			TimeoutSeconds *int   `toml:"timeout_seconds"`
		} `toml:"omdb"`
		Transmission []struct {
			Name           string `toml:"name"`
			URL            string `toml:"url"`
			Username       string `toml:"username"`
			Password       string `toml:"password"`
			DownloadDir    string `toml:"download_dir"`
			TimeoutSeconds *int   `toml:"timeout_seconds"`
		} `toml:"transmission"`
	}
	document.Server = cfg.Server
	if err := toml.NewDecoder(file).DisallowUnknownFields().Decode(&document); err != nil {
		// Parser diagnostics may quote configuration values, including secrets.
		return Config{}, errors.New("invalid TOML configuration (check syntax, field names, and value types)")
	}
	cfg.Server = document.Server
	cfg.Jackett = Jackett{URL: document.Jackett.URL, APIKey: document.Jackett.APIKey, TimeoutSeconds: timeout(document.Jackett.TimeoutSeconds)}
	cfg.OMDb = OMDb{APIKey: strings.TrimSpace(document.OMDb.APIKey), TimeoutSeconds: timeout(document.OMDb.TimeoutSeconds)}
	for _, endpoint := range document.Transmission {
		cfg.Transmission = append(cfg.Transmission, Transmission{
			Name: endpoint.Name, URL: endpoint.URL, Username: endpoint.Username,
			Password: endpoint.Password, DownloadDir: endpoint.DownloadDir,
			TimeoutSeconds: timeout(endpoint.TimeoutSeconds),
		})
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func timeout(value *int) int {
	if value == nil {
		return 60
	}
	return *value
}

func validate(cfg Config) error {
	if strings.TrimSpace(cfg.Server.Listen) == "" {
		return errors.New("server.listen must not be empty")
	}
	if !paired(cfg.Server.Username, cfg.Server.Password) {
		return errors.New("server username and password must both be set or both be omitted")
	}
	if !validURL(cfg.Jackett.URL) {
		return errors.New("jackett.url must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(cfg.Jackett.APIKey) == "" {
		return errors.New("jackett.api_key is required")
	}
	if !validTimeout(cfg.Jackett.TimeoutSeconds) {
		return errors.New("jackett.timeout_seconds must be positive and fit a Go duration")
	}
	if !validTimeout(cfg.OMDb.TimeoutSeconds) {
		return errors.New("omdb.timeout_seconds must be positive and fit a Go duration")
	}
	if len(cfg.Transmission) == 0 {
		return errors.New("at least one transmission endpoint is required")
	}
	names := make(map[string]struct{}, len(cfg.Transmission))
	for i, endpoint := range cfg.Transmission {
		if strings.TrimSpace(endpoint.Name) == "" {
			return fmt.Errorf("transmission endpoint %d requires a name", i+1)
		}
		if _, exists := names[endpoint.Name]; exists {
			return fmt.Errorf("transmission endpoint %d has a duplicate name", i+1)
		}
		names[endpoint.Name] = struct{}{}
		if !validURL(endpoint.URL) {
			return fmt.Errorf("transmission endpoint %d URL must be HTTP(S) without credentials, query, or fragment", i+1)
		}
		if !paired(endpoint.Username, endpoint.Password) {
			return fmt.Errorf("transmission endpoint %d username and password must both be set or both be omitted", i+1)
		}
		if !validTimeout(endpoint.TimeoutSeconds) {
			return fmt.Errorf("transmission endpoint %d timeout_seconds must be positive and fit a Go duration", i+1)
		}
	}
	return nil
}

func paired(username, password string) bool {
	return (username == "") == (password == "")
}

func validTimeout(seconds int) bool {
	return seconds > 0 && int64(seconds) <= int64((1<<63-1)/time.Second)
}

func validURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Hostname() != "" && parsed.User == nil && parsed.Opaque == "" &&
		parsed.RawQuery == "" && !parsed.ForceQuery && !strings.Contains(raw, "#")
}
