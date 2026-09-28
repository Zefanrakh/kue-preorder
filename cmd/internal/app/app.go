// Package app holds the wiring the api and the worker share, so both
// binaries make the same choices from the same configuration.
package app

import (
	"fmt"

	"github.com/Zefanrakh/kue-preorder/internal/payments"
	"github.com/Zefanrakh/kue-preorder/internal/platform/config"
)

// PaymentProvider picks who makes invoices. Only development may pretend;
// Midtrans arrives in M2.6b, and until then nothing else may take orders.
func PaymentProvider(cfg config.Config) (payments.Provider, error) {
	if cfg.AppEnv == config.EnvDevelopment {
		return payments.DevProvider{}, nil
	}
	return nil, fmt.Errorf("APP_ENV=%s needs a payment provider, and Midtrans arrives in M2.6b: run with APP_ENV=development until then", cfg.AppEnv)
}
