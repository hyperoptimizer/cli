package cli

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Real PTY input catches wrappers that hide the native descriptor and leave
// keyboard prompts in canonical mode, where pressing y alone cannot submit.
func TestKeyboardConfirmation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY; Windows retains the native console descriptor")
	}
	t.Setenv("TERM", "xterm")
	for _, test := range []struct {
		name, input string
		want        bool
		canceled    bool
	}{
		{"default-no", "\r", false, false}, {"yes", "y", true, false}, {"ctrl-c", "\x03", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			master, slave, err := pty.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			defer slave.Close()
			if err := pty.Setsize(slave, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
				t.Fatal(err)
			}
			app, _, _ := testApp("http://localhost:1")
			app.In = slave
			sent := make(chan error, 1)
			time.AfterFunc(50*time.Millisecond, func() { _, err := master.Write([]byte(test.input)); sent <- err })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			open, err := app.confirmBrowser(ctx)
			if inputErr := <-sent; inputErr != nil {
				t.Fatal(inputErr)
			}
			if test.canceled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("not canceled: %v", err)
				}
			} else if err != nil || open != test.want {
				t.Fatalf("wrong response %v %v", open, err)
			}
		})
	}
}
