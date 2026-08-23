package app

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func renderAgentProgress(progress coremetadata.AgentProgress, now time.Time, locale i18n.Locale, maxCells int) string {
	if progress.IsZero() {
		return ""
	}
	parts := []string{localizeText(locale, "agent.progress.working", "Working")}
	if progress.PlanTotal > 0 || progress.PlanTruncated {
		total := strconv.Itoa(int(progress.PlanTotal))
		if progress.PlanTruncated {
			total += "+"
		}
		parts = append(parts, localizeText(locale, "agent.progress.plan", "plan")+" "+strconv.Itoa(int(progress.PlanCompleted))+"/"+total)
	}
	if progress.ChangedFiles > 0 || progress.FilesTruncated {
		files := strconv.Itoa(int(progress.ChangedFiles))
		if progress.FilesTruncated {
			files += "+"
		}
		parts = append(parts, localizeText(locale, "agent.progress.files", "files")+" "+files)
	}
	if progress.ActiveItemCount > 1 {
		parts = append(parts, localizeText(locale, "agent.progress.items", "items")+" "+strconv.Itoa(int(progress.ActiveItemCount)))
	}
	if progress.Activity != "" {
		parts = append(parts, string(progress.Activity))
	}
	if !progress.StartedAt.IsZero() && !now.IsZero() {
		elapsed := max(now.Sub(progress.StartedAt), 0)
		parts = append(parts, formatProgressElapsed(elapsed))
	}
	line := strings.Join(parts, " · ")
	if maxCells > 0 {
		line = i18n.TruncateTerminalCells(line, maxCells)
	}
	return line
}

func formatProgressElapsed(elapsed time.Duration) string {
	seconds := int64(elapsed / time.Second)
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	seconds %= 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}
