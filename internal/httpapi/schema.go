package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ruohao1/circular/contracts"
)

type fieldSchema struct {
	Type       string                 `json:"type"`
	Format     string                 `json:"format"`
	Default    any                    `json:"default"`
	MinLength  int                    `json:"minLength"`
	MaxLength  int                    `json:"maxLength"`
	AnyOf      []fieldSchema          `json:"anyOf"`
	Properties map[string]fieldSchema `json:"properties"`
	Required   []string               `json:"required"`
}

func schemas() map[string]fieldSchema {
	var document struct {
		Components struct {
			Schemas map[string]fieldSchema `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(contracts.OpenAPI, &document); err != nil {
		panic("invalid embedded HTTP contract")
	}
	return document.Components.Schemas
}

var contractSchemas = schemas()

type validationError struct {
	Type string `json:"type"`
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
}

func invalid(w http.ResponseWriter, where, name, kind, message string) {
	location := []any{where}
	if name != "" {
		location = append(location, name)
	}
	respond(w, 422, map[string]any{"detail": []validationError{{kind, location, message}}})
}

// Decode request fields from the same checked-in contract used by the frontend.
// Unknown fields stay ignored, matching the original resource creation contract.
func body(w http.ResponseWriter, r *http.Request, schema string) (map[string]any, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16*1024*1024))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var input map[string]any
	if !utf8.Valid(data) || decoder.Decode(&input) != nil || input == nil {
		invalid(w, "body", "", "json_invalid", "JSON object required")
		return nil, false
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		invalid(w, "body", "", "json_invalid", "One JSON object required")
		return nil, false
	}
	spec := contractSchemas[schema]
	required := map[string]bool{}
	for _, name := range spec.Required {
		required[name] = true
	}
	result := map[string]any{}
	for _, name := range sortedFields(spec.Properties) {
		field := spec.Properties[name]
		value, exists := input[name]
		if !exists {
			if required[name] {
				invalid(w, "body", name, "missing", "Field required")
				return nil, false
			}
			value = field.Default
			if value == nil && field.Type == "object" {
				value = map[string]any{}
			}
		}
		if len(field.AnyOf) > 0 {
			if value == nil {
				result[name] = nil
				continue
			}
			field = field.AnyOf[0]
		}
		switch field.Type {
		case "string":
			text, ok := value.(string)
			if !ok {
				invalid(w, "body", name, "string_type", "Input should be a valid string")
				return nil, false
			}
			length := utf8.RuneCountInString(text)
			if length < field.MinLength {
				invalid(w, "body", name, "string_too_short", fmt.Sprintf("String should have at least %d characters", field.MinLength))
				return nil, false
			}
			if field.MaxLength > 0 && length > field.MaxLength {
				invalid(w, "body", name, "string_too_long", fmt.Sprintf("String should have at most %d characters", field.MaxLength))
				return nil, false
			}
			if strings.ContainsRune(text, 0) {
				invalid(w, "body", name, "string_unicode", "Input contains unsupported text")
				return nil, false
			}
			if field.Format == "uuid" {
				id, err := uuid.Parse(text)
				if err != nil {
					invalid(w, "body", name, "uuid_parsing", "Input should be a valid UUID")
					return nil, false
				}
				value = id.String()
			}
		case "object":
			if _, ok := value.(map[string]any); !ok {
				invalid(w, "body", name, "dict_type", "Input should be a valid dictionary")
				return nil, false
			}
		}
		result[name] = value
	}
	return result, true
}

func sortedFields[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func identifier(w http.ResponseWriter, value, where, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(value)
	if err != nil {
		invalid(w, where, name, "uuid_parsing", "Input should be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
