package slack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	appTokenFile = "app-token"
	botTokenFile = "bot-token"
)

// ErrNotSetUp means neither token file exists.
var ErrNotSetUp = errors.New("slack: not set up")

// Dir is where the server keeps the bot's tokens under dataDir.
func Dir(dataDir string) string { return filepath.Join(dataDir, "slack") }

// ReadTokens reads the app-level and bot tokens under dir, or returns
// ErrNotSetUp.
func ReadTokens(dir string) (app, bot string, err error) {
	app, appErr := readToken(filepath.Join(dir, appTokenFile))
	bot, botErr := readToken(filepath.Join(dir, botTokenFile))
	if errors.Is(appErr, os.ErrNotExist) && errors.Is(botErr, os.ErrNotExist) {
		return "", "", ErrNotSetUp
	}
	if err := errors.Join(appErr, botErr); err != nil {
		return "", "", fmt.Errorf("%w; run sudo aether-server slack setup again", err)
	}
	return app, bot, nil
}

func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("slack: read %s: %w", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// WriteTokens stores both tokens under dir, readable only by the server.
func WriteTokens(dir, app, bot string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("slack: create %s: %w", dir, err)
	}
	for name, token := range map[string]string{appTokenFile: app, botTokenFile: bot} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
			return fmt.Errorf("slack: write %s: %w", path, err)
		}
	}
	return nil
}

// RemoveTokens deletes the tokens under dir, and reports whether there
// were any.
func RemoveTokens(dir string) (bool, error) {
	removed := false
	for _, name := range []string{appTokenFile, botTokenFile} {
		err := os.Remove(filepath.Join(dir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("slack: %w", err)
		}
		removed = removed || err == nil
	}
	return removed, nil
}

// CheckTokens asks Slack whether app opens Socket Mode connections and
// bot calls the Web API, and returns the bot's name and workspace.
func CheckTokens(ctx context.Context, app, bot string) (name, team string, err error) {
	switch {
	case !strings.HasPrefix(app, "xapp-"):
		return "", "", errors.New("the app-level token starts with xapp-; generate one under Basic Information, App-Level Tokens, with the connections:write scope")
	case !strings.HasPrefix(bot, "xoxb-"):
		return "", "", errors.New("the bot token starts with xoxb-; copy the Bot User OAuth Token from Install App")
	}
	a := api{base: API, client: http.DefaultClient}
	var self identity
	if err := a.call(ctx, bot, "auth.test", nil, &self); err != nil {
		return "", "", fmt.Errorf("check the bot token: %w", err)
	}
	if err := a.call(ctx, app, "apps.connections.open", nil, nil); err != nil {
		return "", "", fmt.Errorf("check the app-level token: %w", err)
	}
	return self.User, self.Team, nil
}
