package cli

import "golang.org/x/term"

func terminalStream(stream any) bool {
	file, ok := stream.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(int(file.Fd()))
}
func (a *App) dumbTerminal() bool {
	value, _ := a.Getenv("TERM")
	return value == "dumb"
}
func (a *App) liveOutput() bool {
	return terminalStream(a.Out) && !a.dumbTerminal()
}
