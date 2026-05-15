package mux

import (
	"context"
	"strings"
)

// FieldDelimiter is the default delimiter for structured tmux format rows.
// It is intentionally outside ordinary user text so pane titles and topics are
// less likely to collide with the parser.
const FieldDelimiter = "\x1f"

// ListPanesOptions describes a structured `list-panes -F` read.
type ListPanesOptions struct {
	All              bool
	Target           string
	Formats          []string
	Delimiter        string
	AllowExtraFields bool
}

// ListWindowsOptions describes a structured `list-windows -F` read.
type ListWindowsOptions struct {
	Target           string
	Formats          []string
	Delimiter        string
	AllowExtraFields bool
}

// FormatRowsOptions controls parsing for tmux `-F`/`display-message -p`
// output built from a fixed field list.
type FormatRowsOptions struct {
	Delimiter        string
	FieldCount       int
	AllowExtraFields bool
}

// ListPanes executes `tmux list-panes -F` and parses fixed-width rows.
func ListPanes(ctx context.Context, opts ListPanesOptions) ([][]string, error) {
	return DefaultRunner().ListPanes(ctx, opts)
}

// ListWindows executes `tmux list-windows -F` and parses fixed-width rows.
func ListWindows(ctx context.Context, opts ListWindowsOptions) ([][]string, error) {
	return DefaultRunner().ListWindows(ctx, opts)
}

// DisplayPaneFields executes `tmux display-message -p` for one pane target and
// parses the result as a fixed field row.
func DisplayPaneFields(ctx context.Context, target string, formats ...string) ([]string, error) {
	return DefaultRunner().DisplayPaneFields(ctx, target, formats...)
}

// ListPanes executes `tmux list-panes -F` and parses fixed-width rows.
func (r Runner) ListPanes(ctx context.Context, opts ListPanesOptions) ([][]string, error) {
	if len(opts.Formats) == 0 {
		return nil, nil
	}
	delimiter := formatDelimiter(opts.Delimiter)
	args := []string{"list-panes"}
	if opts.All {
		args = append(args, "-a")
	}
	args = appendPaneTargetArgs(args, opts.Target)
	args = append(args, "-F", JoinFormats(delimiter, opts.Formats...))
	out, err := r.Read(ctx, args...)
	if err != nil {
		return nil, err
	}
	return ParseFormatRows(out, FormatRowsOptions{
		Delimiter:        delimiter,
		FieldCount:       len(opts.Formats),
		AllowExtraFields: opts.AllowExtraFields,
	}), nil
}

// ListWindows executes `tmux list-windows -F` and parses fixed-width rows.
func (r Runner) ListWindows(ctx context.Context, opts ListWindowsOptions) ([][]string, error) {
	if len(opts.Formats) == 0 {
		return nil, nil
	}
	delimiter := formatDelimiter(opts.Delimiter)
	args := []string{"list-windows"}
	args = appendPaneTargetArgs(args, opts.Target)
	args = append(args, "-F", JoinFormats(delimiter, opts.Formats...))
	out, err := r.Read(ctx, args...)
	if err != nil {
		return nil, err
	}
	return ParseFormatRows(out, FormatRowsOptions{
		Delimiter:        delimiter,
		FieldCount:       len(opts.Formats),
		AllowExtraFields: opts.AllowExtraFields,
	}), nil
}

// DisplayPaneFields executes `tmux display-message -p` for one pane target and
// parses the result as a fixed field row.
func (r Runner) DisplayPaneFields(ctx context.Context, target string, formats ...string) ([]string, error) {
	if len(formats) == 0 {
		return nil, nil
	}
	out, err := r.DisplayMessage(ctx, DisplayMessageOptions{
		Target: target,
		Format: JoinFormats(FieldDelimiter, formats...),
	})
	if err != nil {
		return nil, err
	}
	rows := ParseFormatRows(out, FormatRowsOptions{
		Delimiter:  FieldDelimiter,
		FieldCount: len(formats),
	})
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

// ParseFormatRows parses fixed-field tmux format output. Blank rows and
// malformed rows are skipped. Fields are whitespace-trimmed; accepted rows with
// extra delimiter splits are truncated to the requested field count only when
// AllowExtraFields is true.
func ParseFormatRows(output []byte, opts FormatRowsOptions) [][]string {
	if opts.FieldCount <= 0 {
		return nil
	}
	delimiter := formatDelimiter(opts.Delimiter)
	lines := strings.Split(strings.TrimRight(string(output), "\r\n"), "\n")
	rows := make([][]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := splitFormatFields(line, delimiter)
		if len(fields) < opts.FieldCount {
			continue
		}
		if len(fields) > opts.FieldCount {
			if !opts.AllowExtraFields {
				continue
			}
			fields = fields[:opts.FieldCount]
		}
		row := make([]string, len(fields))
		for i, field := range fields {
			row[i] = strings.TrimSpace(field)
		}
		rows = append(rows, row)
	}
	return rows
}

func splitFormatFields(raw, delimiter string) []string {
	if delimiter == FieldDelimiter && !strings.Contains(raw, FieldDelimiter) {
		return strings.Split(raw, "\\037")
	}
	return strings.Split(raw, delimiter)
}

func formatDelimiter(delimiter string) string {
	if delimiter == "" {
		return FieldDelimiter
	}
	return delimiter
}
