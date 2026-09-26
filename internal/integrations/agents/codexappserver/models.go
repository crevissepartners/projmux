package codexappserver

import (
	"context"
	"fmt"
	"strings"
)

// ListModelsOn reads every visible model from an initialized app-server
// connection. Model is the launch name; id and displayName are catalog
// metadata and are not accepted as substitutes for it.
func ListModelsOn(ctx context.Context, requester Requester) ([]string, error) {
	models := make([]string, 0)
	seen := make(map[string]bool)
	var cursor string
	for range 1000 {
		params := struct {
			Cursor string `json:"cursor,omitempty"`
		}{Cursor: cursor}
		var page struct {
			Data []struct {
				Model string `json:"model"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := requester.Request(ctx, methodModelList, params, &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, fmt.Errorf("%w: model/list omitted data", ErrProtocol)
		}
		for _, entry := range page.Data {
			if strings.TrimSpace(entry.Model) == "" {
				return nil, fmt.Errorf("%w: model/list returned an empty model", ErrProtocol)
			}
			models = append(models, entry.Model)
		}
		if page.NextCursor == "" {
			return models, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("%w: model/list repeated a cursor", ErrProtocol)
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("%w: model/list exceeded 1000 pages", ErrProtocol)
}
