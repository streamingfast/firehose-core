package logs

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/mattn/go-isatty"
)

var (
	// Styling for output - colors only enabled if terminal is TTY
	isTTY = isatty.IsTerminal(os.Stdout.Fd())

	// Styles
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	headerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	labelStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	valueStyle   = lipgloss.NewStyle().Bold(true)
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // Yellow
	closedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // Green
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // Red
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	warnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("208")) // Orange
	separatorStr = "─"

	// Column indices for styling
	colStatus = 0
	colEnd    = 6
	colError  = 9
)

func init() {
	// Disable styling if not a TTY
	if !isTTY {
		titleStyle = lipgloss.NewStyle()
		headerStyle = lipgloss.NewStyle()
		labelStyle = lipgloss.NewStyle()
		valueStyle = lipgloss.NewStyle()
		activeStyle = lipgloss.NewStyle()
		closedStyle = lipgloss.NewStyle()
		errorStyle = lipgloss.NewStyle()
		dimStyle = lipgloss.NewStyle()
		warnStyle = lipgloss.NewStyle()
	}
}

// printConnectionsTable prints the connections table to stdout
func printConnectionsTable(result *CorrelationResult, userID, namespace string, startTime, endTime time.Time) {
	// Print header
	fmt.Println(titleStyle.Render("Substreams Connections"))
	fmt.Println(dimStyle.Render(strings.Repeat(separatorStr, 120)))
	fmt.Println()

	fmt.Printf("%s %s\n", labelStyle.Render("Organization:"), valueStyle.Render(userID))
	if namespace != "" {
		fmt.Printf("%s %s\n", labelStyle.Render("Namespace:"), valueStyle.Render(namespace))
	}
	duration := endTime.Sub(startTime)
	fmt.Printf("%s %s - %s (%s)\n",
		labelStyle.Render("Time range:"),
		valueStyle.Render(startTime.Local().Format("2006-01-02 15:04:05")),
		valueStyle.Render(endTime.Local().Format("2006-01-02 15:04:05")),
		valueStyle.Render(formatDuration(duration)),
	)
	fmt.Println(dimStyle.Render("Times shown in local timezone"))
	fmt.Println()

	if len(result.Connections) == 0 {
		fmt.Println(labelStyle.Render("No connections found"))
		fmt.Println()
		return
	}

	// Build rows and track status for each row
	rows := make([][]string, 0, len(result.Connections))
	rowStatuses := make([]ConnectionStatus, 0, len(result.Connections))

	activeCount, closedCount, errorCount := 0, 0, 0
	for _, conn := range result.Connections {
		status := conn.Status()
		rowStatuses = append(rowStatuses, status)

		switch status {
		case StatusActive:
			activeCount++
		case StatusClosed:
			closedCount++
		case StatusError:
			errorCount++
		}

		blocks := "-"
		errMsg := "-"
		durationStr := formatDuration(conn.Duration())

		if conn.Stats != nil {
			blocks = fmt.Sprintf("%d", conn.Stats.TotalBlocksProcessed)
			if conn.Stats.Error != "" {
				errMsg = truncateString(conn.Stats.Error, 30)
			}
		}

		network := extractNetwork(conn.Namespace)
		moduleDisplay := formatModuleWithHash(conn.OutputModule, conn.OutputModuleHash)
		startTimeStr := conn.Timestamp.Local().Format("15:04:05")
		endTimeStr := "-"
		if conn.Stats != nil {
			endTimeStr = conn.Stats.EndTimestamp.Local().Format("15:04:05")
		}

		rows = append(rows, []string{
			string(status),
			conn.TraceID,
			network,
			conn.IPAddress,
			moduleDisplay,
			startTimeStr,
			endTimeStr,
			durationStr,
			blocks,
			errMsg,
		})
	}

	// Create style function that applies colors based on row status and column
	styleFunc := func(row, col int) lipgloss.Style {
		// Base style with right padding for column spacing
		baseStyle := lipgloss.NewStyle().PaddingRight(2)

		// Header row
		if row == table.HeaderRow {
			return headerStyle.PaddingRight(2)
		}

		// Get the status for this row
		if row < len(rowStatuses) {
			status := rowStatuses[row]

			// Status column gets the status color
			if col == colStatus {
				switch status {
				case StatusActive:
					return activeStyle.PaddingRight(2)
				case StatusClosed:
					return closedStyle.PaddingRight(2)
				case StatusError:
					return errorStyle.PaddingRight(2)
				}
			}

			// End column - dim the "-" placeholder for active connections
			if col == colEnd && rows[row][colEnd] == "-" {
				return dimStyle.PaddingRight(2)
			}

			// Error column styling
			if col == colError {
				errVal := rows[row][colError]
				if errVal == "-" {
					// Dim the "-" placeholder
					return dimStyle.PaddingRight(2)
				}
				if isNormalDisconnect(errVal) {
					// Normal disconnects (context canceled) use regular style
					return baseStyle
				}
				// Real errors get red
				return errorStyle.PaddingRight(2)
			}
		}

		return baseStyle
	}

	// Build and render the table
	t := table.New().
		Headers("STATUS", "TRACE_ID", "NETWORK", "SOURCE_IP", "MODULE", "START", "END", "DURATION", "BLOCKS", "ERROR").
		Rows(rows...).
		StyleFunc(styleFunc).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderRow(false).
		BorderHeader(true).
		BorderStyle(dimStyle)

	fmt.Println(t.Render())

	// Print summary
	fmt.Println()
	fmt.Println(dimStyle.Render(strings.Repeat(separatorStr, 80)))
	fmt.Printf("Total: %s connections (%s active, %s closed, %s error), max concurrent: %s\n",
		valueStyle.Render(fmt.Sprintf("%d", len(result.Connections))),
		activeStyle.Render(fmt.Sprintf("%d", activeCount)),
		closedStyle.Render(fmt.Sprintf("%d", closedCount)),
		errorStyle.Render(fmt.Sprintf("%d", errorCount)),
		valueStyle.Render(fmt.Sprintf("%d", result.MaxConcurrent)),
	)

	// Print orphaned warning if any
	if result.OrphanedCount > 0 {
		fmt.Println()
		fmt.Printf("%s %s\n",
			warnStyle.Render("Warning:"),
			warnStyle.Render(fmt.Sprintf("%d orphaned stats logs found (stats without matching incoming request)", result.OrphanedCount)),
		)
	}

	fmt.Println()
}

// formatModuleWithHash formats module name with inline hash: "module_name (hash...)"
func formatModuleWithHash(moduleName, hash string) string {
	if hash == "" {
		return moduleName
	}
	shortHash := hash
	if len(hash) > 6 {
		shortHash = hash[:6] + "..."
	}
	return fmt.Sprintf("%s (%s)", moduleName, shortHash)
}

// extractNetwork extracts the network name from a kubernetes namespace
// e.g., "eth-mainnet-full" -> "eth-mainnet"
func extractNetwork(namespace string) string {
	if namespace == "" {
		return "-"
	}

	// Remove common suffixes
	for _, suffix := range []string{"-full", "-tier1", "-tier2", "-archive"} {
		if trimmed, found := strings.CutSuffix(namespace, suffix); found {
			return trimmed
		}
	}

	return namespace
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
