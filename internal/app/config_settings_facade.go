package app

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/crevissepartners/projmux/internal/services/settings"
)

type centralToggleJSON struct {
	Effective string `json:"effective"`
	Saved     string `json:"saved"`
	Source    string `json:"source"`
	Invalid   string `json:"invalid"`
}
type centralVisibilityJSON struct {
	Key       string `json:"key"`
	Effective string `json:"effective"`
	Saved     string `json:"saved"`
	Source    string `json:"source"`
	Invalid   string `json:"invalid"`
}

func (c *configCommand) runCentralSetting(route string, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet(route, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	value := fs.String("set", "", "store the central setting")
	var leaf string
	if route == "config statusbar-visibility" {
		fs.StringVar(&leaf, "key", "", "central visibility leaf")
	}
	if err := fs.Parse(args); err != nil {
		return usageRefusal(stderr, route, route+": "+err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(route + " does not accept positional arguments")
	}
	flags := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flags[f.Name] = true })
	u := settings.Update{}
	switch route {
	case "config resources":
		u.Key = "statusbar.resources"
		if flags["set"] {
			u.Resources = value
		}
	case "config ai-default-mode":
		u.Key = "ai.defaultMode"
		if flags["set"] {
			u.DefaultMode = value
		}
	case "config statusbar-visibility":
		u.Key = "statusbar.visibility"
		if flags["key"] != flags["set"] {
			return usageError("config statusbar-visibility requires --key and --set together")
		}
		if flags["set"] {
			u.Visibility = map[string]string{leaf: *value}
		}
	}
	service := c.centralPolicyService()
	var v settings.Value
	var err error
	if flags["set"] {
		v, err = service.Set(context.Background(), u)
	} else {
		v, err = service.Get(context.Background(), u.Key)
	}
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(stdout)
	switch route {
	case "config resources":
		r := v.Resources
		return encoder.Encode(struct {
			Key       string            `json:"key"`
			Resources centralToggleJSON `json:"resources"`
		}{v.Key, centralToggleJSON{r.Effective, r.Saved, r.Source, r.Invalid}})
	case "config ai-default-mode":
		return encoder.Encode(struct {
			Key         string `json:"key"`
			DefaultMode string `json:"defaultMode"`
		}{v.Key, v.DefaultMode})
	default:
		leaves := make([]centralVisibilityJSON, 0, len(v.Visibility))
		for _, v := range v.Visibility {
			leaves = append(leaves, centralVisibilityJSON{v.Key, v.Effective, v.Saved, v.Source, v.Invalid})
		}
		return encoder.Encode(struct {
			Key        string                  `json:"key"`
			Visibility []centralVisibilityJSON `json:"visibility"`
		}{v.Key, leaves})
	}
}
