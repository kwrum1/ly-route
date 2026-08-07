package service

import "context"

type transactionContextKey struct{}

func withTransactionID(ctx context.Context, transactionID string) context.Context {
	return context.WithValue(ctx, transactionContextKey{}, transactionID)
}

func transactionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	transactionID, _ := ctx.Value(transactionContextKey{}).(string)
	return transactionID
}
