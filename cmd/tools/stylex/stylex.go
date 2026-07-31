package stylex

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

var (
	// Styling for output - colors only enabled if terminal is TTY
	IsTTY = isatty.IsTerminal(os.Stdout.Fd())

	TitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	HeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	NoteStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Italic(true)
	DimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	LabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	ValueStyle = lipgloss.NewStyle().Bold(true)

	SuccessStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))  // Green
	WarnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // Orange
	ErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))   // Red

	ConnectionActiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // Yellow
	ConnectionClosedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // Green
	ConnectionOrphanStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // Magenta
	ConnectionErrorStyle  = ErrorStyle

	// Styles used when rendering raw log lines, kept desaturated so long log
	// dumps stay easy on the eyes
	LogLoggerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("73"))             // Cadet teal
	LogKeyStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")) // Off-white, emphasized
	LogValueStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))            // Grey

	separator = "─"
)

func init() {
	// Disable styling if not a TTY
	if !IsTTY {
		DisableColors()
	}
}

func DisableColors() {
	TitleStyle = lipgloss.NewStyle()
	HeaderStyle = lipgloss.NewStyle()
	NoteStyle = lipgloss.NewStyle()
	DimStyle = lipgloss.NewStyle()

	LabelStyle = lipgloss.NewStyle()
	ValueStyle = lipgloss.NewStyle()

	SuccessStyle = lipgloss.NewStyle()
	WarnStyle = lipgloss.NewStyle()
	ErrorStyle = lipgloss.NewStyle()

	ConnectionActiveStyle = lipgloss.NewStyle()
	ConnectionClosedStyle = lipgloss.NewStyle()
	ConnectionOrphanStyle = lipgloss.NewStyle()
	ConnectionErrorStyle = lipgloss.NewStyle()

	LogLoggerStyle = lipgloss.NewStyle()
	LogKeyStyle = lipgloss.NewStyle()
	LogValueStyle = lipgloss.NewStyle()
}

// Titlef is a helper function equivalent to Title.Render(fmt.Sprintf(...))
func Titlef(format string, a ...any) string {
	return TitleStyle.Render(fmt.Sprintf(format, a...))
}

// Title is a helper function equivalent to Title.Render(...)
func Title(str string) string {
	return TitleStyle.Render(str)
}

// Headerf is a helper function equivalent to Header.Render(fmt.Sprintf(...))
func Headerf(format string, a ...any) string {
	return HeaderStyle.Render(fmt.Sprintf(format, a...))
}

// Header is a helper function equivalent to Header.Render(...)
func Header(str string) string {
	return HeaderStyle.Render(str)
}

// Notef is a helper function equivalent to Note.Render(fmt.Sprintf(...))
func Notef(format string, a ...any) string {
	return NoteStyle.Render(fmt.Sprintf(format, a...))
}

// Note is a helper function equivalent to Note.Render(...)
func Note(str string) string {
	return NoteStyle.Render(str)
}

// Labelf is a helper function equivalent to Label.Render(fmt.Sprintf(...))
func Labelf(format string, a ...any) string {
	return LabelStyle.Render(fmt.Sprintf(format, a...))
}

// Label is a helper function equivalent to Label.Render(...)
func Label(str string) string {
	return LabelStyle.Render(str)
}

// Valuef is a helper function equivalent to Value.Render(fmt.Sprintf(...))
func Valuef(format string, a ...any) string {
	return ValueStyle.Render(fmt.Sprintf(format, a...))
}

// Value is a helper function equivalent to Value.Render(...)
func Value(str string) string {
	return ValueStyle.Render(str)
}

// Successf is a helper function equivalent to Success.Render(fmt.Sprintf(...))
func Successf(format string, a ...any) string {
	return SuccessStyle.Render(fmt.Sprintf(format, a...))
}

// Success is a helper function equivalent to Success.Render(...)
func Success(str string) string {
	return SuccessStyle.Render(str)
}

// Warnf is a helper function equivalent to Warn.Render(fmt.Sprintf(...))
func Warnf(format string, a ...any) string {
	return WarnStyle.Render(fmt.Sprintf(format, a...))
}

// Warn is a helper function equivalent to Warn.Render(...)
func Warn(str string) string {
	return WarnStyle.Render(str)
}

// Errorf is a helper function equivalent to Error.Render(fmt.Sprintf(...))
func Errorf(format string, a ...any) string {
	return ErrorStyle.Render(fmt.Sprintf(format, a...))
}

// Error is a helper function equivalent to Error.Render(...)
func Error(str string) string {
	return ErrorStyle.Render(str)
}

// Dimf is a helper function equivalent to Dim.Render(fmt.Sprintf(...))
func Dimf(format string, a ...any) string {
	return DimStyle.Render(fmt.Sprintf(format, a...))
}

// Dim is a helper function equivalent to Dim.Render(...)
func Dim(str string) string {
	return DimStyle.Render(str)
}

// ConnectionActivef is a helper function equivalent to ConnectionActive.Render(fmt.Sprintf(...))
func ConnectionActivef(format string, a ...any) string {
	return ConnectionActiveStyle.Render(fmt.Sprintf(format, a...))
}

// ConnectionActive is a helper function equivalent to ConnectionActive.Render(...)
func ConnectionActive(str string) string {
	return ConnectionActiveStyle.Render(str)
}

// ConnectionClosedf is a helper function equivalent to ConnectionClosed.Render(fmt.Sprintf(...))
func ConnectionClosedf(format string, a ...any) string {
	return ConnectionClosedStyle.Render(fmt.Sprintf(format, a...))
}

// ConnectionClosed is a helper function equivalent to ConnectionClosed.Render(...)
func ConnectionClosed(str string) string {
	return ConnectionClosedStyle.Render(str)
}

// ConnectionOrphanf is a helper function equivalent to ConnectionOrphan.Render(fmt.Sprintf(...))
func ConnectionOrphanf(format string, a ...any) string {
	return ConnectionOrphanStyle.Render(fmt.Sprintf(format, a...))
}

// ConnectionOrphan is a helper function equivalent to ConnectionOrphan.Render(...)
func ConnectionOrphan(str string) string {
	return ConnectionOrphanStyle.Render(str)
}

// ConnectionErrorf is a helper function equivalent to ConnectionError.Render(fmt.Sprintf(...))
func ConnectionErrorf(format string, a ...any) string {
	return ConnectionErrorStyle.Render(fmt.Sprintf(format, a...))
}

// ConnectionError is a helper function equivalent to ConnectionError.Render(...)
func ConnectionError(str string) string {
	return ConnectionErrorStyle.Render(str)
}

// LogLoggerf is a helper function equivalent to LogLogger.Render(fmt.Sprintf(...))
func LogLoggerf(format string, a ...any) string {
	return LogLoggerStyle.Render(fmt.Sprintf(format, a...))
}

// LogLogger is a helper function equivalent to LogLogger.Render(...)
func LogLogger(str string) string {
	return LogLoggerStyle.Render(str)
}

// LogKeyf is a helper function equivalent to LogKey.Render(fmt.Sprintf(...))
func LogKeyf(format string, a ...any) string {
	return LogKeyStyle.Render(fmt.Sprintf(format, a...))
}

// LogKey is a helper function equivalent to LogKey.Render(...)
func LogKey(str string) string {
	return LogKeyStyle.Render(str)
}

// LogValuef is a helper function equivalent to LogValue.Render(fmt.Sprintf(...))
func LogValuef(format string, a ...any) string {
	return LogValueStyle.Render(fmt.Sprintf(format, a...))
}

// LogValue is a helper function equivalent to LogValue.Render(...)
func LogValue(str string) string {
	return LogValueStyle.Render(str)
}

func Separator(count int) string {
	return strings.Repeat(separator, count)
}
