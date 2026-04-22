package render

type SessionRow struct {
	Label string
	Value string
}

func BuildSessionRows(sessionNames []string) []SessionRow {
	rows := make([]SessionRow, 0, len(sessionNames))
	for _, sessionName := range sessionNames {
		label := sanitizeCell(sessionName)
		rows = append(rows, SessionRow{
			Label: label,
			Value: label,
		})
	}
	return rows
}
