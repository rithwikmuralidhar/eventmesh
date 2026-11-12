package deliveries

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The expected table is written out independently of allowedTransitions, as a
// full 6x6 matrix, so that an accidental edit to the map is caught.
func TestTransitionMatrix(t *testing.T) {
	type pair struct{ from, to Status }
	allowed := map[pair]bool{
		{StatusPending, StatusProcessing}:        true,
		{StatusPending, StatusCancelled}:         true,
		{StatusRetryScheduled, StatusProcessing}: true,
		{StatusRetryScheduled, StatusCancelled}:  true,
		{StatusProcessing, StatusDelivered}:      true,
		{StatusProcessing, StatusRetryScheduled}: true,
		{StatusProcessing, StatusDeadLetter}:     true,
		{StatusProcessing, StatusPending}:        true,
		{StatusProcessing, StatusCancelled}:      true,
		{StatusDeadLetter, StatusPending}:        true,
	}

	checked := 0
	for _, from := range AllStatuses {
		for _, to := range AllStatuses {
			want := allowed[pair{from, to}]
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				assert.Equal(t, want, CanTransition(from, to))
				err := ValidateTransition(from, to)
				if want {
					assert.NoError(t, err)
				} else {
					assert.Error(t, err)
				}
			})
			checked++
		}
	}
	assert.Equal(t, 36, checked, "every pair of the six states must be checked")
}

func TestUnknownStatusIsRejected(t *testing.T) {
	assert.False(t, CanTransition("bogus", StatusPending))
	assert.False(t, CanTransition(StatusPending, "bogus"))
	assert.False(t, Status("bogus").Valid())
	assert.True(t, StatusDeadLetter.Valid())
}

func TestIsTerminal(t *testing.T) {
	want := map[Status]bool{
		StatusPending: false, StatusProcessing: false, StatusRetryScheduled: false,
		StatusDelivered: true, StatusDeadLetter: true, StatusCancelled: true,
	}
	for _, s := range AllStatuses {
		assert.Equal(t, want[s], s.IsTerminal(), string(s))
	}
}
