package jackett

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

const (
	maxErrorBodyBytes = 64 << 10
	maxErrorRunes     = 1024
)

type statusError struct {
	code   int
	detail string
}

func (s statusError) Error() string {
	message := fmt.Sprintf("Jackett returned HTTP %d", s.code)
	if s.detail != "" {
		message += ": " + s.detail
	}
	return message
}

var (
	errorURL        = regexp.MustCompile(`(?i)(?:https?://|magnet:\?)[^\s<>"']+`)
	errorCredential = regexp.MustCompile(`(?i)\b([\w-]*(?:api[_-]?key|passkey|password|passwd|token|secret|mam_id)[\w-]*["']?\s*[:=]\s*)(?:"[^"]*"|'[^']*'|[^\s,;<>]+)`)
	errorAuthHeader = regexp.MustCompile(`(?im)\b(?:authorization|(?:set-)?cookie)["']?\s*[:=][^\r\n]*`)
)

// responseError consumes a bounded preview, never a whole upstream error page.
// Error bodies are untrusted and can contain URLs, cookies, and API keys.
func responseError(response *http.Response, apiKey string) error {
	defer response.Body.Close()
	status := statusError{code: response.StatusCode}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil || len(body) == 0 {
		return status
	}
	text := strings.ToValidUTF8(string(body), "")
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) == nil {
		// Prefer the explanation over stack traces and other diagnostic fields.
		for _, field := range []string{"error", "message", "detail", "description", "title"} {
			var message string
			if json.Unmarshal(object[field], &message) == nil && strings.TrimSpace(message) != "" {
				text = message
				break
			}
		}
	}
	var xmlError struct {
		XMLName     xml.Name
		Description string `xml:"description,attr"`
	}
	if xml.Unmarshal(body, &xmlError) == nil && xmlError.XMLName.Local == "error" && xmlError.Description != "" {
		text = xmlError.Description
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "html") || strings.Contains(http.DetectContentType([]byte(text)), "html") {
		text = errorHTMLText(text)
	}
	secrets := []string{apiKey}
	if response.Request != nil && response.Request.URL != nil {
		for key, values := range response.Request.URL.Query() {
			if errorCredential.MatchString(key+"=value") || key == "path" {
				secrets = append(secrets, values...)
			}
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	text = errorURL.ReplaceAllString(text, "[URL redacted]")
	text = errorAuthHeader.ReplaceAllString(text, "[credentials redacted]")
	text = errorCredential.ReplaceAllString(text, "${1}[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, text)
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) > maxErrorRunes {
		text = string(runes[:maxErrorRunes]) + "…"
	}
	status.detail = text
	return status
}

func errorHTMLText(body string) string {
	tokens := html.NewTokenizer(strings.NewReader(body))
	var text strings.Builder
	skip := ""
	for {
		switch tokens.Next() {
		case html.ErrorToken:
			return text.String()
		case html.StartTagToken:
			name, _ := tokens.TagName()
			if skip == "" && (string(name) == "head" || string(name) == "script" || string(name) == "style") {
				skip = string(name)
			}
		case html.EndTagToken:
			name, _ := tokens.TagName()
			if string(name) == skip {
				skip = ""
			}
		case html.TextToken:
			if skip == "" {
				text.Write(tokens.Text())
				text.WriteByte(' ')
			}
		}
	}
}
