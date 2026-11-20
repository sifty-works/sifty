package main

import (
	"log/slog"
	"net/http"

	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/server"
)

type checkResp struct {
	State string `json:"state"`
}

type pauseReq struct {
	Paused bool `json:"paused"`
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

	server.Handle(mux, "PUT /api/models/pause", log, func(r *http.Request, req pauseReq) (struct{}, error) {
		if gate == nil {
			return struct{}{}, nil
		}
		return struct{}{}, gate.SetPaused(r.Context(), req.Paused)
	})
}
