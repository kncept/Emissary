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

