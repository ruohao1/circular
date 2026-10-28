package integrations

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrApprovalSession = errors.New("sign in to Circular to approve or reject work")

type ApprovalUser struct {
	UserID    string    `json:"user_id"`
	Login     string    `json:"login"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) ApprovalCallbackURL() string {
	return s.config.APIURL + "/api/v1/approval-auth/github/callback"
}
func approvalReturnPath(path string) bool {
	u, e := url.Parse(path)
	if e != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	if u.Path == "/proposals" {
		return u.RawQuery == ""
	}
	id, err := uuid.Parse(strings.TrimPrefix(u.Path, "/proposals/"))
	if err != nil || id == uuid.Nil || u.Path != "/proposals/"+id.String() {
		return false
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil || len(q) > 1 {
		return false
	}
	if len(q) == 0 {
		return true
	}
	values := q["revision"]
	if len(values) != 1 {
		return false
	}
	n, e := strconv.ParseInt(values[0], 10, 64)
	return e == nil && n > 0 && n <= 9007199254740991 && strconv.FormatInt(n, 10) == values[0]
}
func (s *Service) ApprovalLoginConfigured(ctx context.Context) (bool, error) {
	if len(s.config.ApproverGitHubIDs) == 0 {
		return false, nil
	}
	current, e := s.resolve(ctx, "github")
	if e != nil {
		return false, e
	}
	return current.Configured("github"), nil
}
func (s *Service) BeginApprovalLogin(ctx context.Context, returnPath string) (Authorization, error) {
	var out Authorization
	if !approvalReturnPath(returnPath) {
		return out, ErrCallback
	}
	if len(s.config.ApproverGitHubIDs) == 0 {
		return out, ErrConfiguration
	}
	current, e := s.resolve(ctx, "github")
	if e != nil {
		return out, e
	}
	if !current.Configured("github") {
		return out, ErrConfiguration
	}
	out.State, out.Browser = randomValue(), randomValue()
	verifier := randomValue()
	encrypted, e := s.seal([]byte(verifier), "approval:"+digest(out.State))
	if e != nil {
		return out, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer rollback(ctx, tx)
	if _, e = tx.Exec(ctx, `DELETE FROM approval_oauth_states WHERE expires_at<=now()`); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `DELETE FROM approval_sessions WHERE expires_at<=now()`); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO approval_oauth_states(state_hash,browser_hash,verifier,app_client_id,return_path,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '10 minutes')`, digest(out.State), digest(out.Browser), encrypted, current.config.GitHub.ClientID, returnPath); e != nil {
		return out, e
	}
	if e = tx.Commit(ctx); e != nil {
		return out, e
	}
	challenge := sha256.Sum256([]byte(verifier))
	out.URL = current.authorizeURLForCallback("github", out.State, base64.RawURLEncoding.EncodeToString(challenge[:]), s.ApprovalCallbackURL())
	u, _ := url.Parse(out.URL)
	q := u.Query()
	q.Set("prompt", "select_account")
	q.Set("allow_signup", "false")
	u.RawQuery = q.Encode()
	out.URL = u.String()
	return out, nil
}
func (s *Service) CompleteApprovalLogin(ctx context.Context, state, browser, code string) (string, ApprovalUser, string, error) {
	var user ApprovalUser
	if len(state) != 43 || len(browser) != 43 || code == "" || len(code) > 8192 || len(s.config.ApproverGitHubIDs) == 0 {
		return "", user, "", ErrCallback
	}
	current, e := s.resolve(ctx, "github")
	if e != nil {
		return "", user, "", e
	}
	if !current.Configured("github") {
		return "", user, "", ErrConfiguration
	}
	var encrypted []byte
	var path string
	// Consume once before contacting GitHub. A failed exchange needs a new login.
	e = s.pool.QueryRow(ctx, `DELETE FROM approval_oauth_states WHERE state_hash=$1 AND browser_hash=$2 AND app_client_id=$3 AND expires_at>now() RETURNING verifier,return_path`, digest(state), digest(browser), current.config.GitHub.ClientID).Scan(&encrypted, &path)
	if errors.Is(e, pgx.ErrNoRows) {
		return "", user, "", ErrCallback
	}
	if e != nil {
		return "", user, "", e
	}
	if !approvalReturnPath(path) {
		return "", user, "", ErrCallback
	}
	verifier, e := s.open(encrypted, "approval:"+digest(state))
	if e != nil {
		return "", user, "", ErrCallback
	}
	token, e := current.tokenForCallback(ctx, "github", code, string(verifier), "", s.ApprovalCallbackURL())
	if e != nil {
		return "", user, "", ErrCallback
	}
	var identity struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Type  string `json:"type"`
	}
	if e = current.github(ctx, token.AccessToken, "/user", &identity); e != nil {
		return "", user, "", e
	}
	// Login tokens never enter the integration store and are not revoked: the grant
	// may also serve an existing integration connection.
	user.UserID = strconv.FormatInt(identity.ID, 10)
	user.Login = identity.Login
	if identity.ID <= 0 || identity.Type != "User" || identity.Login == "" || !slices.Contains(s.config.ApproverGitHubIDs, user.UserID) {
		return "", ApprovalUser{}, "", ErrAccess
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return "", user, "", e
	}
	defer rollback(ctx, tx)
	if e = lockApp(ctx, tx, "github"); e != nil {
		return "", user, "", e
	}
	latest, e := s.resolveWith(ctx, "github", tx)
	if e != nil {
		return "", user, "", e
	}
	if latest.config.GitHub.ClientID != current.config.GitHub.ClientID {
		return "", user, "", ErrCallback
	}
	secret := randomValue()
	user.ExpiresAt = time.Now().UTC().Add(30 * time.Minute)
	if _, e = tx.Exec(ctx, `INSERT INTO approval_sessions(token_hash,user_id,login,app_client_id,expires_at) VALUES($1,$2,$3,$4,$5)`, digest(secret), user.UserID, user.Login, current.config.GitHub.ClientID, user.ExpiresAt); e != nil {
		return "", user, "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", user, "", e
	}
	return secret, user, path, nil
}
func (s *Service) ApprovalSession(ctx context.Context, secret string) (ApprovalUser, error) {
	var out ApprovalUser
	if len(secret) != 43 {
		return out, ErrApprovalSession
	}
	current, e := s.resolve(ctx, "github")
	if e != nil {
		return out, e
	}
	if !current.Configured("github") {
		return out, ErrApprovalSession
	}
	e = s.pool.QueryRow(ctx, `SELECT user_id,login,expires_at FROM approval_sessions WHERE token_hash=$1 AND app_client_id=$2 AND expires_at>now()`, digest(secret), current.config.GitHub.ClientID).Scan(&out.UserID, &out.Login, &out.ExpiresAt)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrApprovalSession
	}
	if e != nil {
		return out, e
	}
	if !slices.Contains(s.config.ApproverGitHubIDs, out.UserID) {
		return ApprovalUser{}, ErrApprovalSession
	}
	return out, nil
}
func (s *Service) EndApprovalSession(ctx context.Context, secret string) error {
	_, e := s.pool.Exec(ctx, `DELETE FROM approval_sessions WHERE token_hash=$1`, digest(secret))
	return e
}
