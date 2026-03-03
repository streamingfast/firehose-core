package substreams

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
	"github.com/streamingfast/firehose-core/cmd/tools/stylex"
	"github.com/streamingfast/firehose-core/cmd/tools/substreams/logs"
)

var (
	// Column indices for styling
	colStatus = 0
	colStart  = 5
	colEnd    = 6
	colError  = 9
)

// printConnectionsTable prints the connections table to stdout
func printConnectionsTable(result *logs.CorrelationResult, userID, namespace string, startTime, endTime time.Time) {
	if len(result.Connections) == 0 {
		fmt.Println(stylex.Label("No connections found"))
		fmt.Println()
		return
	}

	// Build rows and track status for each row
	rows := make([][]string, 0, len(result.Connections))
	rowStatuses := make([]logs.ConnectionStatus, 0, len(result.Connections))

	activeCount, closedCount, errorCount, orphanCount := 0, 0, 0, 0
	for _, conn := range result.Connections {
		status := conn.Status()
		rowStatuses = append(rowStatuses, status)

		switch status {
		case logs.StatusActive:
			activeCount++
		case logs.StatusClosed:
			closedCount++
		case logs.StatusError:
			errorCount++
		case logs.StatusOrphan:
			orphanCount++
		}

		blocks := "-"
		errMsg := "-"
		durationStr := "-"

		if conn.Stats != nil {
			blocks = fmt.Sprintf("%d", conn.Stats.TotalBlocksProcessed)
			if conn.Stats.Error != "" {
				errMsg = truncateString(conn.Stats.Error, 30)
			}
			// For orphans, use the duration from stats (parallel_duration)
			// For normal connections, calculate from start/end timestamps
			if conn.IsOrphan && conn.Stats.Duration > 0 {
				durationStr = formatDuration(conn.Stats.Duration)
			} else if !conn.IsOrphan {
				durationStr = formatDuration(conn.Duration())
			}
		} else if !conn.IsOrphan {
			// Active connection - show duration since start
			durationStr = formatDuration(conn.Duration())
		}

		network := extractNetwork(conn.Namespace)
		moduleDisplay := formatModuleWithHash(conn.OutputModule, conn.OutputModuleHash)

		// Show "?" for orphan start time since we don't know it
		startTimeStr := conn.Timestamp.Local().Format("15:04:05")
		if conn.IsOrphan {
			startTimeStr = "?"
		}

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
			return stylex.HeaderStyle.PaddingRight(2)
		}

		// Get the status for this row
		if row < len(rowStatuses) {
			status := rowStatuses[row]

			// Status column gets the status color
			if col == colStatus {
				switch status {
				case logs.StatusActive:
					return stylex.ConnectionActiveStyle.PaddingRight(2)
				case logs.StatusClosed:
					return stylex.ConnectionClosedStyle.PaddingRight(2)
				case logs.StatusError:
					return stylex.ErrorStyle.PaddingRight(2)
				case logs.StatusOrphan:
					return stylex.ConnectionOrphanStyle.PaddingRight(2)
				}
			}

			// Start column - dim the "?" for orphan connections
			if col == colStart && rows[row][colStart] == "?" {
				return stylex.DimStyle.PaddingRight(2)
			}

			// End column - dim the "-" placeholder for active connections
			if col == colEnd && rows[row][colEnd] == "-" {
				return stylex.DimStyle.PaddingRight(2)
			}

			// Error column styling
			if col == colError {
				errVal := rows[row][colError]
				if errVal == "-" {
					// Dim the "-" placeholder
					return stylex.DimStyle.PaddingRight(2)
				}
				if logs.IsNormalDisconnect(errVal) {
					// Normal disconnects (context canceled) use regular style
					return baseStyle
				}
				// Real errors get red
				return stylex.ErrorStyle.PaddingRight(2)
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
		BorderStyle(stylex.DimStyle)

	fmt.Println(t.Render())

	// Print summary
	fmt.Println()
	fmt.Println(stylex.Dim(stylex.Separator(80)))
	fmt.Printf("Total: %s connections (%s active, %s closed, %s error, %s orphan), max concurrent: %s\n",
		stylex.Valuef("%d", len(result.Connections)),
		stylex.ConnectionActivef("%d", activeCount),
		stylex.ConnectionClosedf("%d", closedCount),
		stylex.ConnectionErrorf("%d", errorCount),
		stylex.ConnectionOrphanf("%d", orphanCount),
		stylex.Valuef("%d", result.MaxConcurrent),
	)
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

func localTimezoneDisplay() string {
	name, offset := time.Now().Local().Zone()

	hours := offset / 3600
	if hours == 0 {
		return name
	}

	minutes := (offset % 3600) / 60
	if minutes == 0 {
		return fmt.Sprintf("%s | UTC%+d", name, hours)
	}

	return fmt.Sprintf("%s | UTC%+d:%02d", name, hours, minutes)
}
