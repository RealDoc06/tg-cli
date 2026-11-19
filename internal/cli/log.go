package cli

import (
	"encoding/json"
	"os"
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

// logInvocation appends a command to log.jsonl next to the session: a local
// record of what was done to the account, most useful when the caller was an
// agent. TG_NO_LOG=1 turns it off. Failing to log never fails the command.
func logInvocation(account string, args []string, started time.Time, exit int, err error) {
	if os.Getenv("TG_NO_LOG") != "" || os.Getenv("TG_DEMO") != "" || len(args) == 0 {
		return
	}
	if args[0] == "login" {
		args = args[:1] // its flags can carry the login code and password
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
	if info, statErr := os.Stat(path); statErr == nil && info.Size() > maxLog {
		_ = os.Rename(path, path+".1")
	}
	file, openErr := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(line, '\n'))
}
