package httpapi

import "net/http"

func currentUserFinal(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		principal, err := deps.SessionService.Authenticate(r.Context(), r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if deps.Store == nil || deps.Store.Pool == nil {
			writeJSON(w, http.StatusOK, map[string]any{"user_id": principal.UserID, "organization_id": principal.OrganizationID, "user_type": principal.UserType})
			return
		}
		var item struct {
			ID             string  `json:"user_id"`
			OrganizationID string  `json:"organization_id"`
			UserType       string  `json:"user_type"`
			DisplayName    string  `json:"display_name"`
			LoginName      string  `json:"login_name"`
			AvatarURL      *string `json:"avatar_url"`
		}
		err = deps.Store.Pool.QueryRow(r.Context(), `
			SELECT id::text, organization_id::text, user_type, display_name, COALESCE(login_name, ''), avatar_url
			FROM identity.users WHERE id = $1::uuid AND organization_id = $2::uuid
		`, principal.UserID, principal.OrganizationID).Scan(&item.ID, &item.OrganizationID, &item.UserType, &item.DisplayName, &item.LoginName, &item.AvatarURL)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "profile_load_failed")
			return
		}
		writeJSON(w, http.StatusOK, item)
	}
}
