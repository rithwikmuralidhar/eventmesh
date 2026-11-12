package deliveries

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"eventmesh/internal/database/db"
	"eventmesh/internal/httpx"
)

// statsRange describes one selectable dashboard range.
type statsRange struct {
	unit    string // date_trunc unit: "hour" or "day"
	step    time.Duration
	buckets int
}

// The three ranges of the overview page. Statistics are computed with SQL
// aggregates at request time; there are no rollup tables.
var statsRanges = map[string]statsRange{
	"24h": {unit: "hour", step: time.Hour, buckets: 24},
	"7d":  {unit: "day", step: 24 * time.Hour, buckets: 7},
	"30d": {unit: "day", step: 24 * time.Hour, buckets: 30},
}

// Totals are the headline numbers for a range.
type Totals struct {
	Events     int64 `json:"events"`
	Deliveries int64 `json:"deliveries"`
	Delivered  int64 `json:"delivered"`
	DeadLetter int64 `json:"dead_letter"`
	Pending    int64 `json:"pending"`
	Cancelled  int64 `json:"cancelled"`
	// SuccessRate is delivered / (delivered + dead_letter): the share of
	// finished deliveries that succeeded. Null when nothing has finished.
	SuccessRate *float64 `json:"success_rate"`
}

// Bucket is one point of the delivery-volume series.
type Bucket struct {
	Start      time.Time `json:"start"`
	Total      int64     `json:"total"`
	Delivered  int64     `json:"delivered"`
	DeadLetter int64     `json:"dead_letter"`
}

// Failure is one recent dead-lettered delivery.
type Failure struct {
	ID                 uuid.UUID  `json:"id"`
	EventID            uuid.UUID  `json:"event_id"`
	EndpointID         uuid.UUID  `json:"endpoint_id"`
	EventType          string     `json:"event_type"`
	EndpointName       string     `json:"endpoint_name"`
	LastErrorCategory  *string    `json:"last_error_category"`
	LastResponseStatus *int32     `json:"last_response_status"`
	AttemptCount       int32      `json:"attempt_count"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	CreatedAt          time.Time  `json:"created_at"`
}

const recentFailureLimit = 10

// Stats handles GET /dashboard/stats?range=24h|7d|30d.
func (h *Handler) Stats(c *gin.Context) {
	if err := httpx.AllowQuery(c, "range"); err != nil {
		httpx.WriteError(c, err)
		return
	}
	rangeName := c.DefaultQuery("range", "24h")
	rng, ok := statsRanges[rangeName]
	if !ok {
		httpx.WriteError(c, httpx.BadRequest("range must be one of 24h, 7d, 30d."))
		return
	}
	ctx := c.Request.Context()
	userID := httpx.UserID(c)

	// The window starts at a bucket boundary so the first bucket is whole.
	now := time.Now().UTC()
	since := now.Truncate(rng.step).Add(-time.Duration(rng.buckets-1) * rng.step)

	totalsRow, err := h.queries.StatsTotals(ctx, db.StatsTotalsParams{UserID: userID, Since: since})
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("stats totals: %w", err))
		return
	}
	seriesRows, err := h.queries.StatsTimeSeries(ctx, db.StatsTimeSeriesParams{BucketUnit: rng.unit, UserID: userID, Since: since})
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("stats series: %w", err))
		return
	}
	failureRows, err := h.queries.RecentFailures(ctx, db.RecentFailuresParams{UserID: userID, Since: since, RowLimit: recentFailureLimit})
	if err != nil {
		httpx.WriteError(c, fmt.Errorf("recent failures: %w", err))
		return
	}

	totals := Totals{
		Events: totalsRow.TotalEvents, Deliveries: totalsRow.TotalDeliveries, Delivered: totalsRow.Delivered,
		DeadLetter: totalsRow.DeadLetter, Pending: totalsRow.Pending, Cancelled: totalsRow.Cancelled,
	}
	if finished := totals.Delivered + totals.DeadLetter; finished > 0 {
		rate := float64(totals.Delivered) / float64(finished)
		totals.SuccessRate = &rate
	}

	failures := make([]Failure, 0, len(failureRows))
	for _, f := range failureRows {
		failures = append(failures, Failure{
			ID: f.ID, EventID: f.EventID, EndpointID: f.EndpointID, EventType: f.EventType,
			EndpointName: f.EndpointName, LastErrorCategory: f.LastErrorCategory,
			LastResponseStatus: f.LastResponseStatus, AttemptCount: f.AttemptCount,
			LastAttemptAt: f.LastAttemptAt, CreatedAt: f.CreatedAt,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"range":           rangeName,
		"since":           since,
		"totals":          totals,
		"series":          fillSeries(since, rng, seriesRows),
		"recent_failures": failures,
	})
}

// fillSeries returns one bucket per step of the range. The SQL only returns
// buckets that contain deliveries; a chart needs the empty ones too.
func fillSeries(since time.Time, rng statsRange, rows []db.StatsTimeSeriesRow) []Bucket {
	byStart := make(map[int64]db.StatsTimeSeriesRow, len(rows))
	for _, r := range rows {
		byStart[r.Bucket.UTC().Unix()] = r
	}
	series := make([]Bucket, 0, rng.buckets)
	for i := 0; i < rng.buckets; i++ {
		start := since.Add(time.Duration(i) * rng.step)
		bucket := Bucket{Start: start}
		if r, ok := byStart[start.Unix()]; ok {
			bucket.Total, bucket.Delivered, bucket.DeadLetter = r.Total, r.Delivered, r.DeadLetter
		}
		series = append(series, bucket)
	}
	return series
}
