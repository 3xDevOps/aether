package timeline

import (
	"testing"

	"github.com/3xDevOps/Aether/internal/events"
)

func TestRunTitleIsExcludedFromDefaultFeed(t *testing.T) {
	if !detailTypes[events.TypeRunTitle] {
		t.Fatal("run.title should be a detail event in the default feed")
	}
}
