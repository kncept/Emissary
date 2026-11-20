package base

import "testing"

func TestPageBoundsClampsUntrustedArguments(t *testing.T) {
	position, limit := -5, 10
	start, end := PageBounds(7, &position, &limit)
	if start != 0 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (0, 7)", start, end)
	}

	position, limit = 100, 2
	start, end = PageBounds(7, &position, &limit)
	if start != 7 || end != 7 {
		t.Fatalf("PageBounds() = (%d, %d), want (7, 7)", start, end)
	}

	position, limit = 3, -2
	start, end = PageBounds(7, &position, &limit)
	if start != 3 || end != 3 {
		t.Fatalf("PageBounds() = (%d, %d), want (3, 3)", start, end)
	}
}

func TestQueryChangesResponseRenamesStateFields(t *testing.T) {
	result := QueryChangesResponse(map[string]any{
		"oldState": "email-1",
		"newState": "email-2",
	})
	if result["oldQueryState"] != "email-1" || result["newQueryState"] != "email-2" {
		t.Fatalf("renamed states = %v", result)
	}
	if _, ok := result["oldState"]; ok {
		t.Fatal("oldState should be removed")
	}
	if _, ok := result["newState"]; ok {
		t.Fatal("newState should be removed")
	}
}
