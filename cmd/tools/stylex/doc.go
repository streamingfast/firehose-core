// Package stylex provides styles for the tools commands.
// This is a separate package to avoid import cycles between the tools commands.
//
// It defines a bunch of lipgloss styles that can be used across the tools commands to
// ensure a consistent look and feel. It will automatically disable styling if the output is not a TTY,
// so that the tools can be used in scripts without worrying about ANSI escape codes.
//
// You have nothing to do to activate that.
package stylex
