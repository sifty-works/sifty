package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"

	"github.com/sifty-works/sifty-server/internal/apperr"
	"github.com/sifty-works/sifty-server/internal/config"
	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/server"
)

type checkResp struct {
	State string `json:"state"`
}

type modelsPatch struct {
	Work          map[string]string `json:"work,omitempty"`
	AllowPaidBulk *bool             `json:"allowPaidBulk,omitempty"`
	Paused        *bool             `json:"paused,omitempty"`
	DailyLimit    *int              `json:"dailyLimit,omitempty"`
}

type providerView struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Model string `json:"model"`
	Cost  string `json:"cost"`
	State string `json:"state"`
}

type workView struct {
	Kind     string   `json:"kind"`
	Provider string   `json:"provider"`
	Choices  []string `json:"choices"`
}

type modelsResp struct {
	Providers     []providerView `json:"providers"`
	Work          []workView     `json:"work"`
	AllowPaidBulk bool           `json:"allowPaidBulk"`
	Paused        bool           `json:"paused"`
	DailyLimit    int            `json:"dailyLimit"`
	TodayCalls    int            `json:"todayCalls"`
}

func modelsView(ctx context.Context, gate *models.Gate) modelsResp {
	out := modelsResp{Providers: []providerView{}, Work: []workView{}}
	if gate == nil {
		return out
	}
	cfg := gate.Store().Get()
	status := gate.Status()
	names := slices.Sorted(maps.Keys(cfg.Providers))
	if names == nil {
		names = []string{}
	}
	for _, name := range names {
		pc := cfg.Providers[name]
		out.Providers = append(out.Providers, providerView{
			Name: name, Kind: pc.Type, Model: pc.Model, Cost: pc.Cost,
			State: status.Providers[name].State,
		})
	}
	for _, kind := range config.WorkKinds {
		out.Work = append(out.Work, workView{Kind: kind, Provider: cfg.WorkProviderFor(kind), Choices: names})
	}
	out.AllowPaidBulk = cfg.AllowPaidBulk
	out.Paused = status.Paused
	out.DailyLimit = cfg.DailyModelLimit
	out.TodayCalls = gate.TodayBackgroundCalls(ctx)
	return out
}

func registerModelRoutes(mux *http.ServeMux, gate *models.Gate, log *slog.Logger) {
	server.Handle(mux, "POST /api/providers/{name}/check", log, func(r *http.Request, _ struct{}) (checkResp, error) {
		if gate == nil {
			return checkResp{State: "Offline"}, nil
		}
		name := r.PathValue("name")
		state, _ := gate.Check(r.Context(), name)
		if state == "" {
			state = "Offline"
		}
		return checkResp{State: state}, nil
	})

	server.Handle(mux, "GET /api/models", log, func(r *http.Request, _ struct{}) (modelsResp, error) {
		return modelsView(r.Context(), gate), nil
	})

	server.Handle(mux, "PATCH /api/models", log, func(r *http.Request, req modelsPatch) (modelsResp, error) {
		if gate == nil {
			return modelsView(r.Context(), gate), nil
		}
		if req.DailyLimit != nil && *req.DailyLimit < 0 {
			return modelsResp{}, apperr.Invalid{Message: "The daily limit of background model calls is 0 or more; 0 means no limit."}
		}
		if req.Work != nil || req.AllowPaidBulk != nil || req.DailyLimit != nil {
			err := gate.Store().Update(func(c *config.Config) error {
				for kind, provider := range req.Work {
					if !c.SetWorkProvider(kind, provider) {
						return apperr.Invalid{Message: fmt.Sprintf("There is no kind of model work called %q.", kind)}
					}
				}
				if req.AllowPaidBulk != nil {
					c.AllowPaidBulk = *req.AllowPaidBulk
				}
				if req.DailyLimit != nil {
					c.DailyModelLimit = *req.DailyLimit
				}
				return nil
			})
			if err != nil {
				return modelsResp{}, err
			}
		}
		if req.Paused != nil {
			if err := gate.SetPaused(r.Context(), *req.Paused); err != nil {
				return modelsResp{}, err
			}
		}
		return modelsView(r.Context(), gate), nil
	})
}
