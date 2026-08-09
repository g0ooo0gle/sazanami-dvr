package recording

import (
	"context"
	"testing"
)

func TestStopRegistryCancelsOnlyMatchingRecordingAndUnregisters(t *testing.T) {
	registry := newStopRegistry(2)
	firstID, secondID := appID(t, 71), appID(t, 72)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	secondContext, cancelSecond := context.WithCancel(context.Background())
	unregisterFirst, err := registry.register(firstID, cancelFirst)
	if err != nil {
		t.Fatal(err)
	}
	defer unregisterFirst()
	unregisterSecond, err := registry.register(secondID, cancelSecond)
	if err != nil {
		t.Fatal(err)
	}
	registry.notify(firstID)
	if firstContext.Err() == nil || secondContext.Err() != nil {
		t.Fatalf("first=%v second=%v", firstContext.Err(), secondContext.Err())
	}
	unregisterSecond()
	registry.notify(secondID)
	if secondContext.Err() != nil {
		t.Fatal("登録解除後の録画が停止しました")
	}
	cancelSecond()
}

func TestStopRegistryRejectsDuplicateAndBoundOverflow(t *testing.T) {
	registry := newStopRegistry(1)
	id := appID(t, 73)
	_, cancel := context.WithCancel(context.Background())
	unregister, err := registry.register(id, cancel)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if _, err := registry.register(id, cancel); err == nil {
		t.Fatal("同じ録画が二重登録されました")
	}
	if _, err := registry.register(appID(t, 74), cancel); err == nil {
		t.Fatal("登録上限を超えました")
	}
}
