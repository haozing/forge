package agenttask

import "testing"

func TestTaskOperationAndKeyValidation(t *testing.T) {
	if !validOperation(PrepareAsset) {
		t.Fatal("prepare_asset should be recognized")
	}
	if validOperation("") {
		t.Fatal("empty operation should be rejected")
	}
	if validIdempotencyKey("short") {
		t.Fatal("short idempotency keys should be rejected")
	}
	if !validIdempotencyKey("agent-task-create-idempotency") {
		t.Fatal("normal idempotency keys should be accepted")
	}
}
