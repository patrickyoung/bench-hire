package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

// Retain native command evidence on disk, but never echo configured service
// credentials into manager pages, teaching sources, or worker command output.
func (a *application) redactAppData(c ConnectedApp, raw []byte) []byte {
	dir, err := a.appDir(c.ID)
	if err != nil {
		return raw
	}
	var secrets AppCredentials
	if readJSON(filepath.Join(dir, "credentials.json"), &secrets) != nil {
		return raw
	}
	values := []string{secrets.Token, secrets.ClientSecret}
	for _, v := range secrets.Headers {
		values = append(values, v)
	}
	for _, v := range secrets.Env {
		values = append(values, v)
	}
	slices.SortFunc(values, func(x, y string) int { return len(y) - len(x) })
	redact := func(text string) string {
		for _, value := range values {
			if value != "" {
				text = strings.ReplaceAll(text, value, "[credential removed]")
			}
		}
		return text
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return []byte(redact(string(raw)))
	}
	var walk func(any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			return redact(v)
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
			return v
		case map[string]any:
			result := map[string]any{}
			for k, item := range v {
				result[redact(k)] = walk(item)
			}
			return result
		default:
			return value
		}
	}
	result, err := json.Marshal(walk(value))
	if err != nil {
		return nil
	}
	return result
}

func (a *application) publicAppCall(call AppCall) AppCall {
	c, err := a.loadConnectedApp(call.ConnectionID)
	if err != nil {
		return call
	}
	if call.Result != nil {
		call.Result = a.redactAppData(c, call.Result)
	}
	if call.Input != nil {
		call.Input = a.redactAppData(c, call.Input)
	}
	return call
}
