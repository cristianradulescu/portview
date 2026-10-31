package tui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jesseduffield/gocui"
)

// clipboardTools are tried in order; each reads the text on stdin. They
// are run directly (no shell) with fixed arguments.
var clipboardTools = []struct {
	env  string // only tried when this variable is set
	name string
	args []string
}{
	{"WAYLAND_DISPLAY", "wl-copy", nil},
	{"DISPLAY", "xclip", []string{"-selection", "clipboard"}},
	{"DISPLAY", "xsel", []string{"--clipboard", "--input"}},
}

// Copy puts text on the clipboard and returns how: through wl-copy, xclip
// or xsel when one is available for the session, otherwise with the OSC 52
// escape sequence, which the terminal (and tmux with set-clipboard on)
// turns into a clipboard update. OSC 52 cannot report failure: terminals
// that do not support it ignore it.
func Copy(text string) (string, error) {
	for _, t := range clipboardTools {
		if os.Getenv(t.env) == "" {
			continue
		}
		path, err := exec.LookPath(t.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		cmd := exec.CommandContext(ctx, path, t.args...)
		cmd.Stdin = strings.NewReader(text)
		err = cmd.Run() // wl-copy forks to serve the clipboard and returns
		cancel()
		if err == nil {
			return t.name, nil
		}
	}
	if gocui.Screen == nil {
		return "", errors.New("no clipboard available")
	}
	gocui.Screen.SetClipboard([]byte(text))
	return "OSC 52", nil
}
