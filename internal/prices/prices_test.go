package prices

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSetRejectsNonPositivePrice(t *testing.T) {
	for _, bad := range []int64{0, -1, -150000} {
		if _, err := Set(context.Background(), nil, uuid.New(), bad, uuid.New()); err != ErrInvalidPrice {
			t.Errorf("Set(%d): expected ErrInvalidPrice, got %v", bad, err)
		}
	}
}
