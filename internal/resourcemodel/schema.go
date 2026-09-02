package resourcemodel

import (
	"bytes"
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

// ChannelPolicy is one publication channel on the policy JSON. ContentScope is
// only meaningful for the agent/open_api channels and must be published or all.
type ChannelPolicy struct {
	Enabled      bool   `json:"enabled"`
	ContentScope string `json:"content_scope,omitempty"`
}

// EnablePolicy is the retrieval toggle shape ({enabled: bool}).
type EnablePolicy struct {
	Enabled bool `json:"enabled"`
}

// PublishingPolicy is the publishing section of the policy JSON.
type PublishingPolicy struct {
	Mode                     string   `json:"mode"`
	RequiredFields           []string `json:"required_fields"`
	RequireCleanAttachments  bool     `json:"require_clean_attachments"`
	RequireHumanConfirmation bool     `json:"require_human_confirmation"`
}

// PolicyVisibility is the visibility section of the policy JSON.
type PolicyVisibility struct {
	Default string   `json:"default"`
	Allowed []string `json:"allowed"`
}

// Policy is the final (phase 0) resource model policy structure. The legacy
// channel shape is rejected on parse — it is never normalized.
type Policy struct {
	Visibility PolicyVisibility         `json:"visibility"`
	Channels   map[string]ChannelPolicy `json:"channels"`
	Retrieval  map[string]EnablePolicy  `json:"retrieval"`
	Publishing PublishingPolicy         `json:"publishing"`
}

var allowedChannelKeys = map[string]struct{}{
	"workspace": {}, "public_site": {}, "agent": {}, "open_api": {},
}

var scopedChannelKeys = map[string]struct{}{
	"agent": {}, "open_api": {},
}

var allowedContentScopes = map[string]struct{}{
	"published": {}, "all": {},
}

var allowedVisibilityValues = map[string]struct{}{
	"workspace": {}, "organization": {}, "public": {},
}

var allowedPublishingModes = map[string]struct{}{
	"direct": {}, "approval": {},
}

// PolicyFromJSON parses and validates the final policy structure. The legacy
// outlet shape and any unknown section are rejected explicitly — nothing is
// normalized or converted.
func PolicyFromJSON(raw []byte) (Policy, error) {
	var probe map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&probe); err != nil {
		return Policy{}, fmt.Errorf("%w: policy must be a JSON object", ErrSchemaInvalid)
	}
	for key := range probe {
		switch key {
		case "visibility", "channels", "retrieval", "publishing":
		default:
			return Policy{}, fmt.Errorf("%w: policy.%s is not a known policy section; the legacy outlet shape was removed", ErrSchemaInvalid, key)
		}
	}
	policy := Policy{}
	// Sections are decoded individually with unknown-field rejection because
	// json.Decoder cannot DisallowUnknownFields across map values.
	if rawVisibility, ok := probe["visibility"]; ok {
		var visibility PolicyVisibility
		if err := decodeStrict(rawVisibility, &visibility); err != nil {
			return Policy{}, fmt.Errorf("%w: policy.visibility is invalid: %v", ErrSchemaInvalid, err)
		}
		policy.Visibility = visibility
	}
	if rawChannels, ok := probe["channels"]; ok {
		var channels map[string]ChannelPolicy
		if err := decodeStrict(rawChannels, &channels); err != nil {
			return Policy{}, fmt.Errorf("%w: policy.channels is invalid: %v", ErrSchemaInvalid, err)
		}
		policy.Channels = channels
	}
	if rawRetrieval, ok := probe["retrieval"]; ok {
		var retrieval map[string]EnablePolicy
		if err := decodeStrict(rawRetrieval, &retrieval); err != nil {
			return Policy{}, fmt.Errorf("%w: policy.retrieval is invalid: %v", ErrSchemaInvalid, err)
		}
		policy.Retrieval = retrieval
	}
	if rawPublishing, ok := probe["publishing"]; ok {
		var publishing PublishingPolicy
		if err := decodeStrict(rawPublishing, &publishing); err != nil {
			return Policy{}, fmt.Errorf("%w: policy.publishing is invalid: %v", ErrSchemaInvalid, err)
		}
		policy.Publishing = publishing
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func decodeStrict(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

// Validate checks the canonical policy invariants.
func (p Policy) Validate() error {
	if p.Visibility.Default != "" {
		if _, ok := allowedVisibilityValues[p.Visibility.Default]; !ok {
			return fmt.Errorf("%w: policy.visibility.default must be workspace, organization, or public", ErrSchemaInvalid)
		}
	}
	seenAllowed := map[string]struct{}{}
	for _, value := range p.Visibility.Allowed {
		if _, ok := allowedVisibilityValues[value]; !ok {
			return fmt.Errorf("%w: policy.visibility.allowed must only contain workspace, organization, or public", ErrSchemaInvalid)
		}
		if _, dup := seenAllowed[value]; dup {
			return fmt.Errorf("%w: policy.visibility.allowed contains a duplicate value", ErrSchemaInvalid)
		}
		seenAllowed[value] = struct{}{}
	}
	if p.Visibility.Default != "" {
		if _, ok := seenAllowed[p.Visibility.Default]; !ok {
			return fmt.Errorf("%w: policy.visibility.default must be listed in policy.visibility.allowed", ErrSchemaInvalid)
		}
	}
	channels := p.Channels
	if channels == nil {
		channels = map[string]ChannelPolicy{}
	}
	keys := make([]string, 0, len(channels))
	for key := range channels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowedChannelKeys[key]; !ok {
			return fmt.Errorf("%w: policy.channels.%s is not a known channel", ErrSchemaInvalid, key)
		}
		channel := channels[key]
		_, scoped := scopedChannelKeys[key]
		if channel.ContentScope != "" {
			if !scoped {
				return fmt.Errorf("%w: policy.channels.%s.content_scope is only allowed on agent and open_api", ErrSchemaInvalid, key)
			}
			if _, ok := allowedContentScopes[channel.ContentScope]; !ok {
				return fmt.Errorf("%w: policy.channels.%s.content_scope must be published or all", ErrSchemaInvalid, key)
			}
		}
	}
	retrieval := p.Retrieval
	if retrieval == nil {
		retrieval = map[string]EnablePolicy{}
	}
	retrievalKeys := make([]string, 0, len(retrieval))
	for key := range retrieval {
		retrievalKeys = append(retrievalKeys, key)
	}
	sort.Strings(retrievalKeys)
	for _, key := range retrievalKeys {
		if key != "structured" && key != "fulltext" && key != "semantic" {
			return fmt.Errorf("%w: policy.retrieval.%s is not a known retrieval channel", ErrSchemaInvalid, key)
		}
	}
	if p.Publishing.Mode != "" {
		if _, ok := allowedPublishingModes[p.Publishing.Mode]; !ok {
			return fmt.Errorf("%w: policy.publishing.mode must be direct or approval", ErrSchemaInvalid)
		}
	}
	return nil
}

// ToJSON renders the canonical policy JSON.
func (p Policy) ToJSON() []byte {
	body, err := json.Marshal(p)
	if err != nil {
		return []byte("{}")
	}
	return body
}

func Validate(contentKind string, fieldSchema, formSchema, listSchema, policy map[string]any) error {
	issues := make([]ValidationIssue, 0)
	validatePolicy(policy, &issues)
	if contentKind != "record" && contentKind != "document" && contentKind != "faq" && contentKind != "note" {
		issues = append(issues, issue("content_kind", "invalid_content_kind", "content_kind must be record, document, faq, or note"))
	}
	fields := fieldDefinitions(fieldSchema, &issues)
	validateForm(formSchema, fields, &issues)
	validateList(listSchema, fields, &issues)
	if len(issues) > 0 {
		return &SchemaValidationError{Issues: issues}
	}
	return nil
}

// SchemaChecksum keeps the historical map-based signature. The policy is
// validated and canonically marshaled as-is — no alias normalization happens.
func SchemaChecksum(contentKind string, fieldSchema, formSchema, listSchema, policy map[string]any) (string, error) {
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
	// The 2026-09-02 over-design sweep converged the vocabulary to the types
	// the product actually exercises (builtin models use enum/integer/
	// string/date). Retired zero-semantics types — block, json, currency,
	// relation, person, department, attachment, image, video, location,
	// calculated — had no downstream consumer (query families, JSON-value
	// validation and extraction schemas all default them away), and
	// department contradicted doc §15 (no departments in v2).
	allowedTypes := map[string]struct{}{
		"string": {}, "text": {}, "markdown": {}, "integer": {}, "number": {},
		"boolean": {}, "date": {}, "datetime": {}, "enum": {}, "multiselect": {},
		"object": {}, "array": {}, "asset_reference": {},
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

// validatePolicy parses the final policy structure strictly. Legacy shapes are
// rejected with an explicit issue — nothing is converted.
func validatePolicy(policy map[string]any, issues *[]ValidationIssue) {
	if policy == nil {
		*issues = append(*issues, issue("policy", "required", "policy must be an object"))
		return
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		*issues = append(*issues, issue("policy", "invalid_policy", "policy must be a JSON object"))
		return
	}
	if _, err := PolicyFromJSON(raw); err != nil {
		*issues = append(*issues, issue("policy", "invalid_policy", err.Error()))
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
