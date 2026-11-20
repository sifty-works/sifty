package inbox

import (
	"log/slog"
	"net/http"

	"github.com/sifty-works/sifty-server/internal/industry"
	"github.com/sifty-works/sifty-server/internal/server"
)

type IndustryItem struct {
	Name string `json:"name"`
}

type IndustriesResp struct {
	Industries []IndustryItem `json:"industries"`
}

func RegisterIndustryRoutes(mux *http.ServeMux, logger *slog.Logger) {
	server.Handle(mux, "GET /api/industries", logger, func(_ *http.Request, _ struct{}) (IndustriesResp, error) {
		names := industry.Names()
		items := make([]IndustryItem, len(names))
		for i, n := range names {
			items[i] = IndustryItem{Name: n}
		}
		return IndustriesResp{Industries: items}, nil
	})
}
