package resourcemodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var ErrSchemaInvalid = errors.New("resource model schema is invalid")

var fieldKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

var reservedFieldKeys = map[string]struct{}{
	"id": {}, "title": {}, "markdown": {}, "summary": {}, "tags": {},
	"source": {}, "attachments": {}, "created_at": {}, "updated_at": {},
	"created_by": {}, "updated_by": {}, "visibility": {}, "publication_status": {},
	"review_status": {}, "quality": {},
}

type ValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SchemaValidationError struct {
	Issues []ValidationIssue `json:"issues"`
}

func (e *SchemaValidationError) Error() string {
	if len(e.Issues) == 0 {
		return ErrSchemaInvalid.Error()
	}
	return fmt.Sprintf("%s: %s", e.Issues[0].Path, e.Issues[0].Message)
}

func (e *SchemaValidationError) Unwrap() error { return ErrSchemaInvalid }

func Validate(contentKind string, fieldSchema, formSchema, listSchema, policy map[string]any) error {
	issues := make([]ValidationIssue, 0)
	canonicalPolicy, normalizeErr := NormalizePolicy(policy)
	if normalizeErr != nil {
		var schemaErr *SchemaValidationError
		if errors.As(normalizeErr, &schemaErr) {
			issues = append(issues, schemaErr.Issues...)
		} else {
			issues = append(issues, issue("policy", "invalid_policy", normalizeErr.Error()))
		}
	} else {
		policy = canonicalPolicy
	}
	if contentKind != "record" && contentKind != "document" && contentKind != "faq" && contentKind != "note" {
		issues = append(issues, issue("content_kind", "invalid_content_kind", "content_kind must be record, document, faq, or note"))
	}
	fields := fieldDefinitions(fieldSchema, &issues)
	validateForm(formSchema, fields, &issues)
	validateList(listSchema, fields, &issues)
	validatePolicy(policy, &issues)
	if len(issues) > 0 {
		return &SchemaValidationError{Issues: issues}
	}
	return nil
}

func SchemaChecksum(contentKind string, fieldSchema, formSchema, listSchema, policy map[string]any) (string, error) {
	canonicalPolicy, err := NormalizePolicy(policy)
	if err != nil {
		return "", err
	}
	policy = canonicalPolicy
	if err := Validate(contentKind, fieldSchema, formSchema, listSchema, policy); err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		ContentKind string         `json:"content_kind"`
		FieldSchema map[string]any `json:"field_schema"`
		FormSchema  map[string]any `json:"form_schema"`
		ListSchema  map[string]any `json:"list_schema"`
		Policy      map[string]any `json:"policy"`
	}{contentKind, fieldSchema, formSchema, listSchema, policy})
	if err != nil {
		return "", fmt.Errorf("marshal schema checksum: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// NormalizePolicy keeps persisted outlet names aligned with the query layer.
// Older contracts used external/open_api for Agent access and member_search
// for workspace search. They are accepted as aliases and stored canonically.
func NormalizePolicy(policy map[string]any) (map[string]any, error) {
	if policy == nil {
		return nil, nil
	}
	result := make(map[string]any, len(policy))
	for key, value := range policy {
		result[key] = value
	}
	rawOutlets, ok := policy["outlets"]
	if !ok {
		return result, nil
	}
	outlets, ok := rawOutlets.(map[string]any)
	if !ok {
		return result, nil
	}
	canonicalOutlets := make(map[string]any, len(outlets))
	for key, value := range outlets {
		canonicalOutlets[key] = value
	}
	for alias, canonical := range map[string]string{
		"external":      "agent_tool",
		"open_api":      "agent_tool",
		"member_search": "workspace",
	} {
		if value, exists := canonicalOutlets[alias]; exists {
			if _, alreadyCanonical := canonicalOutlets[canonical]; !alreadyCanonical {
				canonicalOutlets[canonical] = value
			}
			delete(canonicalOutlets, alias)
		}
	}
	result["outlets"] = canonicalOutlets
	return result, nil
}

func issue(path, code, message string) ValidationIssue {
	return ValidationIssue{Path: path, Code: code, Message: message}
}

func fieldDefinitions(schema map[string]any, issues *[]ValidationIssue) map[string]map[string]any {
	result := make(map[string]map[string]any)
	if schema == nil {
		*issues = append(*issues, issue("field_schema", "required", "field_schema must be an object"))
		return result
	}
	if fields, ok := schema["fields"].([]any); ok {
		seen := map[string]struct{}{}
		for index, raw := range fields {
			definition, ok := raw.(map[string]any)
			if !ok {
				*issues = append(*issues, issue(fmt.Sprintf("field_schema.fields[%d]", index), "invalid_definition", "field definition must be an object"))
				continue
			}
			key, _ := definition["key"].(string)
			validateField(key, definition, fmt.Sprintf("field_schema.fields[%d]", index), issues)
			if _, exists := seen[key]; exists {
				*issues = append(*issues, issue(fmt.Sprintf("field_schema.fields[%d]", index), "duplicate_key", "field key is duplicated"))
			}
			seen[key] = struct{}{}
			result[key] = definition
		}
	} else {
		*issues = append(*issues, issue("field_schema.fields", "required", "field_schema.fields must be an array"))
	}
	if _, ok := schema["additional_properties"].(bool); !ok {
		*issues = append(*issues, issue("field_schema.additional_properties", "required", "additional_properties must be boolean"))
	}
	return result
}

func validateField(key string, definition map[string]any, path string, issues *[]ValidationIssue) {
	if !fieldKeyPattern.MatchString(key) {
		*issues = append(*issues, issue(path+".key", "invalid_key", "field key must match ^[a-z][a-z0-9_]{1,63}$"))
	}
	if _, reserved := reservedFieldKeys[key]; reserved {
		*issues = append(*issues, issue(path+".key", "reserved_key", "field key is reserved"))
	}
	fieldType, _ := definition["type"].(string)
	if fieldType == "" {
		*issues = append(*issues, issue(path+".type", "required", "field type is required"))
	}
	allowedTypes := map[string]struct{}{
		"string": {}, "text": {}, "markdown": {}, "block": {}, "integer": {}, "number": {}, "currency": {},
		"boolean": {}, "date": {}, "datetime": {}, "enum": {}, "multiselect": {}, "object": {}, "json": {},
		"array": {}, "asset_reference": {}, "relation": {}, "person": {}, "department": {}, "tags": {},
		"attachment": {}, "image": {}, "video": {}, "location": {}, "calculated": {},
	}
	if _, ok := allowedTypes[fieldType]; fieldType != "" && !ok {
		*issues = append(*issues, issue(path+".type", "unsupported_type", "field type is not supported"))
	}
	if fieldType == "enum" || fieldType == "multiselect" {
		validateOptions(definition["options"], path+".options", issues)
	} else if _, exists := definition["options"]; exists {
		*issues = append(*issues, issue(path+".options", "unexpected_options", "options are only allowed for enum and multiselect fields"))
	}
	if unique, exists := definition["unique"]; exists {
		if _, ok := unique.(bool); !ok {
			*issues = append(*issues, issue(path+".unique", "invalid_boolean", "unique must be boolean"))
		}
	}
	if calculated, exists := definition["calculated"]; exists {
		if _, ok := calculated.(bool); !ok {
			*issues = append(*issues, issue(path+".calculated", "invalid_boolean", "calculated must be boolean"))
		}
	}
	if fieldType == "calculated" {
		expression, ok := definition["expression"].(string)
		if !ok || strings.TrimSpace(expression) == "" {
			*issues = append(*issues, issue(path+".expression", "required", "calculated fields require an expression"))
		}
	}
	if fieldType == "currency" {
		currency, ok := definition["currency"].(string)
		if !ok || strings.TrimSpace(currency) == "" {
			*issues = append(*issues, issue(path+".currency", "required", "currency fields require a currency code"))
		}
	}
	if fieldType == "object" {
		properties, ok := definition["properties"].(map[string]any)
		if !ok {
			*issues = append(*issues, issue(path+".properties", "required", "object fields require properties"))
		} else {
			for childKey, raw := range properties {
				child, ok := raw.(map[string]any)
				if !ok {
					*issues = append(*issues, issue(path+".properties."+childKey, "invalid_definition", "field definition must be an object"))
					continue
				}
				validateNestedField(childKey, child, path+".properties."+childKey, issues)
			}
		}
	}
	if fieldType == "array" {
		items, ok := definition["items"].(map[string]any)
		if !ok {
			*issues = append(*issues, issue(path+".items", "required", "array fields require an items definition"))
		} else {
			validateNestedField("item", items, path+".items", issues)
		}
	}
	if searchable, ok := definition["searchable"].(bool); ok && searchable && fieldType == "object" {
		*issues = append(*issues, issue(path+".searchable", "invalid_index", "object fields cannot be searchable"))
	}
}

func validateNestedField(key string, definition map[string]any, path string, issues *[]ValidationIssue) {
	copy := make(map[string]any, len(definition)+1)
	for name, value := range definition {
		copy[name] = value
	}
	copy["key"] = key
	validateField(key, copy, path, issues)
}

func validateOptions(raw any, path string, issues *[]ValidationIssue) {
	options, ok := raw.([]any)
	if !ok || len(options) == 0 {
		*issues = append(*issues, issue(path, "invalid_options", "options must be a non-empty array"))
		return
	}
	seen := make(map[string]struct{}, len(options))
	for index, rawOption := range options {
		option, ok := rawOption.(map[string]any)
		if !ok {
			*issues = append(*issues, issue(fmt.Sprintf("%s[%d]", path, index), "invalid_option", "option must be an object"))
			continue
		}
		value, ok := option["value"].(string)
		if !ok || strings.TrimSpace(value) == "" {
			*issues = append(*issues, issue(fmt.Sprintf("%s[%d].value", path, index), "required", "option value must be a non-empty string"))
			continue
		}
		if _, exists := seen[value]; exists {
			*issues = append(*issues, issue(fmt.Sprintf("%s[%d].value", path, index), "duplicate_value", "option value is duplicated"))
		}
		seen[value] = struct{}{}
	}
}

func validateForm(schema map[string]any, fields map[string]map[string]any, issues *[]ValidationIssue) {
	if schema == nil {
		*issues = append(*issues, issue("form_schema", "required", "form_schema must be an object"))
		return
	}
	sections, ok := schema["sections"].([]any)
	if !ok {
		*issues = append(*issues, issue("form_schema.sections", "required", "sections must be an array"))
		return
	}
	for index, raw := range sections {
		section, ok := raw.(map[string]any)
		if !ok {
			*issues = append(*issues, issue(fmt.Sprintf("form_schema.sections[%d]", index), "invalid_section", "section must be an object"))
			continue
		}
		items, ok := section["fields"].([]any)
		if !ok {
			continue
		}
		for itemIndex, rawField := range items {
			key, _ := rawField.(string)
			if key == "" {
				if value, ok := rawField.(map[string]any); ok {
					key, _ = value["key"].(string)
				}
			}
			if _, exists := fields[key]; !exists {
				*issues = append(*issues, issue(fmt.Sprintf("form_schema.sections[%d].fields[%d]", index, itemIndex), "unknown_field", "form references an unknown field"))
			}
		}
	}
}

func validateList(schema map[string]any, fields map[string]map[string]any, issues *[]ValidationIssue) {
	if schema == nil {
		*issues = append(*issues, issue("list_schema", "required", "list_schema must be an object"))
		return
	}
	for _, key := range []string{"columns", "filters"} {
		values, ok := schema[key].([]any)
		if !ok {
			*issues = append(*issues, issue("list_schema."+key, "required", key+" must be an array"))
			continue
		}
		for index, raw := range values {
			fieldKey := ""
			if value, ok := raw.(string); ok {
				fieldKey = value
			} else if value, ok := raw.(map[string]any); ok {
				fieldKey, _ = value["field"].(string)
				if fieldKey == "" {
					fieldKey, _ = value["key"].(string)
				}
			}
			if _, exists := fields[fieldKey]; !exists && !reservedListKey(fieldKey) {
				*issues = append(*issues, issue(fmt.Sprintf("list_schema.%s[%d]", key, index), "unknown_field", "list schema references an unknown field"))
			}
		}
	}
}

func reservedListKey(key string) bool {
	_, ok := reservedFieldKeys[key]
	return ok || key == "id"
}

func validatePolicy(policy map[string]any, issues *[]ValidationIssue) {
	if policy == nil {
		*issues = append(*issues, issue("policy", "required", "policy must be an object"))
		return
	}
	if outlets, ok := policy["outlets"]; ok {
		outletMap, ok := outlets.(map[string]any)
		if !ok {
			*issues = append(*issues, issue("policy.outlets", "invalid_outlets", "outlets must be an object"))
			return
		}
		keys := make([]string, 0, len(outletMap))
		for key := range outletMap {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key != "workspace" && key != "agent_tool" && key != "fulltext" && key != "semantic" {
				*issues = append(*issues, issue("policy.outlets."+key, "invalid_outlet", "outlet must be workspace, agent_tool, fulltext, or semantic"))
				continue
			}
			value, ok := outletMap[key].(map[string]any)
			if !ok {
				*issues = append(*issues, issue("policy.outlets."+key, "invalid_outlet", "outlet policy must be an object"))
				continue
			}
			if enabled, exists := value["enabled"]; exists {
				if _, ok := enabled.(bool); !ok {
					*issues = append(*issues, issue("policy.outlets."+key+".enabled", "invalid_boolean", "enabled must be boolean"))
				}
			}
		}
	}
}

func reservedKeys(schema map[string]any) []string {
	result := make([]string, 0, len(reservedFieldKeys))
	for key := range reservedFieldKeys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func IsSchemaError(err error) bool { return errors.Is(err, ErrSchemaInvalid) }

func ReservedKeys() []string { return reservedKeys(nil) }

func normalizeText(value string) string { return strings.TrimSpace(value) }
