package cmd

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const (
	// globalHelpFlag is the flag to use to display the full list of global flags in the
	// help output of commands that hide them by default.
	globalHelpFlag = "global-help"

	// globalHelpFlagAlias is the short, easier to type, form of [globalHelpFlag].
	globalHelpFlagAlias = "gh"

	// showGlobalFlagsAnnotation is the command annotation that forces the full list of
	// global flags to be printed, see [showGlobalFlagsInHelp].
	showGlobalFlagsAnnotation = "firehose-core/show-global-flags"
)

func init() {
	cobra.AddTemplateFunc("inheritedFlagSections", inheritedFlagSections)
}

// showGlobalFlagsInHelp marks the command as always printing the full list of global
// flags in its help output, instead of compacting them behind the --gh flag. It's meant
// for the few commands where knowing the global flags is part of using them, the root
// command and 'start' today.
func showGlobalFlagsInHelp(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}

	cmd.Annotations[showGlobalFlagsAnnotation] = "true"
}

// configureGlobalHelp registers the --global-help flag (and its --gh alias) on the root
// command and installs the usage template rendering the inherited flags sections.
//
// It must be called once every command has been added to the tree, the alias is
// installed as a normalization function which is propagated to children at call time
// only.
func configureGlobalHelp(root *cobra.Command) {
	root.PersistentFlags().Bool(globalHelpFlag, false, fmt.Sprintf("Show the global flags in the help output, --%s for short", globalHelpFlagAlias))
	root.PersistentFlags().MarkHidden(globalHelpFlag)

	root.SetUsageTemplate(usageTemplate)
	root.SetGlobalNormalizationFunc(func(_ *pflag.FlagSet, name string) pflag.NormalizedName {
		if name == globalHelpFlagAlias {
			name = globalHelpFlag
		}

		return pflag.NormalizedName(name)
	})
}

// expandGlobalHelpArgs makes '--gh' (and '--global-help') usable on their own by turning
// them into a help request. Doing it on the arguments rather than in a hook is what makes
// it work on commands that would otherwise reject the invocation while validating their
// arguments, which happens before any hook runs.
func expandGlobalHelpArgs(args []string) []string {
	for _, arg := range args {
		if arg == "--" {
			return args
		}

		if arg == "-h" || arg == "--help" {
			return args
		}
	}

	if !slices.ContainsFunc(args, isGlobalHelpArg) {
		return args
	}

	return append(slices.Clone(args), "--help")
}

func isGlobalHelpArg(arg string) bool {
	return arg == "--"+globalHelpFlagAlias || arg == "--"+globalHelpFlag
}

// inheritedFlagSections renders the help sections for the flags a command inherits from
// its ancestors, one section per ancestor defining some, the root command's own flags
// being rendered last under the well-known 'Global Flags' section.
//
// The global flags are big enough that printing them in full on every command drowns the
// command's own flags, they are compacted to a single '--gh' line unless the command opted
// in through [showGlobalFlagsInHelp] or the user asked for them.
func inheritedFlagSections(cmd *cobra.Command) string {
	if !cmd.HasAvailableInheritedFlags() {
		return ""
	}

	inherited := cmd.InheritedFlags()

	var ancestors []*cobra.Command
	for parent := cmd.Parent(); parent != nil; parent = parent.Parent() {
		ancestors = append(ancestors, parent)
	}

	// Nearest ancestor first so that a flag re-defined closer to the command is rendered
	// in the section of the command actually defining the value in use.
	claimed := map[string]bool{}
	flagSets := make([]*pflag.FlagSet, len(ancestors))
	for i, ancestor := range ancestors {
		flagSet := pflag.NewFlagSet(ancestor.Name(), pflag.ContinueOnError)
		ancestor.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
			if flag.Hidden || claimed[flag.Name] || inherited.Lookup(flag.Name) == nil {
				return
			}

			claimed[flag.Name] = true
			flagSet.AddFlag(flag)
		})

		flagSets[i] = flagSet
	}

	out := &strings.Builder{}

	// Root most ancestor first, the root command itself is rendered after all of them.
	for i := len(ancestors) - 2; i >= 0; i-- {
		writeFlagSection(out, sectionTitle(ancestors[i].Name()), flagSets[i].FlagUsages())
	}

	globalFlags := flagSets[len(flagSets)-1]
	if !globalFlags.HasAvailableFlags() {
		return out.String()
	}

	if showsGlobalFlags(cmd) {
		writeFlagSection(out, "Global", globalFlags.FlagUsages())
	} else {
		writeFlagSection(out, "Global", compactedGlobalFlagsUsage(countFlags(globalFlags)))
	}

	return out.String()
}

func showsGlobalFlags(cmd *cobra.Command) bool {
	if cmd.Annotations[showGlobalFlagsAnnotation] == "true" {
		return true
	}

	flag := cmd.Flags().Lookup(globalHelpFlag)

	return flag != nil && flag.Changed
}

// compactedGlobalFlagsUsage renders the single line standing for all the hidden global
// flags. It goes through a real flag set so that it lines up with the other sections.
func compactedGlobalFlagsUsage(hiddenCount int) string {
	flagSet := pflag.NewFlagSet("", pflag.ContinueOnError)
	flagSet.Bool(globalHelpFlagAlias, false, fmt.Sprintf("Show global flags help, %d global flags hidden", hiddenCount))

	return flagSet.FlagUsages()
}

func writeFlagSection(out *strings.Builder, title string, usages string) {
	usages = strings.TrimRight(usages, " \t\n")
	if usages == "" {
		return
	}

	fmt.Fprintf(out, "\n\n%s Flags:\n%s", title, usages)
}

func countFlags(flagSet *pflag.FlagSet) (count int) {
	flagSet.VisitAll(func(*pflag.Flag) { count++ })

	return count
}

func sectionTitle(commandName string) string {
	if commandName == "" {
		return commandName
	}

	return strings.ToUpper(commandName[0:1]) + commandName[1:]
}

// usageTemplate is Cobra's default usage template with the 'Global Flags' section
// replaced by our own inherited flags sections, see [inheritedFlagSections].
var usageTemplate = `Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if gt (len .Aliases) 0}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}{{$cmds := .Commands}}{{if eq (len .Groups) 0}}

Available Commands:{{range $cmds}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{else}}{{range $group := .Groups}}

{{.Title}}{{range $cmds}}{{if (and (eq .GroupID $group.ID) (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if not .AllChildCommandsHaveGroup}}

Additional Commands:{{range $cmds}}{{if (and (eq .GroupID "") (or .IsAvailableCommand (eq .Name "help")))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{inheritedFlagSections .}}{{if .HasHelpSubCommands}}

Additional help topics:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

Use "{{.CommandPath}} [command] --help" for more information about a command.{{end}}
`
