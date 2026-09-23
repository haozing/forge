package asset

import (
	"testing"
)

func TestValidateFieldsExternalSiteProbe(t *testing.T) {
	schemaBytes := []byte(`{"fields": [{"key": "site_name", "type": "string", "label": "Site Name", "required": true}, {"key": "site_url", "type": "string", "label": "Site URL", "required": true}, {"key": "category", "type": "enum", "label": "Category", "options": [{"label": "SEO & Keyword Tools", "value": "seo-tools"}, {"label": "Content Creation Resources", "value": "content-creation"}, {"label": "Monetization & SaaS Guides", "value": "monetization"}, {"label": "Operations & Compliance", "value": "operations"}, {"label": "Website Building Tools", "value": "website-building"}], "required": true}, {"key": "description", "type": "text", "label": "Short Description", "required": true}, {"key": "tested_date", "type": "date", "label": "Tested On"}, {"key": "agent_skill", "type": "text", "label": "Agent Skill"}, {"key": "submitter_site_name", "type": "string", "label": "Your Site Name"}, {"key": "submitter_site_url", "type": "string", "label": "Your Site URL"}, {"key": "contact_email", "type": "string", "label": "Contact Email", "required": true}, {"key": "submission_note", "type": "text", "label": "Submitter Note"}], "additional_properties": false}`)
	fields := map[string]any{
		"site_name":     "t",
		"site_url":      "https://x.example",
		"category":      "seo-tools",
		"description":   "d",
		"contact_email": "a@b.c",
	}
	if err := ValidateFields(schemaBytes, fields); err != nil {
		t.Fatalf("ValidateFields rejected a valid record: %v", err)
	}
}
