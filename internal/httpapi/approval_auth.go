package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/ruohao1/circular/internal/integrations"
)

func approvalHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func (a *api) approvalOrigin(w http.ResponseWriter, r *http.Request, required bool) bool {
	approvalHeaders(w)
	origin := r.Header.Get("Origin")
	if origin == "" && !required {
		return true
	}
	if origin != a.integrations.WebURL() && origin != a.integrations.APIURL() {
		problem(w, 403, "Use the private Circular review page for this action")
		return false
	}
	return true
}
func (a *api) approvalCookieName() string {
	if strings.HasPrefix(a.integrations.APIURL(), "https://") {
		return "__Host-circular_approval_session"
	}
	return "circular_approval_session"
}
func (a *api) approvalCookie(w http.ResponseWriter, name, value, path string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: age, HttpOnly: true, Secure: strings.HasPrefix(a.integrations.APIURL(), "https://"), SameSite: http.SameSiteLaxMode})
}
func approvalCSRF(secret string) string {
	hash := sha256.Sum256([]byte("circular-approval-csrf:" + secret))
	return hex.EncodeToString(hash[:])
}
func (a *api) approvalBrowser(w http.ResponseWriter, r *http.Request) (integrations.ApprovalUser, string, bool) {
	var out integrations.ApprovalUser
	if !a.approvalOrigin(w, r, true) {
		return out, "", false
	}
	cookie, err := r.Cookie(a.approvalCookieName())
	if err != nil {
		problem(w, 401, integrations.ErrApprovalSession.Error())
		return out, "", false
	}
	out, err = a.integrations.ApprovalSession(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, integrations.ErrApprovalSession) {
			problem(w, 401, err.Error())
		} else {
			integrationError(w, err)
		}
		return out, "", false
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Circular-CSRF")), []byte(approvalCSRF(cookie.Value))) != 1 {
		problem(w, 403, "The review session changed; reload the page")
		return out, "", false
	}
	return out, cookie.Value, true
}
func (a *api) approvalAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/approval-auth/github/start", func(w http.ResponseWriter, r *http.Request) {
		if !a.approvalOrigin(w, r, true) {
			return
		}
		var in struct {
			ReturnPath string `json:"return_path"`
		}
		if !proposalBody(w, r, &in, "return_path") {
			return
		}
		auth, err := a.integrations.BeginApprovalLogin(r.Context(), in.ReturnPath)
		if err != nil {
			if errors.Is(err, integrations.ErrCallback) {
				problem(w, 422, "Choose a Circular proposal review page")
			} else {
				integrationError(w, err)
			}
			return
		}
		a.approvalCookie(w, integrations.CookieName("approval", auth.State), auth.Browser, "/api/v1/approval-auth/github/callback", 600)
		respond(w, 200, map[string]string{"url": auth.URL})
	})
	mux.HandleFunc("GET /api/v1/approval-auth/github/callback", func(w http.ResponseWriter, r *http.Request) {
		approvalHeaders(w)
		state := r.URL.Query().Get("state")
		name := integrations.CookieName("approval", state)
		browser := ""
		if c, e := r.Cookie(name); e == nil {
			browser = c.Value
		}
		a.approvalCookie(w, name, "", "/api/v1/approval-auth/github/callback", -1)
		secret, _, path, err := a.integrations.CompleteApprovalLogin(r.Context(), state, browser, r.URL.Query().Get("code"))
		if err != nil {
			http.Redirect(w, r, a.integrations.WebURL()+"/proposals?login_error=1", http.StatusSeeOther)
			return
		}
		if old, e := r.Cookie(a.approvalCookieName()); e == nil {
			if e = a.integrations.EndApprovalSession(r.Context(), old.Value); e != nil {
				problem(w, 500, "Could not replace the previous session")
				return
			}
		}
		a.approvalCookie(w, a.approvalCookieName(), secret, "/", 1800)
		http.Redirect(w, r, a.integrations.WebURL()+path, http.StatusSeeOther)
	})
	mux.HandleFunc("GET /api/v1/approval-auth/session", func(w http.ResponseWriter, r *http.Request) {
		if !a.approvalOrigin(w, r, false) {
			return
		}
		configured, err := a.integrations.ApprovalLoginConfigured(r.Context())
		if integrationError(w, err) {
			return
		}
		var user *integrations.ApprovalUser
		csrf := ""
		if c, e := r.Cookie(a.approvalCookieName()); e == nil {
			value, e := a.integrations.ApprovalSession(r.Context(), c.Value)
			if e == nil {
				user = &value
				csrf = approvalCSRF(c.Value)
			} else if !errors.Is(e, integrations.ErrApprovalSession) {
				integrationError(w, e)
				return
			}
		}
		respond(w, 200, map[string]any{"configured": configured, "user": user, "csrf_token": csrf})
	})
	mux.HandleFunc("POST /api/v1/approval-auth/logout", func(w http.ResponseWriter, r *http.Request) {
		_, secret, ok := a.approvalBrowser(w, r)
		if !ok {
			return
		}
		if integrationError(w, a.integrations.EndApprovalSession(r.Context(), secret)) {
			return
		}
		a.approvalCookie(w, a.approvalCookieName(), "", "/", -1)
		respond(w, 200, map[string]bool{"signed_out": true})
	})
}
