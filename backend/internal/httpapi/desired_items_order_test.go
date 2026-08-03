package httpapi

import (
	"context"
	"testing"

	"ly-route/backend/internal/persistence"
)

func TestDesiredItemsReturnsStableIDOrder(t *testing.T) {
	// Given
	ctx := context.Background()
	store, err := persistence.Open(ctx, "file:desired-items-order?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := fixedClock()()
	for _, id := range []string{"order-b", "order-a"} {
		if err := store.SaveConfig(ctx, configDocument(t, "interface", id, map[string]any{"id": id}, now)); err != nil {
			t.Fatal(err)
		}
	}
	server := New(WithStore(store))

	// When / Then
	for attempt := 0; attempt < 64; attempt++ {
		items, err := server.desiredItems(ctx, "interface")
		if err != nil {
			t.Fatal(err)
		}
		for index := 1; index < len(items); index++ {
			if stringField(items[index-1], "id") > stringField(items[index], "id") {
				t.Fatalf("desired item IDs are not stable on attempt %d: %q before %q", attempt, stringField(items[index-1], "id"), stringField(items[index], "id"))
			}
		}
	}
}
