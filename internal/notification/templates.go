package notification

// templates.go — the plain-text template renderer used by the delivery
// worker. Payloads are produced by the domain services and arrive decrypted;
// only the trusted PUBLIC_APP_BASE_URL link and the organization name are
// rendered, never raw tokens (they remain inside the link).

import "fmt"

// TextTemplateRenderer renders the two phase 1 templates as plain text.
type TextTemplateRenderer struct{}

// NewTextTemplateRenderer builds the worker's renderer.
func NewTextTemplateRenderer() TextTemplateRenderer { return TextTemplateRenderer{} }

// Render turns a decrypted payload into a sendable message. Unknown templates
// fail terminally so the delivery is marked template_invalid instead of
// sending an empty mail.
func (r TextTemplateRenderer) Render(template, organizationName string, payload map[string]any) (Message, error) {
	link := stringFrom(payload["link"])
	if link == "" {
		return Message{}, fmt.Errorf("payload is missing the trusted link")
	}
	subject := organizationName
	bodyOrganization := organizationName
	if bodyOrganization == "" {
		bodyOrganization = "your organization"
	}
	switch template {
	case TemplateOrganizationInvitation:
		if subject != "" {
			subject = "You are invited to join " + organizationName
		} else {
			subject = "You are invited to join"
		}
		body := "Hello,\n\n" +
			"You have been invited to join " + bodyOrganization
		if workspace := stringFrom(payload["workspace_name"]); workspace != "" {
			body += ", workspace \"" + workspace + "\""
		}
		body += ".\n\nAccept your invitation: " + link + "\n\n" +
			"This link expires when the invitation expires. If you did not expect it, ignore this email.\n"
		return Message{Subject: subject, Body: body}, nil
	case TemplatePasswordReset:
		if subject != "" {
			subject = "Reset your " + organizationName + " password"
		} else {
			subject = "Reset your password"
		}
		body := "Hello,\n\n" +
			"A password reset was requested for your " + bodyOrganization + " account.\n\n" +
			"Reset your password: " + link + "\n\n" +
			"The link is valid for 30 minutes. If you did not request a reset, ignore this email.\n"
		return Message{Subject: subject, Body: body}, nil
	default:
		return Message{}, fmt.Errorf("unsupported email template %q", template)
	}
}
