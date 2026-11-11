package ginweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/calendar"
	"github.com/pfisterer/cloud-self-service-golib/page"
)

func TestPageRequestAndPeriod(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	r := gin.New()
	r.GET("/list", func(c *gin.Context) {
		req, ok := PageRequest(c)
		if !ok {
			return
		}
		c.JSON(http.StatusOK, page.Of([]int{1, 2, 3}, req))
	})
	r.GET("/report", func(c *gin.Context) {
		p, ok := Period(c, calendar.Report(366), now)
		if !ok {
			return
		}
		c.String(http.StatusOK, p.From.String()+".."+p.To.String())
	})
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"/list?limit=1&offset=1", 200, `{"items":[2],"total":3,"limit":1,"offset":1}`},
		{"/list?limit=abc", 400, `"code":"invalid"`},
		{"/report?to=2026-06-30", 200, "2026-06-01..2026-06-30"},
		{"/report?from=2026-12-01&to=2026-11-01", 400, "liegt nach"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
}
