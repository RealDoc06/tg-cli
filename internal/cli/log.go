package cli

import (
	"encoding/json"
	"os"
	"slices"
	"time"

	"golang.org/x/term"

	"github.com/RealDoc06/tg-cli/internal/config"
)

// maxLog is the size at which the command log is rotated.
const maxLog = 1 << 20

// logEntry is one line of the command log.
type logEntry struct {
	Time    time.Time `json:"time"`
	Account string    `json:"account,omitempty"`
	Args    []string  `json:"args"`
	Exit    int       `json:"exit"`
	Error   string    `json:"error,omitempty"`
	Ms      int64     `json:"ms"`
	TTY     bool      `json:"tty"`
}

// logInvocation appends a command to log.jsonl in tg's directory, so the
// owner can see what was run, most usefully by an agent. It is a
// convenience, not an audit trail: TG_NO_LOG=1 turns it off. Failing to log
// never fails the command.
func logInvocation(account, command string, args []string, started time.Time, exit int, err error) {
	if os.Getenv("TG_NO_LOG") != "" || os.Getenv("TG_DEMO") != "" || len(args) == 0 {
		return
	}
	// Login flags carry the code and the password. When the command could
	// not even be told (a bad flag before it), err on the side of silence.
	login := command == "login" || (command == "" && slices.Contains(args, "login"))
	if login {
		args, err = []string{"login"}, nil
	}
	entry := logEntry{
		Time: started, Account: account, Args: args, Exit: exit, Ms: time.Since(started).Milliseconds(),
		TTY: term.IsTerminal(int(os.Stdout.Fd())),
	}
	if err != nil {
		entry.Error = err.Error()
	}
	line, jsonErr := json.Marshal(entry)
	if jsonErr != nil {
		return
	}

	path := config.Path("log.jsonl")
	if os.MkdirAll(config.Dir(), 0o700) != nil {
		return
	}
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxLog {
		rotate(path)
	}
	file, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}

// rotate moves a full log aside. It checks the size again under the lock:
// of two commands that both saw a full log, only the first may move it, or
// the second would replace the archive with a nearly empty file.
func rotate(path string) {
	unlock, err := config.Lock()
	if err != nil {
		return
	}
	defer unlock()
	if info, err := os.Stat(path); err == nil && info.Size() > maxLog {
		_ = os.Rename(path, path+".1")
	}
}
