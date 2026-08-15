package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandGlobalHelpArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected []string
	}{
		{"no args", nil, nil},
		{"no global help", []string{"tools", "print"}, []string{"tools", "print"}},
		{"alias alone", []string{"tools", "print", "--gh"}, []string{"tools", "print", "--gh", "--help"}},
		{"long form alone", []string{"tools", "--global-help"}, []string{"tools", "--global-help", "--help"}},
		{"already asking for help", []string{"tools", "--gh", "-h"}, []string{"tools", "--gh", "-h"}},
		{"already asking for long help", []string{"tools", "--help", "--gh"}, []string{"tools", "--help", "--gh"}},
		{"after arguments terminator", []string{"tools", "--", "--gh"}, []string{"tools", "--", "--gh"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, expandGlobalHelpArgs(tt.args))
		})
	}
}

func TestHelpFlagSections(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name: "root command shows global flags as its own flags",
			args: []string{"-h"},
			expected: `
Flags:
  -d, --data-dir string   Path to data storage
  -h, --help              help for test
      --shift-ports int   Shift all ports

Use "test [command] --help" for more information about a command.
`,
		},
		{
			name: "command opting in shows the full global flags",
			args: []string{"start", "-h"},
			expected: `
Flags:
  -h, --help   help for start

Global Flags:
  -d, --data-dir string   Path to data storage
      --shift-ports int   Shift all ports
`,
		},
		{
			name: "nested command compacts the global flags only",
			args: []string{"tools", "print", "-h"},
			expected: `
Flags:
  -h, --help           help for print
      --transactions   Print transactions

Tools Flags:
  -o, --output string   Output printer to use

Global Flags:
      --gh   Show global flags help, 2 global flags hidden
`,
		},
		{
			name: "nested command with --gh shows the full global flags",
			args: []string{"tools", "print", "-h", "--gh"},
			expected: `
Flags:
  -h, --help           help for print
      --transactions   Print transactions

Tools Flags:
  -o, --output string   Output printer to use

Global Flags:
  -d, --data-dir string   Path to data storage
      --shift-ports int   Shift all ports
`,
		},
		{
			name: "nested command with --global-help shows the full global flags",
			args: []string{"tools", "print", "--global-help"},
			expected: `
Flags:
  -h, --help           help for print
      --transactions   Print transactions

Tools Flags:
  -o, --output string   Output printer to use

Global Flags:
  -d, --data-dir string   Path to data storage
      --shift-ports int   Shift all ports
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := runTestCommandHelp(t, tt.args)
			assert.Equal(t, tt.expected, flagsBlockOf(out), "Full help output was:\n%s", out)
		})
	}
}

// flagsBlockOf extracts everything from the 'Flags:' section onward so that tests
// are not sensitive to the parts of the help output we do not customize.
func flagsBlockOf(out string) string {
	index := bytes.Index([]byte(out), []byte("\nFlags:\n"))
	if index == -1 {
		return out
	}

	return out[index:]
}

func runTestCommandHelp(t *testing.T, args []string) string {
	t.Helper()

	root := &cobra.Command{Use: "test"}
	root.PersistentFlags().StringP("data-dir", "d", "", "Path to data storage")
	root.PersistentFlags().Int("shift-ports", 0, "Shift all ports")

	startCmd := &cobra.Command{Use: "start", Run: func(*cobra.Command, []string) {}}
	showGlobalFlagsInHelp(startCmd)
	root.AddCommand(startCmd)

	toolsCmd := &cobra.Command{Use: "tools"}
	toolsCmd.PersistentFlags().StringP("output", "o", "", "Output printer to use")
	root.AddCommand(toolsCmd)

	printCmd := &cobra.Command{Use: "print", Run: func(*cobra.Command, []string) {}}
	printCmd.Flags().Bool("transactions", false, "Print transactions")
	toolsCmd.AddCommand(printCmd)

	configureGlobalHelp(root)

	buffer := bytes.NewBuffer(nil)
	root.SetOut(buffer)
	root.SetErr(buffer)
	root.SetArgs(expandGlobalHelpArgs(args))

	require.NoError(t, root.Execute())

	return buffer.String()
}
