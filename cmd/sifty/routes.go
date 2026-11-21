package main

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/sifty-works/sifty-server/internal/candidate"
	"github.com/sifty-works/sifty-server/internal/collect"
	"github.com/sifty-works/sifty-server/internal/db"
	"github.com/sifty-works/sifty-server/internal/fit"
	"github.com/sifty-works/sifty-server/internal/inbox"
	"github.com/sifty-works/sifty-server/internal/models"
	"github.com/sifty-works/sifty-server/internal/server"
)

func registerAllRoutes(
	mux *http.ServeMux,
	logger *slog.Logger,
	gate *models.Gate,
	database *db.DB,
	now func() time.Time,
	collectActive func() bool,
	sourceNames func() map[string]string,
	rules func() fit.Rules,
	recomputeFit func(),
	filesDir string,
	col *collect.Collector,
) {
	server.Handle(mux, "POST /api/providers/{name}/check", logger, models.CheckProviderHandler(gate))
	server.Handle(mux, "GET /api/models", logger, models.ModelsHandler(gate))
	server.Handle(mux, "PATCH /api/models", logger, models.PatchModelsHandler(gate))
	inbox.RegisterRoutes(mux, database, now, logger, collectActive, sourceNames, rules, recomputeFit)
	inbox.RegisterIndustryRoutes(mux, logger)
	candidate.RegisterRoutes(mux, database, filesDir, logger, recomputeFit)
	col.RegisterRoutes(mux)
}
