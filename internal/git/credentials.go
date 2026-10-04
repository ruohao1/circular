package git

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

func (l *Local) credentials(ctx context.Context, id uuid.UUID, cloneURL string) (map[string]string, error) {
	if l.config.Credential == nil {
		return nil, nil
	}
	token, err := l.config.Credential(ctx, id, cloneURL)
	if err != nil {
		return nil, failure(ErrAuthentication, id, "", -1, nil)
	}
	if token == "" {
		return nil, nil
	}
	u, err := url.Parse(cloneURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(token, "\r\n\x00") {
		return nil, failure(ErrAuthentication, id, "", -1, nil)
	}
	// Git's environment-backed configuration keeps credentials out of argv,
	// persisted remotes, and worktrees. The header matches this exact HTTPS URL;
	// redirects and interactive/ambient credential helpers are disabled.
	key := "http." + cloneURL + "."
	return map[string]string{
		"GIT_CONFIG_COUNT": "5",
		"GIT_CONFIG_KEY_0": "credential.helper", "GIT_CONFIG_VALUE_0": "",
		"GIT_CONFIG_KEY_1": "http.extraHeader", "GIT_CONFIG_VALUE_1": "",
		"GIT_CONFIG_KEY_2": key + "extraHeader", "GIT_CONFIG_VALUE_2": "",
		"GIT_CONFIG_KEY_3": key + "extraHeader", "GIT_CONFIG_VALUE_3": "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)),
		"GIT_CONFIG_KEY_4": key + "followRedirects", "GIT_CONFIG_VALUE_4": "false",
	}, nil
}
