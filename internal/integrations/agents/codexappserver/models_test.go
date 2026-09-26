package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

type modelListRequester func(string, any, any) error

func (f modelListRequester) Request(_ context.Context, method string, params, result any) error {
	return f(method, params, result)
}

func TestListModelsOnFollowsOpaqueCursorAndUsesLaunchNames(t *testing.T) {
	t.Parallel()
	var cursors []string
	requester := modelListRequester(func(method string, params, result any) error {
		if method != "model/list" {
			t.Fatalf("method = %q", method)
		}
		raw, _ := json.Marshal(params)
		var p struct {
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		cursors = append(cursors, p.Cursor)
		var response string
		switch p.Cursor {
		case "":
			if string(raw) != `{}` {
				t.Fatalf("first params = %s", raw)
			}
			response = `{"data":[{"id":"opaque-id","displayName":"Fancy name","model":"gpt-6"}],"nextCursor":"opaque:page2"}`
		case "opaque:page2":
			response = `{"data":[{"model":"gpt-6-mini"}]}`
		default:
			t.Fatalf("unexpected cursor %q", p.Cursor)
		}
		return json.Unmarshal([]byte(response), result)
	})
	models, err := ListModelsOn(context.Background(), requester)
	if err != nil || !reflect.DeepEqual(models, []string{"gpt-6", "gpt-6-mini"}) ||
		!reflect.DeepEqual(cursors, []string{"", "opaque:page2"}) {
		t.Fatalf("models = %v, cursors = %v, err = %v", models, cursors, err)
	}
}

func TestListModelsOnRejectsRepeatedCursor(t *testing.T) {
	t.Parallel()
	requester := modelListRequester(func(_ string, _ any, result any) error {
		return json.Unmarshal([]byte(`{"data":[],"nextCursor":"same"}`), result)
	})
	if _, err := ListModelsOn(context.Background(), requester); !errors.Is(err, ErrProtocol) {
		t.Fatalf("err = %v, want protocol error", err)
	}
}
