package customer_test

import (
	"testing"
	"time"

	"example.org/shop/internal/domain/customer"
	"example.org/shop/internal/domain/customer/mocks"
)

func TestGet(t *testing.T) {
	started := time.Now()
	repo := mocks.NewMockRepository(t)
	uc := customer.NewUsecase(customer.Deps{Repo: repo})
	if uc == nil || time.Since(started) > time.Minute {
		t.Fatal("usecase is not built")
	}
}
