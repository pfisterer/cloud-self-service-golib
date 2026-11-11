package ginweb

import (
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/apierr"
	"github.com/pfisterer/cloud-self-service-golib/calendar"
	"github.com/pfisterer/cloud-self-service-golib/page"
)

// PageRequest reads the query parameters limit and offset (page.FromQuery). Bounds that do not parse are answered with 400 and ok is false.
func PageRequest(c *gin.Context) (r page.Request, ok bool) {
	r, err := page.FromQuery(c.Request.URL.Query())
	if err != nil {
		abortWith(c, err)
		return page.Request{}, false
	}
	return r, true
}

// Period reads the query parameters from and to by rule at now (calendar.Rule.Parse). A period the rule refuses is answered with 400 and ok is false.
func Period(c *gin.Context, rule calendar.Rule, now time.Time) (p calendar.Period, ok bool) {
	p, err := rule.Parse(c.Query("from"), c.Query("to"), now)
	if err != nil {
		abortWith(c, err)
		return calendar.Period{}, false
	}
	return p, true
}

func abortWith(c *gin.Context, err error) {
	if e, ok := errors.AsType[*apierr.Error](err); ok {
		AbortError(c, e)
		return
	}
	AbortError(c, apierr.Internal.WithCause(err))
}
